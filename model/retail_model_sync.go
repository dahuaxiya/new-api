package model

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"

	"gorm.io/gorm"
)

// RetailModelListSource verifies supply enrollment before allowing a credential to be used for discovery.
// Reserved retail suppliers may carry legacy group labels; ApplyRetailRouting already excludes
// these channel IDs from legacy traffic. Mixed-key pools cannot prove group-specific availability.
func RetailModelListSource(db *gorm.DB, id int) (*Channel, error) {
	rows, err := retailBindingChannels(db)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(rows, func(r RetailBindingChannel) bool {
		return r.ID == id && r.HasKey && r.Status == common.ChannelStatusEnabled
	}) {
		return nil, retailprice.ErrAccess
	}
	var ch Channel
	if err = db.First(&ch, id).Error; err != nil {
		return nil, err
	}
	// Recheck the exact row whose bearer token will be sent, not just the earlier selector entry.
	cfg, err := retail_setting.Load()
	if err != nil || !validRetailSupplySite(ch, cfg.Group) {
		return nil, retailprice.ErrAccess
	}
	var occupied int64
	if err = db.Model(&GPTHostingBinding{}).Where("channel_id = ?", id).Count(&occupied).Error; err != nil {
		return nil, err
	}
	if occupied != 0 || !slices.ContainsFunc(rows, func(r RetailBindingChannel) bool { return r.ID == id && r.BaseURL == ch.GetBaseURL() }) || ch.ChannelInfo.IsMultiKey || ch.Type != constant.ChannelTypeOpenAI || ch.Key == "" || strings.ContainsAny(ch.Key, "\r\n") || ch.Status != common.ChannelStatusEnabled || (ch.HeaderOverride != nil && *ch.HeaderOverride != "" && *ch.HeaderOverride != "{}") {
		return nil, retailprice.ErrAccess
	}
	return &ch, nil
}

// ApplyRetailModelList commits abilities and the price book together. Revalidate the credential under
// the price lock: a delayed response for a replaced Key must never open its replacement's routes.
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
			if err != nil || current.Key != source.Key || current.GetBaseURL() != source.GetBaseURL() {
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
