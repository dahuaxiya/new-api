package model

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
)

var ErrRetailSupplyGroupName = common.NewMessage("Supply group name already exists at this site")
var ErrRetailSupplyGroupKey = common.NewMessage("This Key is already used by another group at this site")
var ErrRetailSupplyGroupIncomplete = common.NewMessage("Add a Key and at least one model before enabling it")
var ErrRetailSupplyGroupAddress = common.NewMessage("Edit the site channel to change the shared address or protocol")

// RetailReservedSupplyIDs isolates every site group from legacy wallet/token routing, including
// disabled, orphaned or manually altered groups that are no longer eligible for retail sales.
func RetailReservedSupplyIDs() ([]int, error) {
	cfg, err := retail_setting.Load()
	if err != nil {
		return nil, err
	}
	ids := []int{}
	if err := DB.Model(&Channel{}).Where("retail_site_id > ?", 0).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	for _, supply := range cfg.Supplies {
		if !slices.Contains(ids, supply.ChannelID) {
			ids = append(ids, supply.ChannelID)
		}
	}
	return ids, nil
}

// RetailSupplyConfig expands only groups created under an explicitly approved reseller site.
// The database relationship alone never enrolls an arbitrary or customer-hosted channel.
func RetailSupplyConfig(db *gorm.DB) (retail_setting.Config, error) {
	cfg, err := retail_setting.Load()
	if err != nil || len(cfg.Supplies) == 0 {
		return cfg, err
	}
	parents := map[int]Channel{}
	for _, supply := range cfg.Supplies {
		if supply.Kind != "reseller" {
			continue
		}
		var parent Channel
		if err := db.First(&parent, supply.ChannelID).Error; err == gorm.ErrRecordNotFound {
			continue
		} else if err != nil {
			return cfg, err
		}
		if validRetailSupplySite(parent, cfg.Group) {
			var occupied int64
			if err := db.Model(&GPTHostingBinding{}).Where("channel_id = ?", parent.Id).Count(&occupied).Error; err != nil {
				return cfg, err
			}
			if occupied != 0 {
				continue
			}
			parents[parent.Id] = parent
		}
	}
	if len(parents) == 0 {
		return cfg, nil
	}
	ids := make([]int, 0, len(parents))
	for id := range parents {
		ids = append(ids, id)
	}
	var children []Channel
	if err := db.Where("retail_site_id IN ?", ids).Order("id").Limit(101).Find(&children).Error; err != nil {
		return cfg, err
	}
	if len(children)+len(cfg.Supplies) > 100 {
		return cfg, retail_setting.ErrUnavailable
	}
	for _, child := range children {
		parent := parents[child.RetailSiteID]
		// A manual child address/protocol edit cannot use a trusted site's enrollment.
		// Hosting occupancy is checked again by RetailSupplyIDs before request routing.
		if child.Type != parent.Type || child.GetBaseURL() != parent.GetBaseURL() || !validRetailSupplySite(child, cfg.Group) || slices.ContainsFunc(cfg.Supplies, func(s retail_setting.Supply) bool { return s.ChannelID == child.Id }) {
			continue
		}
		cfg.Supplies = append(cfg.Supplies, retail_setting.Supply{ChannelID: child.Id, Kind: "reseller"})
	}
	return cfg, nil
}

// validRetailSupplySite limits this editor to approved HTTPS OpenAI/Claude supply sites.
// It deliberately rejects private groups; creation never copies custom authentication overrides.
func validRetailSupplySite(ch Channel, group string) bool {
	u, err := url.Parse(ch.GetBaseURL())
	groups := strings.Split(ch.Group, ",")
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(ch.Type == constant.ChannelTypeOpenAI || ch.Type == constant.ChannelTypeAnthropic) && slices.Contains(groups, group) &&
		!slices.ContainsFunc(groups, func(g string) bool { return strings.HasPrefix(g, "acct_") })
}

// CreateRetailSupplyGroup adds a separate credential under an approved site, never copies its Key.
// A missing Key creates a disabled draft; credentials can be added through the channel editor later.
func CreateRetailSupplyGroup(ctx context.Context, siteID int, name, key, models string) (int, error) {
	return createRetailSupplyGroupIn(DB, ctx, siteID, name, key, models)
}

// createRetailSupplyGroupIn keeps creation inside the caller's pricing transaction when supplied.
func createRetailSupplyGroupIn(db *gorm.DB, ctx context.Context, siteID int, name, key, models string) (int, error) {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	parts := strings.FieldsFunc(models, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" || len(parts[i]) > 200 {
			return 0, retail_setting.ErrUnavailable
		}
	}
	models = strings.Join(parts, ",")
	if siteID <= 0 || name == "" || utf8.RuneCountInString(name) > 120 || len(key) > 4096 || strings.ContainsFunc(key, unicode.IsSpace) || len(models) > 16000 || len(strings.Split(models, ",")) > 200 {
		return 0, retail_setting.ErrUnavailable
	}
	var id int
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// All additions under this site lock the same row, serializing duplicate names and Keys.
		var site Channel
		if err := lockForUpdate(tx).First(&site, siteID).Error; err != nil {
			return retail_setting.ErrUnavailable
		}
		cfg, err := RetailSupplyConfig(tx)
		if err != nil || !slices.ContainsFunc(cfg.Supplies, func(s retail_setting.Supply) bool { return s.ChannelID == siteID && s.Kind == "reseller" }) || site.RetailSiteID != 0 || !validRetailSupplySite(site, cfg.Group) || len(cfg.Supplies) >= 100 {
			return retail_setting.ErrUnavailable
		}
		var occupied int64
		if err := tx.Model(&GPTHostingBinding{}).Where("channel_id = ?", siteID).Count(&occupied).Error; err != nil || occupied != 0 {
			return retail_setting.ErrUnavailable
		}
		var duplicate int64
		if err := tx.Model(&Channel{}).Where("(retail_site_id = ? OR id = ?) AND name = ?", siteID, siteID, name).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate != 0 {
			return ErrRetailSupplyGroupName
		}
		// Identical credentials cannot represent distinct upstream groups with independent costs.
		if key != "" {
			if err := tx.Model(&Channel{}).Where("(retail_site_id = ? OR id = ?)", siteID, siteID).Where(map[string]any{"key": key}).Count(&duplicate).Error; err != nil {
				return err
			}
			if duplicate != 0 {
				return ErrRetailSupplyGroupKey
			}
		}
		status := common.ChannelStatusManuallyDisabled
		if key != "" && models != "" {
			status = common.ChannelStatusEnabled
		}
		// Construct a new channel from safe site fields only. Never clone headers, mappings,
		// organization, runtime counters or the parent credential into a different supply group.
		child := Channel{RetailSiteID: siteID, Name: name, Key: key, Models: models, BaseURL: site.BaseURL, Type: site.Type, Group: cfg.Group, Status: status, CreatedTime: common.GetTimestamp()}
		if err := tx.Create(&child).Error; err != nil {
			return err
		}
		if err := child.AddAbilities(tx); err != nil {
			return err
		}
		id = child.Id
		return nil
	})
	return id, err
}

// syncRetailSiteAddress keeps child addresses attached to the site's address in one transaction.
// A child may rotate its Key/models, but cannot silently change procurement host or adapter.
func syncRetailSiteAddress(tx *gorm.DB, ch *Channel) error {
	var stored Channel
	if err := tx.First(&stored, ch.Id).Error; err != nil {
		return err
	}
	anchor := ch.Id
	if stored.RetailSiteID != 0 {
		anchor = stored.RetailSiteID
	}
	var parent Channel
	if err := lockForUpdate(tx).First(&parent, anchor).Error; err != nil {
		return err
	}
	if ch.Key != "" {
		var duplicate int64
		if err := tx.Model(&Channel{}).Where("(retail_site_id = ? OR id = ?) AND id <> ?", anchor, anchor, ch.Id).Where(map[string]any{"key": ch.Key}).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate != 0 {
			return ErrRetailSupplyGroupKey
		}
	}
	if stored.RetailSiteID != 0 {
		if (ch.BaseURL != nil && (&Channel{Type: parent.Type, BaseURL: ch.BaseURL}).GetBaseURL() != parent.GetBaseURL()) || (ch.Type != 0 && ch.Type != parent.Type) {
			return ErrRetailSupplyGroupAddress
		}
		return nil
	}
	updates := map[string]any{}
	if ch.BaseURL != nil {
		updates["base_url"] = *ch.BaseURL
	}
	if ch.Type != 0 {
		updates["type"] = ch.Type
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&Channel{}).Where("retail_site_id = ?", ch.Id).Updates(updates).Error
}

// RetailSupplyEdit is write-only: negative IDs identify drafts within one request, positive IDs edit
// an approved group. A blank Key preserves an existing credential and never means copy the site Key.
type RetailSupplyEdit struct {
	ConfirmKeyGroup string `json:"confirm_key_group,omitempty"`
	ID              int    `json:"id"`
	SiteID          int    `json:"site_id"`
	Name            string `json:"name"`
	Key             string `json:"key"`
	Models          string `json:"models"`
	Enabled         *bool  `json:"enabled,omitempty"`
}

// SaveRetailSupplyConfiguration commits group credentials, model abilities, bindings and margin together.
// The price row is locked before site rows. Any stale version, invalid route or credential failure rolls
// back all changes, so retrying cannot leave paid routes paired with only half of the intended settings.
func SaveRetailSupplyConfiguration(ctx context.Context, expected int64, bindings []retailprice.GroupBinding, enabled bool, snapshot *retailprice.PublicRateSnapshot, actorID int, policy *retailprice.SalePolicy, edits []RetailSupplyEdit) (int64, error) {
	if len(edits) > 100 || len(bindings) > 200 {
		return 0, retailprice.ErrConfig
	}
	var version int64
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		if state.Version != expected {
			return ErrRetailPriceConflict
		}
		channels, err := retailBindingChannels(tx)
		if err != nil {
			return err
		}
		ids := map[int]int{}
		for _, edit := range edits {
			// Only explicit switches may edit a saved credential without an attached route.
			if edit.ID == 0 || ids[edit.ID] != 0 || (edit.Enabled == nil && !slices.ContainsFunc(bindings, func(b retailprice.GroupBinding) bool { return b.ChannelID == edit.ID })) {
				return retailprice.ErrConfig
			}
			if edit.ID < 0 {
				// A draft has no durable credential until a binding references it. Keep the
				// creation policy separate from explicit status changes on saved credentials.
				if edit.Enabled != nil {
					return retailprice.ErrConfig
				}
				id, err := createRetailSupplyGroupIn(tx, ctx, edit.SiteID, edit.Name, edit.Key, edit.Models)
				if err != nil {
					return err
				}
				ids[edit.ID] = id
				continue
			}
			if !slices.ContainsFunc(channels, func(c RetailBindingChannel) bool { return c.ID == edit.ID && c.SiteID == edit.SiteID }) {
				return retailprice.ErrAccess
			}
			name, key := strings.TrimSpace(edit.Name), strings.TrimSpace(edit.Key)
			models := strings.FieldsFunc(edit.Models, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
			for i := range models {
				models[i] = strings.TrimSpace(models[i])
				if models[i] == "" || len(models[i]) > 200 {
					return retailprice.ErrConfig
				}
			}
			if name == "" || utf8.RuneCountInString(name) > 120 || len(key) > 4096 || strings.ContainsFunc(key, unicode.IsSpace) || len(edit.Models) > 16000 || len(models) > 200 {
				return retailprice.ErrConfig
			}
			// Lock the parent before the child, matching creation and generic credential rotation.
			var parent Channel
			if err := lockForUpdate(tx).First(&parent, edit.SiteID).Error; err != nil {
				return err
			}
			var ch Channel
			if err := lockForUpdate(tx).First(&ch, edit.ID).Error; err != nil {
				return err
			}
			cfg, err := retail_setting.Load()
			if err != nil {
				return err
			}
			// Recheck the trust boundary after locking: the generic channel editor may have
			// changed site membership or hosting occupancy since candidates were loaded.
			if parent.RetailSiteID != 0 || !validRetailSupplySite(parent, cfg.Group) ||
				!validRetailSupplySite(ch, cfg.Group) || (ch.Id != parent.Id && ch.RetailSiteID != parent.Id) ||
				ch.Type != parent.Type || ch.GetBaseURL() != parent.GetBaseURL() {
				return retailprice.ErrAccess
			}
			var occupied int64
			if err := tx.Model(&GPTHostingBinding{}).Where("channel_id IN ?", []int{parent.Id, ch.Id}).Count(&occupied).Error; err != nil {
				return err
			}
			if occupied != 0 {
				return retailprice.ErrAccess
			}
			patch := Channel{Id: ch.Id, Key: key}
			if err := syncRetailSiteAddress(tx, &patch); err != nil {
				return err
			}
			// Rotating an existing Key requires confirming its upstream identity. A different
			// upstream group needs its own credential because other routes may still use this one.
			if key != "" && ch.Key != "" && ch.Key != key {
				for _, b := range bindings {
					if b.ChannelID == ch.Id && (edit.ConfirmKeyGroup == "" || edit.ConfirmKeyGroup != b.UpstreamGroup) {
						return retailprice.ErrConfig
					}
				}
			}
			var duplicates int64
			if err := tx.Model(&Channel{}).Where("(retail_site_id = ? OR id = ?) AND id <> ? AND name = ?", edit.SiteID, edit.SiteID, ch.Id, name).Count(&duplicates).Error; err != nil {
				return err
			}
			if duplicates != 0 {
				return ErrRetailSupplyGroupName
			}
			ch.Name, ch.Models = name, strings.Join(models, ",")
			if key != "" {
				ch.Key = key
			}
			// Keep manual or automatic suspension unless the operator explicitly changes it.
			// Validate the final Key and model list before restoring channel abilities.
			if edit.Enabled != nil && *edit.Enabled {
				if ch.Key == "" || ch.Models == "" {
					return ErrRetailSupplyGroupIncomplete
				}
				ch.Status = common.ChannelStatusEnabled
			} else if edit.Enabled != nil || ch.Key == "" || ch.Models == "" {
				ch.Status = common.ChannelStatusManuallyDisabled
			}
			updates := map[string]any{"name": ch.Name, "models": ch.Models, "status": ch.Status}
			if key != "" {
				updates["key"] = key
			}
			if err := tx.Model(&Channel{}).Where("id = ?", ch.Id).Updates(updates).Error; err != nil {
				return err
			}
			if err := ch.UpdateAbilities(tx); err != nil {
				return err
			}
			ids[edit.ID] = edit.ID
		}
		// Copy before resolving draft IDs so failure cannot mutate a caller's retry payload.
		resolved := slices.Clone(bindings)
		for i := range resolved {
			if resolved[i].ChannelID < 0 {
				id := ids[resolved[i].ChannelID]
				if id == 0 {
					return retailprice.ErrConfig
				}
				resolved[i].ChannelID = id
			}
		}
		var policies []retailprice.SalePolicy
		if policy != nil {
			policies = append(policies, *policy)
		}
		version, err = saveRetailGroupBindingsIn(tx, ctx, expected, resolved, enabled, snapshot, actorID, policies...)
		if err != nil {
			return err
		}
		// Credential/model-only changes must also invalidate stale editors. Price audits carry
		// only the price book, never the credential payload; all writes share this transaction.
		if len(edits) > 0 && version == expected {
			if err := tx.First(&state, state.ID).Error; err != nil {
				return err
			}
			version++
			if err := tx.Create(&RetailPriceVersion{Version: version, Payload: state.Payload, ActorID: actorID, CreatedAt: time.Now().Unix()}).Error; err != nil {
				return err
			}
			return tx.Model(&state).Update("version", version).Error
		}
		return nil
	})
	return version, err
}
