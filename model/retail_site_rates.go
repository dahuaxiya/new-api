package model

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
)

// RetailRateSiteChannels 只枚举已获供货授权的单 Key 线路，沿用路由的托管账号隔离规则。
func RetailRateSiteChannels(tx *gorm.DB) ([]RetailBindingChannel, error) {
	cfg, err := RetailSupplyConfig(tx)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	resellers := map[int]bool{}
	ids := make([]int, 0, len(cfg.Supplies))
	for _, s := range cfg.Supplies {
		resellers[s.ChannelID] = s.Kind == "reseller"
		ids = append(ids, s.ChannelID)
	}
	var rows []Channel
	// 停用供货的凭据仍可提供公开报价。客户托管的凭据永远不参与选择或授权。
	occupied := tx.Model(&GPTHostingBinding{}).Select("channel_id").Where("channel_id IN ?", ids)
	if err := tx.Where("id IN ? AND id NOT IN (?)", ids, occupied).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := []RetailBindingChannel{}
	for _, ch := range rows {
		if !validRetailSupplySite(ch, cfg.Group) || ch.ChannelInfo.IsMultiKey || strings.ContainsAny(ch.Key, "\r\n") {
			continue
		}
		id := ch.RetailSiteID
		if id == 0 {
			id = ch.Id
		}
		out = append(out, RetailBindingChannel{credentialStamp: credentialMAC([]byte(ch.Key + "\x00" + ch.GetModelMapping())), AllowNewCredential: ch.Id == id && resellers[id], ID: ch.Id, SiteID: id, Type: ch.Type, Name: ch.Name, BaseURL: ch.GetBaseURL(), Models: ch.Models, Status: ch.Status, HasKey: ch.Key != "", ModelMapping: ch.GetModelMapping()})
	}
	return out, nil
}

// CheckRetailRateEndpoint 只接受选中站点同源的公开接口。浏览器提交的地址不能单独决定出站目的地。
func CheckRetailRateEndpoint(channels []RetailBindingChannel, site int, endpoint string) bool {
	origin := retailprice.RateFeedOrigin(endpoint)
	return origin != "" && slices.ContainsFunc(channels, func(c RetailBindingChannel) bool {
		return c.ID == site && c.SiteID == site && retailprice.RateFeedOrigin(c.BaseURL) == origin
	})
}

// SaveRetailSiteRates 将整份快照和受影响的采购绑定放在同一事务。网络读取在事务外完成。
func SaveRetailSiteRates(ctx context.Context, expected int64, site int, prior, next retailprice.SiteRateFeed, actor int) (int64, error) {
	var version int64
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if actor == 0 {
			// 后台任务只写一个站点，另一个站点的调价不应丢掉本次结果；本站点仍逐份比较旧快照。
			state, err := lockRetailPriceState(tx)
			if err != nil {
				return err
			}
			expected = state.Version
		}
		version, err = mutateRetailPriceBookIn(tx, ctx, expected, actor, true, func(book *retailprice.Book) error {
			current := book.SiteFeeds[site]
			if !reflect.DeepEqual(current, prior) {
				// 时间戳续期不推进版本。暂停应冻结续期后的快照，其余过期抓取必须重新核对。
				if actor <= 0 || next.Follow || next.URL != prior.URL || prior.URL != current.URL || !reflect.DeepEqual(next.Snapshot, prior.Snapshot) || current.LastError != prior.LastError || !sameSyncTariff(retailprice.PublicRateSync{Enabled: prior.Follow, Snapshot: prior.Snapshot}, retailprice.PublicRateSync{Enabled: current.Follow, Snapshot: current.Snapshot}) {
					return ErrRetailPriceConflict
				}
				next.Snapshot = current.Snapshot
			}
			if actor == 0 && (!prior.Follow || !next.Follow || prior.URL != next.URL) {
				return retailprice.ErrAccess
			}
			channels, err := RetailRateSiteChannels(tx)
			if err != nil {
				return err
			}
			// 失去站点授权时只允许把旧报价置为不可用，绝不允许写入新报价。
			if !CheckRetailRateEndpoint(channels, site, next.URL) && !(prior.URL == next.URL && next.LastError != "" && reflect.DeepEqual(prior.Snapshot, next.Snapshot)) {
				return retailprice.ErrAccess
			}
			if next.Snapshot == nil {
				return retailprice.ErrConfig
			}
			if prior.URL == next.URL && prior.Snapshot != nil && (prior.Snapshot.UpdatedAt > next.Snapshot.UpdatedAt || prior.Snapshot.FetchedAt > next.Snapshot.FetchedAt) {
				return ErrRetailPriceConflict
			}
			if book.SiteFeeds == nil {
				book.SiteFeeds = map[int]retailprice.SiteRateFeed{}
			}
			book.SiteFeeds[site] = next
			if book.RateSiteByChannel == nil {
				book.RateSiteByChannel = map[int]int{}
			}
			for _, ch := range channels {
				if ch.SiteID == site && retailprice.RateFeedOrigin(ch.BaseURL) == retailprice.RateFeedOrigin(next.URL) {
					book.RateSiteByChannel[ch.ID] = site
				}
			}
			// 报价时按凭据归属解析最新采购价，抓取任务不改已发布商品、路由或手工费率登记。
			return nil
		})
		return err
	})
	return version, err
}

// GetQuotableRetailPriceBook 核对当前授权后提供报价视图。配置页和任务继续读取原记录，以便重新绑定并恢复。
func GetQuotableRetailPriceBook() (retailprice.Book, int64, error) {
	book, version, err := GetRetailPriceBook()
	if err == nil {
		err = checkRetailRateAccess(DB, &book)
	}
	return book, version, err
}

// checkRetailRateAccess 在报价和资金预留前核实站点、协议与托管归属。一次批量读取，无需读取任何 Key。
func checkRetailRateAccess(db *gorm.DB, book *retailprice.Book) error {
	if len(book.RateSiteByChannel) == 0 {
		return nil
	}
	cfg, err := retail_setting.Load()
	if err != nil {
		return err
	}
	kinds := map[int]string{}
	for _, s := range cfg.Supplies {
		kinds[s.ChannelID] = s.Kind
	}
	ids := append(slices.Collect(maps.Keys(book.RateSiteByChannel)), slices.Collect(maps.Keys(book.SiteFeeds))...)
	var channels []Channel
	occupied := db.Model(&GPTHostingBinding{}).Select("channel_id").Where("channel_id IN ?", ids)
	if err := db.Select("id", "retail_site_id", "type", "base_url", "group", "channel_info").Where("id IN ? AND id NOT IN (?)", ids, occupied).Find(&channels).Error; err != nil {
		return err
	}
	valid := map[int]Channel{}
	for _, ch := range channels {
		if cfg.Enabled && validRetailSupplySite(ch, cfg.Group) && !ch.ChannelInfo.IsMultiKey {
			valid[ch.Id] = ch
		}
	}
	book.UnavailableRateChannels = map[int]bool{}
	for id, site := range book.RateSiteByChannel {
		parent, ok := valid[site]
		ch, exists := valid[id]
		// 允许停用供货的 Key 继续提供公开倍率；站点下架、转托管、改协议或改域名则停止使用该报价。
		if !ok || !exists || parent.RetailSiteID != 0 || kinds[site] == "" || retailprice.RateFeedOrigin(parent.GetBaseURL()) != retailprice.RateFeedOrigin(book.SiteFeeds[site].URL) ||
			(id != site && (kinds[site] != "reseller" || ch.RetailSiteID != site || ch.GetBaseURL() != parent.GetBaseURL() || ch.Type != parent.Type)) {
			book.UnavailableRateChannels[id] = true
		}
	}
	return nil
}
