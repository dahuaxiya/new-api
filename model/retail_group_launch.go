package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
)

// RetailGroupLaunch addresses one public group. Only new credentials accept a Key and discovered model list.
type RetailGroupLaunch struct {
	LibraryID       int                 `json:"library_id"`
	KeyRoutes       []int               `json:"additional_key_ids"`
	provisionalKeys map[int]int         // 预览回滚后数据库序号可能变化，用稳定的库存编号计算复核摘要。
	routeKeys       map[int]int         // 仅由事务内核实过的库存路由产生，不接受请求体覆盖。
	Apply           bool                `json:"apply,omitempty"` // Direct publication uses only the locked server-side quote.
	Version         int64               `json:"expected_version"`
	ProductID       string              `json:"product_id"`
	Site            int                 `json:"site_id"`
	Channel         int                 `json:"channel_id"`
	Group           string              `json:"local_group"`
	Upstream        string              `json:"upstream_group"`
	Key             string              `json:"key"`
	Models          []string            `json:"models"`
	Margin          *int                `json:"margin_bps"`
	PricingOnly     bool                `json:"pricing_only"`
	Routes          []RetailLaunchRoute `json:"additional_routes"`
	Review          string              `json:"review"`
}

// RetailLaunchRoute selects a registered credential and its procurement tariff.
// A blank RateID uses the upstream public feed; a manual rate never controls the group's selling multiplier.
type RetailLaunchRoute struct {
	ChannelID     int    `json:"channel_id"`
	UpstreamGroup string `json:"upstream_group"`
	RateID        string `json:"rate_id"`
}

type RetailLaunchRow struct {
	Model    string             `json:"model"`
	Tiers    []retailprice.Tier `json:"tiers,omitempty"`
	Reason   string             `json:"reason,omitempty"`
	Channels []int              `json:"channels,omitempty"`
}
type RetailLaunchResult struct {
	Applied             bool              `json:"applied"` // Set after the outer transaction commits; previews can also carry a review digest.
	UnavailableChannels []int             `json:"unavailable_channels,omitempty"`
	Rows                []RetailLaunchRow `json:"rows"`
	Review              string            `json:"review"`
	Version             int64             `json:"version"`
	SaleMultiplier      string            `json:"sale_multiplier,omitempty"`
}

var errLaunchPreview = errors.New("rollback group preview")
var ErrRetailGroupRateConflict = errors.New("This upstream group already has a different procurement rate")
var ErrRetailGroupExists = errors.New("This call group is already published")
var ErrRetailLaunchPaused = errors.New("Enable retail sales and load the upstream rates before publishing")

// LaunchRetailGroup runs preview and confirmation against the same locked book. Preview rolls back credential,
// ability and audit writes. Confirmation compares visible tariffs again before allowing the transaction to commit.
func LaunchRetailGroup(ctx context.Context, in RetailGroupLaunch, actor int) (RetailLaunchResult, error) {
	return launchRetailGroupIn(DB, ctx, in, actor)
}

// launchRetailGroupIn 发布调用分组，供库存选择与商品报价共用一次事务。
func launchRetailGroupIn(db *gorm.DB, ctx context.Context, in RetailGroupLaunch, actor int) (RetailLaunchResult, error) {
	out := RetailLaunchResult{Rows: []RetailLaunchRow{}}
	if (in.Apply && in.Review != "") || actor <= 0 || in.Site <= 0 || in.Channel < 0 || in.Group == "" || len(in.Group) > 100 || len(in.Models) > 200 || len(in.Routes) > 99 || (in.Margin != nil && (*in.Margin < 0 || *in.Margin > 9500)) {
		return out, retailprice.ErrConfig
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		version, err := mutateRetailPriceBookIn(tx, ctx, in.Version, actor, false, func(book *retailprice.Book) error {
			if !book.Enabled || (!book.PublicRateSync.Enabled && book.SiteFeeds[in.Site].Snapshot == nil) {
				return ErrRetailLaunchPaused
			}
			productIndex := slices.IndexFunc(book.Products, func(p retailprice.Product) bool {
				return p.Name == in.Group || p.BindingGroup == in.Group
			})
			if productIndex >= 0 && in.ProductID == "" {
				return ErrRetailGroupExists
			}
			if in.ProductID != "" && (productIndex < 0 || book.Products[productIndex].ID != in.ProductID ||
				!book.Products[productIndex].Public || book.Products[productIndex].BindingGroup != in.Group) {
				return retailprice.ErrAccess
			}
			id := in.Channel
			if id == 0 {
				if strings.TrimSpace(in.Key) == "" || len(in.Models) == 0 || slices.ContainsFunc(in.Models, func(s string) bool { return s == "" || strings.ContainsAny(s, ",\n\r") }) {
					return ErrRetailSupplyGroupIncomplete
				}
				var err error
				id, err = createRetailSupplyGroupIn(tx, ctx, in.Site, in.Group, in.Key, strings.Join(in.Models, ","))
				if err != nil {
					return err
				}
			} else if in.Key != "" || len(in.Models) != 0 {
				return retailprice.ErrConfig
			}
			channels, err := retailBindingChannels(tx)
			if err != nil {
				return err
			}
			cfg, err := retail_setting.Load()
			if err != nil {
				return err
			}
			options, err := RetailRouteChannels(tx, cfg.Group)
			if err != nil {
				return err
			}
			rateChannels, err := RetailRateSiteChannels(tx)
			if err != nil {
				return err
			}
			for _, option := range rateChannels {
				if feed, ok := book.SiteFeeds[option.SiteID]; ok && CheckRetailRateEndpoint(rateChannels, option.SiteID, feed.URL) && retailprice.RateFeedOrigin(option.BaseURL) == retailprice.RateFeedOrigin(feed.URL) {
					if book.RateSiteByChannel == nil {
						book.RateSiteByChannel = map[int]int{}
					}
					book.RateSiteByChannel[option.ID] = option.SiteID
					if !slices.ContainsFunc(channels, func(c RetailBindingChannel) bool { return c.ID == option.ID }) {
						channels = append(channels, option)
					}
				}
			}
			if err := checkRetailRateAccess(tx, book); err != nil {
				return err
			}
			groups := book.RegisteredGroups()
			idx := slices.IndexFunc(channels, func(c RetailBindingChannel) bool {
				return c.ID == id && c.SiteID == in.Site && ((in.PricingOnly && groups[id] == in.Upstream) || (c.HasKey && c.Status == common.ChannelStatusEnabled))
			})
			if idx < 0 {
				return retailprice.ErrAccess
			}
			ch := channels[idx]
			if !in.PricingOnly && strings.TrimSpace(ch.Models) == "" {
				return ErrRetailSupplyGroupIncomplete
			}
			ids := []int{}
			scopedBindings := []retailprice.GroupBinding{}
			supplies := []retailprice.CatalogSupply{}
			names := map[string]bool{}
			rateIDs := map[int]string{}
			var sourceBinding retailprice.GroupBinding
			now := time.Now().Unix()
			for _, route := range append([]RetailLaunchRoute{{ChannelID: id, UpstreamGroup: in.Upstream}}, in.Routes...) {
				idx := slices.IndexFunc(options, func(c RetailBindingChannel) bool { return c.ID == route.ChannelID })
				pricingOnly := in.PricingOnly && route.ChannelID == id
				// 停用已经绑定的来源只会关掉供货。保留已核实的公开报价时，无需重新启用 Key。
				if idx < 0 && !(pricingOnly && groups[id] == in.Upstream) {
					return retailprice.ErrConfig
				}
				if (route.ChannelID == id && sourceBinding.ChannelID == id) || slices.Contains(ids, route.ChannelID) || !retailprice.ValidBindingGroup(route.UpstreamGroup) {
					return retailprice.ErrConfig
				}
				if pinned := groups[route.ChannelID]; pinned != "" && pinned != route.UpstreamGroup {
					return retailprice.ErrConfig
				}
				groups[route.ChannelID] = route.UpstreamGroup
				binding := retailprice.GroupBinding{LocalGroup: in.Group, ChannelID: route.ChannelID, UpstreamGroup: route.UpstreamGroup}
				publicRate := slices.ContainsFunc(channels, func(c RetailBindingChannel) bool { return c.ID == route.ChannelID })
				if publicRate {
					if route.RateID != "" {
						return retailprice.ErrConfig
					}
					if _, err := book.BoundRate(binding); err != nil {
						return err
					}
					if previous := book.Binding(in.Group, route.ChannelID); previous != nil {
						binding = *previous
					} else {
						book.GroupBindings = append(book.GroupBindings, binding)
					}
				} else {
					// 手工采购费率只在第一次登记时选择，后续共用。确认金额变动只需更新对应费率表项。
					site := options[idx].SiteID
					if route.RateID == "" {
						route.RateID = book.SiteGroupRates[site][route.UpstreamGroup]
					}
					if slices.ContainsFunc(book.GroupBindings, func(b retailprice.GroupBinding) bool { return b.ChannelID == route.ChannelID }) ||
						!slices.ContainsFunc(book.Rates, func(rate retailprice.Rate) bool { return rate.ID == route.RateID && rate.ValidUntil > now }) {
						return retailprice.ErrConfig
					}
					if err := registerRetailGroupRate(book, site, route.UpstreamGroup, route.RateID); err != nil {
						return err
					}
				}
				if route.ChannelID == id {
					sourceBinding = binding
					if in.PricingOnly {
						// 此绑定只读取采购倍率，停止轮询模型，避免恢复已从供货清单移除的线路。
						book.Binding(in.Group, id).FollowModels = false
						delete(book.ModelSync, binding.SyncID())
						continue
					}
				}
				option := options[idx]
				ids = append(ids, route.ChannelID)
				rateIDs[route.ChannelID] = route.RateID
				scopedBindings = append(scopedBindings, binding)
				supplies = append(supplies, retailprice.CatalogSupply{ID: option.ID, Models: option.Models, Mapping: option.ModelMapping, Available: true})
				for name := range strings.SplitSeq(option.Models, ",") {
					if name = strings.TrimSpace(name); name != "" {
						names[name] = true
					}
				}
			}
			book.CredentialGroups = groups
			if productIndex >= 0 {
				// 仅移除该组独占的旧绑定。凭据自己的上游分组归属仍然保留，其他商品的绑定仍可使用。
				book.GroupBindings = slices.DeleteFunc(book.GroupBindings, func(b retailprice.GroupBinding) bool {
					if b.LocalGroup != in.Group || slices.Contains(ids, b.ChannelID) || b.ChannelID == id {
						return false
					}
					for i, p := range book.Products {
						if i != productIndex && (p.BindingGroup == in.Group || (p.BindingGroup == "" && slices.Contains(p.UserGroups, in.Group))) &&
							(p.RateSourceChannelID == b.ChannelID || slices.ContainsFunc(p.Routes, func(r retailprice.Route) bool { return r.ChannelID == b.ChannelID })) {
							return false
						}
					}
					delete(book.ModelSync, b.SyncID())
					return true
				})
			}

			// 仅生成当前组选中的线路。保留已发布组的 ID 和存量模型映射，避免旧 Key 失效。
			scoped := *book
			scoped.GroupBindings = scopedBindings
			scoped.Products = nil
			if productIndex >= 0 {
				product := book.Products[productIndex]
				product.ID = retailprice.OfficialProductID(in.Group)
				product.RateSourceChannelID = 0
				product.Routes = slices.DeleteFunc(slices.Clone(product.Routes), func(r retailprice.Route) bool { return !slices.Contains(ids, r.ChannelID) })
				// 换 Key 后只保留这把 Key 当前暴露、且实际有报价的模型，旧商品不能带回已下架模型。
				if in.LibraryID != 0 {
					for index := range product.Routes {
						route := &product.Routes[index]
						route.Models = maps.Clone(route.Models)
						for modelID := range route.Models {
							if !slices.ContainsFunc(supplies, func(source retailprice.CatalogSupply) bool {
								return source.ID == route.ChannelID && slices.Contains(strings.Split(source.Models, ","), modelID)
							}) {
								delete(route.Models, modelID)
							}
						}
					}
				}
				scoped.Products = []retailprice.Product{product}
			}
			draft := retailprice.GenerateOfficialDraft(scoped, supplies, now)
			skipped := map[string]string{}
			for _, e := range draft.Excluded {
				skipped[e.Model] = e.Reason
			}
			book.Models = draft.Book.Models
			var product *retailprice.Product
			if len(draft.Book.Products) != 0 && len(draft.Book.Products[0].Routes) != 0 {
				product = &draft.Book.Products[0]
				product.MarginBPS = in.Margin
				product.RateSourceChannelID = id
				product.RateSourceKeyID = in.LibraryID
				for i := range product.Routes {
					product.Routes[i].RateID = rateIDs[product.Routes[i].ChannelID]
					product.Routes[i].LibraryID = in.routeKeys[product.Routes[i].ChannelID]
				}
				if productIndex >= 0 {
					product.ID = book.Products[productIndex].ID
					book.Products[productIndex] = *product
				} else {
					book.Products = append(book.Products, *product)
				}
			}
			if err := validateRetailGroupRates(tx, book); err != nil {
				return err
			}
			if err := book.Validate(now); err != nil {
				return err
			}
			targets, err := retailCatalogTargetsIn(tx, cfg.Group, ids)
			if err != nil {
				return err
			}
			economics := *book
			if in.Margin != nil {
				economics.MarginBPS = *in.Margin
			}
			rate, err := book.BoundRate(sourceBinding)
			if err != nil {
				return err
			}
			factor, err := economics.SaleFactor(rate.Multiplier)
			if err != nil {
				return err
			}
			out.SaleMultiplier = factor.String()
			eligibleChannels := map[int]bool{}
			count := 0
			for _, name := range slices.Sorted(maps.Keys(names)) {
				row := RetailLaunchRow{Model: name, Reason: skipped[name]}
				if product != nil {
					q, err := book.MakeQuote(in.Version, in.Group, retailprice.KeyPolicy{GroupCurrent: true, ProductID: product.ID}, name, 1, targets.Eligible(*product, name), now)
					if err == nil {
						row.Reason, row.Tiers, row.Channels = "", q.Sale, q.Channels
						// Preview creates a provisional channel ID. Use the same placeholder in the
						// review digest and response because rolled-back sequences can advance.
						for i, channel := range row.Channels {
							eligibleChannels[channel] = true
							if in.Channel == 0 && channel == id {
								row.Channels[i] = 0
							}
							if keyID := in.provisionalKeys[channel]; keyID > 0 {
								row.Channels[i] = -keyID
							}
						}
						count++
					}
				}
				if row.Reason == "" && len(row.Tiers) == 0 {
					row.Reason = "supply_unavailable"
				}
				out.Rows = append(out.Rows, row)
			}
			// Ignore expiry renewals and provisional DB IDs. Any change to the reviewed money, model or exclusion requires a new preview.
			raw, err := common.Marshal([]any{in.Version, in.Site, in.Channel, in.Group, in.Upstream, in.Margin, in.ProductID, in.PricingOnly, in.Routes, sha256.Sum256([]byte(in.Key)), out.SaleMultiplier, out.Rows})
			if in.LibraryID > 0 {
				raw, err = common.Marshal([]any{in.Version, in.LibraryID, in.KeyRoutes, in.Group, in.Margin, in.ProductID, in.PricingOnly, out.SaleMultiplier, out.Rows})
			}
			if err != nil {
				return err
			}
			// 整组能报价不足以证明存在备用线路。每条选中线路都要至少能承接一个模型才能确认发布。
			for _, channel := range ids {
				if !eligibleChannels[channel] {
					if in.Channel == 0 && channel == id {
						channel = 0
					}
					out.UnavailableChannels = append(out.UnavailableChannels, channel)
				}
			}
			if count > 0 && len(out.UnavailableChannels) == 0 {
				out.Review = fmt.Sprintf("%x", sha256.Sum256(raw))
			}
			// Direct publication has the same eligibility gate and version lock as reviewed publication.
			// Return the exclusions without writing even a provisional credential if the gate fails.
			if in.Apply && out.Review != "" {
				return nil
			}
			if in.Review == "" {
				return errLaunchPreview
			}
			if out.Review == "" || in.Review != out.Review {
				return ErrRetailPriceConflict
			}
			return nil
		})
		out.Version = version
		return err
	})
	if errors.Is(err, errLaunchPreview) {
		out.Version = in.Version
		return out, nil
	}
	out.Applied = err == nil && (in.Apply || in.Review != "")
	return out, err
}
