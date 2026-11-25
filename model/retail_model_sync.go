package model

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"

	"gorm.io/gorm"
)

// ErrRetailModelSourcePaused means enrollment is valid but the scheduler must skip this channel.
var ErrRetailModelSourcePaused = errors.New("retail model source paused")

// RetailModelListSource resolves a single Key only after the same enrollment check used by the editor.
// It returns a detached snapshot for one read; never save this struct with its resolved credential.
func RetailModelListSource(db *gorm.DB, id int) (*Channel, error) {
	if id <= 0 {
		return nil, retailprice.ErrAccess
	}
	var ch Channel
	if err := db.First(&ch, id).Error; err != nil {
		return nil, err
	}
	rows, err := RetailRateSiteChannels(db)
	if err != nil {
		return nil, err
	}
	// Compare the exact credential, protocol and destination with the enrolled row. Identical hostnames
	// do not imply identical custody: different sites or groups can use different Kiro or Claude Keys.
	i := slices.IndexFunc(rows, func(r RetailBindingChannel) bool {
		return r.ID == id && r.ModelSyncSupported && r.SiteID == ch.supplySiteID() && r.Type == ch.Type && r.BaseURL == ch.GetBaseURL() && r.LibraryID == ch.RetailKeyID && r.credentialStamp == credentialMAC([]byte(ch.Key+"\x00"+ch.GetModelMapping()))
	})
	if i < 0 {
		return nil, retailprice.ErrAccess
	}
	if rows[i].Status != common.ChannelStatusEnabled || ch.Status != common.ChannelStatusEnabled {
		return nil, ErrRetailModelSourcePaused
	}
	ch.Key, err = ch.SupplySecret(db)
	if err == nil && (ch.Key == "" || strings.ContainsAny(ch.Key, "\r\n")) {
		return nil, retailprice.ErrAccess
	}
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

// ApplyRetailModelList commits abilities and the price book together. Revalidate the credential under
// the price lock: a delayed response must not publish to a replaced Key, site, or protocol.
func ApplyRetailModelList(ctx context.Context, expected int64, id int, source *Channel, names []string) (bool, error) {
	if source == nil && len(names) > 0 {
		return false, retailprice.ErrAccess
	}
	changed := false
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		if state.Version != expected {
			return ErrRetailPriceConflict
		}
		var ch Channel
		if source != nil {
			if err := lockForUpdate(tx).First(&ch, id).Error; err != nil {
				return err
			}
			current, err := RetailModelListSource(tx, id)
			if err != nil || current.Id != source.Id || current.Key != source.Key || current.RetailKeyID != source.RetailKeyID || current.supplySiteID() != source.supplySiteID() || current.supplyTarget() != source.supplyTarget() {
				return ErrRetailPriceConflict
			}
		}
		mapping := map[string]string{}
		if raw := ch.GetModelMapping(); raw != "" && common.UnmarshalJsonStr(raw, &mapping) != nil {
			names = nil
		}
		_, err = mutateRetailPriceBookIn(tx, ctx, expected, 0, false, func(book *retailprice.Book) error {
			if !book.Enabled {
				return retailprice.ErrAccess
			}
			models := ch.GetModels()
			before := len(models)
			for _, b := range book.GroupBindings {
				if b.ChannelID != id || !b.FollowModels {
					continue
				}
				for _, name := range book.ApplyModelList(b, names, mapping, time.Now().Unix()) {
					if !slices.Contains(models, name) {
						models = append(models, name)
					}
				}
			}
			if len(models) == before {
				return nil
			}
			ch.Models = strings.Join(models, ",")
			if len(ch.Models) > 16000 || len(models) > 200 {
				return retailprice.ErrConfig
			}
			if err := tx.Model(&ch).Update("models", ch.Models).Error; err != nil {
				return err
			}
			changed = true
			return ch.UpdateAbilities(tx)
		})
		return err
	})
	return changed && err == nil, err
}

// RetailRouteTargets records the outbound ID for each enabled public model and eligible credential.
type RetailRouteTargets map[string]map[int]string

// Eligible returns only routes whose configured upstream ID is still the one the relay sends.
func (targets RetailRouteTargets) Eligible(product retailprice.Product, name string) []int {
	var ids []int
	for _, route := range product.Routes {
		if expected := route.Models[name]; expected != "" && targets[name][route.ChannelID] == expected {
			ids = append(ids, route.ChannelID)
		}
	}
	return ids
}

// RetailCatalogTargets intersects enrolled supplies with enabled abilities and channel mappings.
// Optional names restrict per-request work; an empty list loads the whole catalog for display.
func RetailCatalogTargets(group string, ids []int, names ...string) (RetailRouteTargets, error) {
	return retailCatalogTargetsIn(DB, group, ids, names...)
}

// retailCatalogTargetsIn reads abilities from the same transaction as a proposed supply.
func retailCatalogTargetsIn(db *gorm.DB, group string, ids []int, names ...string) (RetailRouteTargets, error) {
	type row struct {
		Model        string
		ChannelID    int
		ModelMapping *string
	}
	var rows []row
	query := db.Model(&Ability{}).Select("abilities.model, abilities.channel_id, channels.model_mapping").Joins("JOIN channels ON channels.id = abilities.channel_id").Where(&Ability{Group: group, Enabled: true}).Where("channels.status = ? AND abilities.channel_id IN ?", common.ChannelStatusEnabled, ids)
	// Preview callers can pass IDs directly. Filter the small set of library-backed routes once,
	// before expanding it into one result per model. Inline routes keep their existing eligibility.
	var referenced []Channel
	if err := db.Select("id", "retail_site_id", "retail_key_id", "retail_key_target", "type", "base_url", "channel_info").Where("id IN ? AND retail_key_id > 0", ids).Find(&referenced).Error; err != nil {
		return nil, err
	}
	owners, err := retailKeyOwners(db, referenced)
	if err != nil {
		return nil, err
	}
	var blocked []int
	for _, ch := range referenced {
		if !ch.hasSupplyKey(owners) {
			blocked = append(blocked, ch.Id)
		}
	}
	if len(blocked) > 0 {
		query = query.Where("abilities.channel_id NOT IN ?", blocked)
	}
	if len(names) > 0 {
		query = query.Where("abilities.model IN ?", names)
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	mappings := map[int]retailprice.ChannelMapping{}
	targets := RetailRouteTargets{}
	for _, r := range rows {
		mapping, ok := mappings[r.ChannelID]
		if !ok {
			raw := ""
			if r.ModelMapping != nil {
				raw = *r.ModelMapping
			}
			mapping = retailprice.ParseChannelMapping(raw)
			mappings[r.ChannelID] = mapping
		}
		if targets[r.Model] == nil {
			targets[r.Model] = map[int]string{}
		}
		targets[r.Model][r.ChannelID] = mapping.Target(r.Model)
	}
	return targets, nil
}

// RetailDisplayTargets loads the same active supply set for preview and customer catalogs.
func RetailDisplayTargets() (RetailRouteTargets, error) {
	cfg, err := retail_setting.Load()
	if err != nil {
		return nil, err
	}
	ids, err := RetailSupplyIDs(cfg.Group)
	if err != nil {
		return nil, err
	}
	return RetailCatalogTargets(cfg.Group, ids)
}
