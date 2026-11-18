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

// RetailCatalogAbilities loads enabled model-to-supply pairs in one query for an entire catalog.
// Eligibility supplied by the caller already excludes disabled, private and occupied channels.
func RetailCatalogAbilities(group string, ids []int) (map[string][]int, error) {
	var rows []Ability
	err := DB.Select("model", "channel_id").Where(commonGroupCol+" = ? AND enabled = ? AND channel_id IN ?", group, true, ids).Find(&rows).Error
	result := map[string][]int{}
	for _, r := range rows {
		result[r.Model] = append(result[r.Model], r.ChannelId)
	}
	return result, err
}
