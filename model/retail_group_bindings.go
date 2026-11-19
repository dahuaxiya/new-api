package model

import (
	"context"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
)

// RetailBindingChannel exposes only safe operator labels, never a channel credential.
type RetailBindingChannel struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	SiteID       int    `json:"site_id"`
	BaseURL      string `json:"base_url"`
	Type         int    `json:"type"`
	Models       string `json:"models"`
	HasKey       bool   `json:"has_key"`
	Status       int    `json:"status"`
	ModelMapping string `json:"-"` // Internal only: used to compare published and actual upstream model IDs.
}

// RetailBindingChannels lists explicitly authorized Xingxing reseller credentials.
// A matching hostname alone does not enroll a customer-owned or unapproved channel.
func RetailBindingChannels() ([]RetailBindingChannel, error) {
	return retailBindingChannels(DB)
}

// retailBindingChannels uses the caller transaction so newly created groups participate in the same save.
func retailBindingChannels(db *gorm.DB) ([]RetailBindingChannel, error) {
	cfg, err := RetailSupplyConfig(db)
	if err != nil {
		return nil, err
	}
	ids := []int{}
	for _, supply := range cfg.Supplies {
		if supply.Kind == "reseller" {
			ids = append(ids, supply.ChannelID)
		}
	}
	var channels []Channel
	if len(ids) > 0 {
		if err := db.Select("id", "name", "base_url", "type", "group", "retail_site_id", "models", "key", "status", "model_mapping").Where("id IN ?", ids).Find(&channels).Error; err != nil {
			return nil, err
		}
	}
	result := []RetailBindingChannel{}
	for _, channel := range channels {
		if channel.BaseURL == nil {
			continue
		}
		endpoint, err := url.Parse(*channel.BaseURL)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host != "aixingxing.pro" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/" && endpoint.Path != "/v1") {
			continue
		}
		// Dedicated customer groups must never become public procurement via this editor.
		groups := strings.Split(channel.Group, ",")
		if !slices.Contains(groups, cfg.Group) || slices.ContainsFunc(groups, func(group string) bool { return strings.HasPrefix(group, "acct_") }) {
			continue
		}
		siteID := channel.RetailSiteID
		if siteID == 0 {
			siteID = channel.Id
		}
		result = append(result, RetailBindingChannel{ID: channel.Id, Name: channel.Name, SiteID: siteID, BaseURL: channel.GetBaseURL(), Type: channel.Type, Models: channel.Models, HasKey: channel.Key != "", Status: channel.Status, ModelMapping: channel.GetModelMapping()})
	}
	return result, nil
}

// mutateRetailPriceBook serializes operator edits and feed updates with request admission.
// A timestamp-only renewal keeps the price version and avoids a full audit copy every minute.
func mutateRetailPriceBook(ctx context.Context, expected int64, actorID int, renewal bool, edit func(*retailprice.Book) error) (int64, error) {
	return mutateRetailPriceBookIn(DB, ctx, expected, actorID, renewal, edit)
}

// mutateRetailPriceBookIn composes price publication with credential edits without a second connection.
func mutateRetailPriceBookIn(db *gorm.DB, ctx context.Context, expected int64, actorID int, renewal bool, edit func(*retailprice.Book) error) (int64, error) {
	if expected < 0 || actorID < 0 {
		return 0, retailprice.ErrConfig
	}
	var version int64
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		if state.Version != expected {
			return ErrRetailPriceConflict
		}
		var book retailprice.Book
		if common.UnmarshalJsonStr(state.Payload, &book) != nil {
			return retailprice.ErrConfig
		}
		oldSync := book.PublicRateSync
		if err := edit(&book); err != nil {
			return err
		}
		now := time.Now().Unix()
		if err := book.Validate(now); err != nil {
			return err
		}
		cfg, err := RetailSupplyConfig(tx)
		if err != nil {
			return err
		}
		for _, product := range book.Products {
			for _, route := range product.Routes {
				if !slices.ContainsFunc(cfg.Supplies, func(s retail_setting.Supply) bool { return s.ChannelID == route.ChannelID }) {
					return retailprice.ErrAccess
				}
			}
		}
		payload, err := common.Marshal(book)
		if err != nil || len(payload) > 1<<20 {
			return retailprice.ErrConfig
		}
		version = state.Version
		if string(payload) == state.Payload {
			return nil
		}
		samePrices := renewal && oldSync.Enabled == book.PublicRateSync.Enabled && oldSync.LastError == book.PublicRateSync.LastError && oldSync.Snapshot != nil && book.PublicRateSync.Snapshot != nil && reflect.DeepEqual(oldSync.Snapshot.Groups, book.PublicRateSync.Snapshot.Groups)
		if !samePrices {
			version++
			if err := tx.Create(&RetailPriceVersion{Version: version, Payload: string(payload), ActorID: actorID, CreatedAt: now}).Error; err != nil {
				return err
			}
		}
		// A renewal changes freshness only. Accepted requests already carry their own
		// immutable expiry and sale schedule; no earlier quote gains a longer lifetime.
		return tx.Model(state).Updates(map[string]any{"version": version, "payload": string(payload)}).Error
	})
	return version, err
}

// SaveRetailGroupBindings atomically saves mappings, consent and optional resale economics.
// Catalogs, request admission and wallet units remain unchanged before and after first publication.
// Unknown groups and credentials are rejected server-side. Removing a route in use requires first
// removing that route from its product, so deletion cannot restore an obsolete manual cost.
func SaveRetailGroupBindings(ctx context.Context, expected int64, bindings []retailprice.GroupBinding, enabled bool, snapshot *retailprice.PublicRateSnapshot, actorID int, policy ...retailprice.SalePolicy) (int64, error) {
	return saveRetailGroupBindingsIn(DB, ctx, expected, bindings, enabled, snapshot, actorID, policy...)
}

// saveRetailGroupBindingsIn validates route ownership against the same transaction that writes credentials.
func saveRetailGroupBindingsIn(db *gorm.DB, ctx context.Context, expected int64, bindings []retailprice.GroupBinding, enabled bool, snapshot *retailprice.PublicRateSnapshot, actorID int, policy ...retailprice.SalePolicy) (int64, error) {
	if actorID <= 0 || len(policy) > 1 {
		return 0, retailprice.ErrConfig
	}
	channels, err := retailBindingChannels(db)
	if err != nil {
		return 0, err
	}
	localGroups := ratio_setting.GetGroupRatioCopy()
	return mutateRetailPriceBookIn(db, ctx, expected, actorID, false, func(book *retailprice.Book) error {
		// Seed from the old routes before replacing them, including the last route being removed.
		groups := book.RegisteredGroups()
		for _, binding := range bindings {
			if group := groups[binding.ChannelID]; group != "" && group != binding.UpstreamGroup {
				return retailprice.ErrConfig
			}
			groups[binding.ChannelID] = binding.UpstreamGroup
			if _, ok := localGroups[binding.LocalGroup]; !ok {
				return retailprice.ErrAccess
			}
			if !slices.ContainsFunc(channels, func(ch RetailBindingChannel) bool { return ch.ID == binding.ChannelID }) {
				return retailprice.ErrAccess
			}
			if snapshot == nil || !slices.ContainsFunc(snapshot.Groups, func(rate retailprice.PublicRate) bool { return rate.Group == binding.UpstreamGroup }) {
				return retailprice.ErrConfig
			}
		}
		for _, previous := range book.GroupBindings {
			if slices.Contains(bindings, previous) {
				continue
			}
			// Reassigning the same pair is intentional and versioned; deleting an in-use
			// binding is refused rather than silently switching it back to manual pricing.
			if slices.ContainsFunc(bindings, func(next retailprice.GroupBinding) bool {
				return next.LocalGroup == previous.LocalGroup && next.ChannelID == previous.ChannelID
			}) {
				continue
			}
			for _, product := range book.Products {
				// Public products buy through their pinned group. Legacy private products use the caller's allowed group.
				if product.BindingGroup != previous.LocalGroup && (product.BindingGroup != "" || !slices.Contains(product.UserGroups, previous.LocalGroup)) {
					continue
				}
				if slices.ContainsFunc(product.Routes, func(route retailprice.Route) bool { return route.ChannelID == previous.ChannelID }) {
					return retailprice.ErrAccess
				}
			}
		}
		// An absent policy preserves older consoles. Explicit values must be validated
		// even for an empty book, whose catalog validation permits initial setup.
		if len(policy) == 1 {
			if err := policy[0].Validate(); err != nil {
				return err
			}
			book.ReceiptRatio = policy[0].ReceiptRatio
			book.MarginBPS, book.FeeBPS, book.BufferBPS = policy[0].MarginBPS, policy[0].FeeBPS, policy[0].BufferBPS
			book.SalePolicyManaged = true
		}
		// Only uninterrupted automatic following inherits observations. Manual overrides and a fresh opt-in start without old pauses.
		for _, old := range book.GroupBindings {
			if !slices.ContainsFunc(bindings, func(next retailprice.GroupBinding) bool {
				return old.LocalGroup == next.LocalGroup && old.ChannelID == next.ChannelID && old.UpstreamGroup == next.UpstreamGroup && old.FollowModels && next.FollowModels
			}) {
				delete(book.ModelSync, old.SyncID())
			}
		}
		book.CredentialGroups = groups
		book.GroupBindings = bindings
		book.PublicRateSync = retailprice.PublicRateSync{Enabled: enabled, Snapshot: snapshot}
		if enabled {
			for _, binding := range bindings {
				if _, err := book.BoundRate(binding, time.Now().Unix()); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// SyncRetailGroupRates applies a complete feed or marks it unavailable; operator edits win version conflicts.
// A failed read retains the old values for diagnostics only, never for new procurement.
func SyncRetailGroupRates(ctx context.Context, expected int64, snapshot *retailprice.PublicRateSnapshot, failed bool) (int64, error) {
	return mutateRetailPriceBook(ctx, expected, 0, true, func(book *retailprice.Book) error {
		if !book.PublicRateSync.Enabled || len(book.GroupBindings) == 0 {
			return retailprice.ErrAccess
		}
		if failed {
			book.PublicRateSync.LastError = "Unable to read upstream public multipliers"
			return nil
		}
		if snapshot == nil {
			return retailprice.ErrConfig
		}
		previous := book.PublicRateSync.Snapshot
		// A delayed response must not roll a newer snapshot back, including when its prices match.
		if previous != nil && (snapshot.FetchedAt < previous.FetchedAt || snapshot.UpdatedAt < previous.UpdatedAt) {
			return ErrRetailPriceConflict
		}
		book.PublicRateSync.Snapshot = snapshot
		book.PublicRateSync.LastError = ""
		return nil
	})
}
