package retailprice

import (
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// SalePolicy contains resale economics without granting access or converting wallet units.
type SalePolicy struct {
	ReceiptRatio string `json:"receipt_ratio"`
	MarginBPS    int    `json:"margin_bps"`
	FeeBPS       int    `json:"fee_bps"`
	BufferBPS    int    `json:"buffer_bps"`
}

// Validate bounds economics even before the first model catalog is published.
func (p SalePolicy) Validate() error {
	receipts, err := Amount(p.ReceiptRatio)
	if err != nil || !receipts.IsPositive() || receipts.GreaterThan(decimal.NewFromInt(1)) || p.MarginBPS < 0 || p.MarginBPS > 9500 || p.FeeBPS < 0 || p.FeeBPS > 5000 || p.BufferBPS < 0 || p.BufferBPS > 10000 {
		return ErrConfig
	}
	return nil
}

// GroupBinding associates a customer group and supplier credential with an explicit public cost group.
// A channel represents one upstream Key, so all its bindings must use the same upstream group.
type GroupBinding struct {
	LocalGroup    string `json:"local_group"`
	ChannelID     int    `json:"channel_id"`
	UpstreamGroup string `json:"upstream_group"`
	FollowModels  bool   `json:"follow_models,omitempty"`
}

// PublicRate retains exact decimal values without inferring wallet conversion or account discounts.
type PublicRate struct {
	Group      string `json:"group"`
	Name       string `json:"name"`
	Multiplier string `json:"multiplier"`
}

// PublicRateSnapshot records both source publication and retrieval times for auditing.
type PublicRateSnapshot struct {
	Source    string       `json:"source"`
	UpdatedAt int64        `json:"updated_at"`
	FetchedAt int64        `json:"fetched_at"`
	Groups    []PublicRate `json:"groups"`
}

// PublicRateSync is committed with the price book, keeping bindings and costs atomic at admission.
type PublicRateSync struct {
	Enabled   bool                `json:"enabled"`
	LastError string              `json:"last_error,omitempty"`
	Snapshot  *PublicRateSnapshot `json:"snapshot,omitempty"`
}

// SiteRateFeed 保存一个上游的完整报价。关闭 Follow 后，已核实的快照仍可用于报价。
type SiteRateFeed struct {
	URL       string              `json:"url"`
	Follow    bool                `json:"follow"`
	LastError string              `json:"last_error,omitempty"`
	Snapshot  *PublicRateSnapshot `json:"snapshot"`
}

// RateFeedOrigin 限制公开倍率地址为无凭据的 HTTPS 端点，供管理端与抓取端核对站点身份。
func RateFeedOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ""
	}
	return "https://" + strings.ToLower(u.Host)
}

// ValidateBindings rejects ambiguous credential assignments even in an otherwise empty disabled book.
func (b Book) ValidateBindings(now int64) error {
	if len(b.GroupBindings) > 200 || len(b.PublicRateSync.LastError) > 200 {
		return ErrConfig
	}
	seen := map[GroupBinding]bool{}
	channelGroups := map[int]string{}
	for _, binding := range b.GroupBindings {
		if !ValidBindingGroup(binding.LocalGroup) || !ValidBindingGroup(binding.UpstreamGroup) || binding.ChannelID <= 0 || seen[binding] {
			return ErrConfig
		}
		for previous := range seen {
			if previous.LocalGroup == binding.LocalGroup && previous.ChannelID == binding.ChannelID {
				return ErrConfig
			}
		}
		if previous, ok := channelGroups[binding.ChannelID]; ok && previous != binding.UpstreamGroup {
			return ErrConfig
		}
		seen[binding], channelGroups[binding.ChannelID] = true, binding.UpstreamGroup
	}
	if b.PublicRateSync.Enabled && len(b.GroupBindings) == 0 {
		return ErrConfig
	}
	if len(b.SiteFeeds) > 100 || len(b.RateSiteByChannel) > 200 {
		return ErrConfig
	}
	for site, feed := range b.SiteFeeds {
		if site <= 0 || RateFeedOrigin(feed.URL) == "" || feed.Snapshot == nil || len(feed.LastError) > 200 {
			return ErrConfig
		}
		if err := validatePublicSnapshot(feed.Snapshot, feed.URL, now); err != nil {
			return err
		}
	}
	for channel, site := range b.RateSiteByChannel {
		if channel <= 0 || b.SiteFeeds[site].URL == "" {
			return ErrConfig
		}
	}
	return validatePublicSnapshot(b.PublicRateSync.Snapshot, "https://aixingxing.pro/api/public/rates", now)
}

// validatePublicSnapshot 校验单一来源的完整报价，拒绝重复分组及不精确的金额。
func validatePublicSnapshot(snapshot *PublicRateSnapshot, source string, now int64) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.Source != source || snapshot.UpdatedAt <= 0 || snapshot.FetchedAt <= 0 || snapshot.UpdatedAt > now+30 || snapshot.FetchedAt > now+30 || len(snapshot.Groups) == 0 || len(snapshot.Groups) > 200 {
		return ErrConfig
	}
	groups := map[string]bool{}
	for _, rate := range snapshot.Groups {
		value, err := Amount(rate.Multiplier)
		if !ValidBindingGroup(rate.Group) || groups[rate.Group] || err != nil || !value.IsPositive() || value.GreaterThan(decimal.NewFromInt(100)) {
			return ErrConfig
		}
		groups[rate.Group] = true
	}
	return nil
}

// ValidBindingGroup treats names as data and excludes ambiguous whitespace and control characters.
func ValidBindingGroup(group string) bool {
	return group != "" && len(group) <= 128 && utf8.ValidString(group) && strings.TrimSpace(group) == group && !strings.ContainsFunc(group, unicode.IsControl)
}

// Binding finds an exact customer-group/channel pair; names alone cannot authorize another credential.
func (b Book) Binding(group string, channelID int) *GroupBinding {
	for i := range b.GroupBindings {
		if b.GroupBindings[i].LocalGroup == group && b.GroupBindings[i].ChannelID == channelID {
			return &b.GroupBindings[i]
		}
	}
	return nil
}

// BoundRate returns the bound multiplier from the latest successful sync. Publication and
// retrieval times are audit fields; an unchanged upstream rate remains quotable.
func (b Book) BoundRate(binding GroupBinding) (PublicRate, error) {
	if b.UnavailableRateChannels[binding.ChannelID] {
		return PublicRate{}, ErrExpired
	}
	sync := b.PublicRateSync
	if site := b.RateSiteByChannel[binding.ChannelID]; site != 0 {
		// 同名分组必须按凭据所属站点取价。手动读取的快照也有效，刷新失败时绝不跨站回退。
		feed := b.SiteFeeds[site]
		sync = PublicRateSync{Enabled: true, Snapshot: feed.Snapshot, LastError: feed.LastError}
	}
	if !sync.Enabled || sync.LastError != "" || sync.Snapshot == nil {
		return PublicRate{}, ErrExpired
	}
	snapshot := sync.Snapshot
	index := slices.IndexFunc(snapshot.Groups, func(rate PublicRate) bool { return rate.Group == binding.UpstreamGroup })
	if index < 0 {
		return PublicRate{}, ErrExpired
	}
	return snapshot.Groups[index], nil
}
