package model

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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
	Version  int64    `json:"expected_version"`
	Site     int      `json:"site_id"`
	Channel  int      `json:"channel_id"`
	Group    string   `json:"local_group"`
	Upstream string   `json:"upstream_group"`
	Key      string   `json:"key"`
	Models   []string `json:"models"`
	Margin   *int     `json:"margin_bps"`
	Review   string   `json:"review"`
}
type RetailLaunchRow struct {
	Model  string             `json:"model"`
	Tiers  []retailprice.Tier `json:"tiers,omitempty"`
	Reason string             `json:"reason,omitempty"`
}
type RetailLaunchResult struct {
	Rows    []RetailLaunchRow `json:"rows"`
	Review  string            `json:"review"`
	Version int64             `json:"version"`
}

var errLaunchPreview = errors.New("rollback group preview")
var ErrRetailGroupExists = errors.New("This call group is already published")
var ErrRetailLaunchPaused = errors.New("Enable retail sales and automatic upstream rates before publishing a group")

// LaunchRetailGroup runs preview and confirmation against the same locked book. Preview rolls back credential,
// ability and audit writes. Confirmation compares visible tariffs again before allowing the transaction to commit.
func LaunchRetailGroup(ctx context.Context, in RetailGroupLaunch, actor int) (RetailLaunchResult, error) {
	out := RetailLaunchResult{Rows: []RetailLaunchRow{}}
	if actor <= 0 || in.Site <= 0 || in.Channel < 0 || in.Group == "" || len(in.Group) > 100 || len(in.Models) > 200 || (in.Margin != nil && (*in.Margin < 0 || *in.Margin > 9500)) {
		return out, retailprice.ErrConfig
	}
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		version, err := mutateRetailPriceBookIn(tx, ctx, in.Version, actor, false, func(book *retailprice.Book) error {
			if !book.Enabled || !book.PublicRateSync.Enabled {
				return ErrRetailLaunchPaused
			}
			if slices.ContainsFunc(book.Products, func(p retailprice.Product) bool { return p.Name == in.Group || p.BindingGroup == in.Group }) {
				return ErrRetailGroupExists
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
			idx := slices.IndexFunc(channels, func(c RetailBindingChannel) bool {
				return c.ID == id && c.SiteID == in.Site && c.HasKey && c.Status == common.ChannelStatusEnabled
			})
			if idx < 0 {
				return retailprice.ErrAccess
			}
			ch := channels[idx]
			if strings.TrimSpace(ch.Models) == "" {
				return ErrRetailSupplyGroupIncomplete
			}
			groups := book.RegisteredGroups()
			if prev := groups[id]; prev != "" && prev != in.Upstream {
				return retailprice.ErrConfig
			}
			binding := retailprice.GroupBinding{LocalGroup: in.Group, ChannelID: id, UpstreamGroup: in.Upstream}
			if prev := book.Binding(in.Group, id); prev != nil {
				if prev.UpstreamGroup != in.Upstream {
					return retailprice.ErrConfig
				}
				binding = *prev
			} else {
				book.GroupBindings = append(book.GroupBindings, binding)
			}
			groups[id] = in.Upstream
			book.CredentialGroups = groups
			now := time.Now().Unix()
			if _, err := book.BoundRate(binding); err != nil {
				return err
			}
			cfg, err := retail_setting.Load()
			if err != nil {
				return err
			}
			ids, err := retailSupplyIDsIn(tx, cfg.Group)
			if err != nil {
				return err
			}
			if !slices.Contains(ids, id) {
				return retailprice.ErrAccess
			}
			targets, err := retailCatalogTargetsIn(tx, cfg.Group, []int{id})
			if err != nil {
				return err
			}
			// Generation is additive. Restrict its input to this pair so unrelated drafts cannot become public.
			scoped := *book
			scoped.GroupBindings = []retailprice.GroupBinding{binding}
			draft := retailprice.GenerateOfficialDraft(scoped, []retailprice.CatalogSupply{{ID: id, Models: ch.Models, Mapping: ch.ModelMapping, Available: true}}, now)
			skipped := map[string]string{}
			for _, e := range draft.Excluded {
				skipped[e.Model] = e.Reason
			}
			book.Models, book.Products = draft.Book.Models, draft.Book.Products
			var product *retailprice.Product
			for i := range book.Products {
				if book.Products[i].BindingGroup == in.Group {
					product = &book.Products[i]
					product.MarginBPS = in.Margin
					break
				}
			}
			if err := book.Validate(now); err != nil {
				return err
			}
			count := 0
			for _, name := range strings.Split(ch.Models, ",") {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				row := RetailLaunchRow{Model: name, Reason: skipped[name]}
				if row.Reason == "" && product != nil {
					q, err := book.MakeQuote(in.Version, in.Group, retailprice.KeyPolicy{GroupCurrent: true, ProductID: product.ID}, name, 1, targets.Eligible(*product, name), now)
					if err == nil {
						row.Tiers = q.Sale
						count++
					} else {
						row.Reason = "supply_unavailable"
					}
				}
				out.Rows = append(out.Rows, row)
			}
			// Ignore expiry renewals and provisional DB IDs. Any change to the reviewed money, model or exclusion requires a new preview.
			raw, err := common.Marshal([]any{in.Version, in.Site, in.Channel, in.Group, in.Upstream, in.Margin, sha256.Sum256([]byte(in.Key)), out.Rows})
			if err != nil {
				return err
			}
			if count > 0 {
				out.Review = fmt.Sprintf("%x", sha256.Sum256(raw))
			}
			if in.Review == "" {
				return errLaunchPreview
			}
			if count == 0 || in.Review != out.Review {
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
	return out, err
}
