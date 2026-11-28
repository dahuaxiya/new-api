package model

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"gorm.io/gorm"
)

type retailKeySelection struct {
	key    RetailUpstreamKey
	site   RetailBindingChannel
	models []string
}

// LaunchRetailGroupKeys accepts inventory IDs only. Read each Key's model list outside the price lock,
// using the enrolled site's protocol and address, then recheck both before committing the route.
func LaunchRetailGroupKeys(ctx context.Context, in RetailGroupLaunch, actor int, discover func(context.Context, int, string, string) ([]string, error)) (result RetailLaunchResult, err error) {
	if actor <= 0 || in.LibraryID <= 0 || len(in.KeyRoutes) > 99 || in.Key != "" || len(in.Models) > 0 || len(in.Routes) > 0 || in.Upstream != "" || in.Site != 0 || in.Channel != 0 || discover == nil {
		return result, retailprice.ErrConfig
	}
	db := DB.WithContext(ctx)
	sites, err := RetailRateSiteChannels(db)
	if err != nil {
		return result, err
	}
	selections := []retailKeySelection{}
	seen := map[int]bool{}
	for _, id := range append([]int{in.LibraryID}, in.KeyRoutes...) {
		if id <= 0 || seen[id] {
			return result, retailprice.ErrConfig
		}
		seen[id] = true
		var key RetailUpstreamKey
		if err := db.First(&key, id).Error; err != nil || !retailprice.ValidBindingGroup(key.UpstreamGroup) {
			return result, retailprice.ErrConfig
		}
		index := slices.IndexFunc(sites, func(ch RetailBindingChannel) bool { return ch.ID == key.OwnerSiteID && ch.SiteID == ch.ID })
		if index < 0 {
			return result, retailprice.ErrAccess
		}
		site := sites[index]
		// Check existing destination pins before sending the Key for discovery. An invalidated route cannot be revived by opening a preview.
		if slices.ContainsFunc(sites, func(ch RetailBindingChannel) bool {
			return ch.SiteID == key.OwnerSiteID && ch.LibraryID == key.ID && (!ch.HasKey || ch.BaseURL != site.BaseURL || ch.Type != site.Type)
		}) {
			return result, retailprice.ErrAccess
		}
		selection := retailKeySelection{key: key, site: site}
		if !(in.PricingOnly && id == in.LibraryID) {
			secret, err := key.secret()
			if err != nil {
				return result, err
			}
			selection.models, err = discover(ctx, site.Type, site.BaseURL, secret)
			if err != nil {
				return result, err
			}
			slices.Sort(selection.models)
			selection.models = slices.Compact(selection.models)
			if len(selection.models) == 0 || len(selection.models) > 500 || slices.ContainsFunc(selection.models, func(model string) bool { return model == "" || len(model) > 200 || strings.ContainsAny(model, ",\r\n") }) {
				return result, ErrRetailSupplyGroupIncomplete
			}
		}
		selections = append(selections, selection)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		price, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		if price.Version != in.Version {
			return ErrRetailPriceConflict
		}
		var book retailprice.Book
		if err := common.UnmarshalJsonStr(price.Payload, &book); err != nil {
			return retailprice.ErrConfig
		}
		eligible, err := RetailRateSiteChannels(tx)
		if err != nil {
			return err
		}
		in.provisionalKeys = map[int]int{}
		in.routeKeys = map[int]int{}
		for index, selection := range selections {
			var current RetailUpstreamKey
			if err := lockForUpdate(tx).First(&current, selection.key.ID).Error; err != nil || current.Version != selection.key.Version || current.OwnerSiteID != selection.key.OwnerSiteID || current.UpstreamGroup != selection.key.UpstreamGroup || !slices.ContainsFunc(eligible, func(ch RetailBindingChannel) bool {
				return ch.ID == current.OwnerSiteID && ch.SiteID == ch.ID && ch.BaseURL == selection.site.BaseURL && ch.Type == selection.site.Type
			}) {
				return ErrRetailPriceConflict
			}
			channel, created, err := selection.resolveRoute(tx, ctx, eligible, index == 0 && in.PricingOnly, book.Models)
			if err != nil {
				return err
			}
			if created {
				in.provisionalKeys[channel] = current.ID
			}
			in.routeKeys[channel] = current.ID
			if index == 0 {
				in.Site, in.Channel, in.Upstream = current.OwnerSiteID, channel, current.UpstreamGroup
			} else {
				in.Routes = append(in.Routes, RetailLaunchRoute{ChannelID: channel, UpstreamGroup: current.UpstreamGroup})
			}
		}
		result, err = launchRetailGroupIn(tx, ctx, in, actor)
		if err != nil {
			return err
		}
		// A preview can create a backing channel to verify all pricing and routing invariants. Roll it back as well.
		if !result.Applied {
			return errLaunchPreview
		}
		return nil
	})
	if errors.Is(err, errLaunchPreview) {
		err = nil
	}
	return result, err
}

// resolveRoute 复用同站点同凭据的唯一线路，新库存凭据创建共享线路，停用线路须先显式启用。
// catalog 必须来自当前已锁定价格版本，仅用于保留管理员已登记且报价有效的视频模型。
func (selection retailKeySelection) resolveRoute(tx *gorm.DB, ctx context.Context, eligible []RetailBindingChannel, pricingOnly bool, catalog []retailprice.Model) (int, bool, error) {
	key := selection.key
	secret, err := key.secret()
	if err != nil {
		return 0, false, err
	}
	var peers []Channel
	if err := lockForUpdate(tx).Where("id = ? OR retail_site_id = ?", key.OwnerSiteID, key.OwnerSiteID).Find(&peers).Error; err != nil {
		return 0, false, err
	}
	var matches []Channel
	for _, peer := range peers {
		if peer.RetailKeyID == key.ID || (peer.RetailKeyID == 0 && peer.Key == secret) {
			matches = append(matches, peer)
		}
	}
	var channel Channel
	if len(matches) > 1 {
		return 0, false, ErrRetailSupplyGroupKey
	}
	created := len(matches) == 0
	if created {
		if pricingOnly {
			return 0, false, retailprice.ErrConfig
		}
		id, err := createRetailSupplyGroupIn(tx, ctx, key.OwnerSiteID, fmt.Sprintf("Key #%d", key.ID), "", "")
		if err != nil {
			return 0, false, err
		}
		if err = tx.First(&channel, id).Error; err != nil {
			return 0, false, err
		}
		channel.Status = common.ChannelStatusEnabled
	} else {
		channel = matches[0]
		if !slices.ContainsFunc(eligible, func(row RetailBindingChannel) bool {
			return row.ID == channel.Id && row.SiteID == key.OwnerSiteID && row.HasKey
		}) || (!pricingOnly && channel.Status != common.ChannelStatusEnabled) {
			return 0, false, retailprice.ErrAccess
		}
	}
	if err := checkSiteSecret(tx, channel, secret); err != nil {
		return 0, false, err
	}
	updates := map[string]any{"retail_key_id": key.ID, "retail_key_target": channel.supplyTarget(), "key": "", "status": channel.Status}
	if !pricingOnly {
		models := slices.Clone(selection.models)
		registered := channel.GetModels()
		now := time.Now().Unix()
		// 部分供应商的 /v1/models 省略异步视频模型。仅保留本线路人工登记并有有效手工报价的模型，
		// 避免一次目录读取覆盖已核实的视频能力，也不借此恢复普通模型或添加其他凭据的模型。
		for _, tariff := range catalog {
			if tariff.Kind == "video" && tariff.PriceMode == "manual" && tariff.Video != nil &&
				tariff.ID == tariff.OfficialModel && tariff.VerifiedAt <= now && tariff.ValidUntil > now &&
				slices.Contains(registered, tariff.ID) {
				models = append(models, tariff.ID)
			}
		}
		slices.Sort(models)
		models = slices.Compact(models)
		channel.Models = strings.Join(models, ",")
		if len(models) > 500 || len(channel.Models) > 16000 {
			return 0, false, ErrRetailSupplyGroupIncomplete
		}
		updates["models"] = channel.Models
	}
	if err := tx.Table("channels").Where("id = ?", channel.Id).Updates(updates).Error; err != nil {
		return 0, false, err
	}
	if !pricingOnly {
		if err := channel.UpdateAbilities(tx); err != nil {
			return 0, false, err
		}
	}
	return channel.Id, created, nil
}
