// Package retailprice implements an explicitly CNY-denominated retail contract.
// It never changes the USD semantics of the existing billing expression engine.
package retailprice

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"
)

var (
	ErrConfig     = errors.New("retail_price_configuration_invalid")
	ErrExpired    = errors.New("retail_price_expired")
	ErrAccess     = errors.New("retail_product_not_allowed")
	ErrCap        = errors.New("retail_price_cap_exceeded")
	ErrBudget     = errors.New("retail_budget_exceeded")
	ErrUsage      = errors.New("retail_usage_requires_review")
	amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.[0-9]{1,8})?$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,100}$`)
)

// ConfigRule identifies the rejected price-book rule without retaining tariff values or supplier names.
// Error deliberately preserves the legacy public text; only diagnostic logs should use the rule.
type ConfigRule string

// Error keeps management endpoints from accidentally exposing the internal rule.
func (r ConfigRule) Error() string { return ErrConfig.Error() }

// Unwrap keeps existing configuration error handling compatible with rule-specific failures.
func (r ConfigRule) Unwrap() error { return ErrConfig }

// OutputLimitError 表示报价因请求的输出 token 超过模型上限而拒绝。
// 调用方可保留既有预算错误语义，或按请求协议提示对应字段。
type OutputLimitError struct {
	MaxOutput int
}

// Error 返回预算错误码，供沿用既有错误文案的调用方使用。
func (e *OutputLimitError) Error() string { return ErrBudget.Error() }

// Unwrap 保留既有预算错误分类，供尚未区分具体字段的调用方匹配。
func (e *OutputLimitError) Unwrap() error { return ErrBudget }

// Prices contains all supported text billing categories, in currency / 1M tokens.
// Explicit zero is different from an omitted price; empty values are rejected.
type Prices struct {
	Input        string `json:"input"`
	Output       string `json:"output"`
	CacheRead    string `json:"cache_read"`
	CacheWrite   string `json:"cache_write"`
	CacheWrite1h string `json:"cache_write_1h"`
}

// Tier applies to the whole request based on total input context, including cache.
type Tier struct {
	UpTo   int    `json:"up_to"`
	Prices Prices `json:"prices"`
}

// Model freezes an official tariff and an explicit currency; aliases must be mapped by the operator.
type Model struct {
	PriceMode string `json:"price_mode,omitempty"` // Empty for historical imports, official after verification, manual for overrides.
	// Kind distinguishes text completion from text-only image generation. Empty preserves existing books.
	Kind          string       `json:"kind,omitempty"`
	ID            string       `json:"id"`
	OfficialModel string       `json:"official_model"`
	Currency      string       `json:"currency"`
	Source        string       `json:"source"`
	VerifiedAt    int64        `json:"verified_at"`
	ValidUntil    int64        `json:"valid_until"`
	MaxOutput     int          `json:"max_output"`
	Tiers         []Tier       `json:"tiers"`
	Video         *VideoTariff `json:"video,omitempty"`
}

// VideoTariff 保存按分辨率计价的每秒价格，独立于 Token 阶梯。
type VideoTariff struct {
	MaxDuration int               `json:"max_duration"`
	Prices      map[string]string `json:"prices"`
}

// VideoRequest 是服务端验证并冻结的单次视频规格。
type VideoRequest struct {
	Duration    int    `json:"duration"`
	Resolution  string `json:"resolution"`
	AspectRatio string `json:"aspect_ratio"`
}

// Validate 将视频参数限制在已核实的 Grok 协议范围内，禁止未知规格参与计费。
func (v VideoRequest) Validate() error {
	if v.Duration < 1 || v.Duration > 15 || !slices.Contains([]string{"480p", "720p", "1080p"}, v.Resolution) || !slices.Contains([]string{"16:9", "9:16", "1:1", "4:3", "3:4", "3:2", "2:3"}, v.AspectRatio) {
		return ErrUsage
	}
	return nil
}

// Rate is an account-specific upstream group multiplier, imported independently of official tariffs.
type Rate struct {
	ID         string `json:"id"`
	Multiplier string `json:"multiplier"`
	VerifiedAt int64  `json:"verified_at"`
	ValidUntil int64  `json:"valid_until"`
}

// Route binds one supplier group credential to a channel and an explicit upstream model name.
type Route struct {
	LibraryID int               `json:"library_id,omitempty"` // 业务上选择的库存 Key。ChannelID 供实际分发和历史结算使用。
	ChannelID int               `json:"channel_id"`
	RateID    string            `json:"rate_id"`
	Models    map[string]string `json:"models"`
}

// Product defines independently authorized sales access and equivalent procurement routes.
type Product struct {
	RateSourceKeyID     int      `json:"rate_source_key_id,omitempty"`
	RateSourceChannelID int      `json:"rate_source_channel_id,omitempty"` // 旧组为 0，继续按原有报价规则。
	MarginBPS           *int     `json:"margin_bps,omitempty"`             // Nil products inherit the site margin, including every historical product.
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Public              bool     `json:"public,omitempty"`
	BindingGroup        string   `json:"binding_group,omitempty"`
	UserGroups          []string `json:"user_groups"`
	Routes              []Route  `json:"routes"`
}

// AllowsGroup checks sales access only; procurement uses BindingGroup, not the customer's account group.
// Missing public on historical books retains their explicit account-group restrictions.
func (p Product) AllowsGroup(group string) bool {
	return p.Public || slices.Contains(p.UserGroups, group)
}

// RegisteredGroups returns the upstream identity independently of any customer route.
// Older books carry the same identity on their routes until the next bindings save.
func (b Book) RegisteredGroups() map[int]string {
	groups := make(map[int]string, len(b.CredentialGroups))
	for id, group := range b.CredentialGroups {
		groups[id] = group
	}
	for _, binding := range b.GroupBindings {
		if groups[binding.ChannelID] == "" {
			groups[binding.ChannelID] = binding.UpstreamGroup
		}
	}
	return groups
}

// Book stores an immutable pricing version. Wallet conversion is explicit and never a display FX rate.
type Book struct {
	UnavailableRateChannels map[int]bool `json:"-"` // 请求时按当前供货授权计算，禁止从客户端价表导入。
	// 手工采购价按上游站点和分组登记。线路引用同一费率表项，更新金额即对所有引用生效。
	SiteFeeds         map[int]SiteRateFeed      `json:"site_rate_feeds,omitempty"`
	RateSiteByChannel map[int]int               `json:"rate_site_by_channel,omitempty"`
	SiteGroupRates    map[int]map[string]string `json:"site_group_rates,omitempty"`
	CredentialGroups  map[int]string            `json:"credential_groups,omitempty"`
	ModelSync         map[string]ModelSyncState `json:"model_sync,omitempty"`
	SalePolicyManaged bool                      `json:"sale_policy_managed,omitempty"`
	GroupBindings     []GroupBinding            `json:"group_bindings,omitempty"`
	PublicRateSync    PublicRateSync            `json:"public_rate_sync"`
	Enabled           bool                      `json:"enabled"`
	WalletCNYPerUnit  string                    `json:"wallet_cny_per_unit"`
	ReceiptRatio      string                    `json:"receipt_ratio"`
	MarginBPS         int                       `json:"margin_bps"`
	FeeBPS            int                       `json:"fee_bps"`
	BufferBPS         int                       `json:"buffer_bps"`
	Models            []Model                   `json:"models"`
	Products          []Product                 `json:"products"`
	Rates             []Rate                    `json:"rates"`
}

// KeyPolicy selects a call group; legacy explicit limits remain readable for existing clients.
type KeyPolicy struct {
	// GroupCurrent uses the site's current group prices with no customer-entered
	// price ceilings or budgets. Wallet and token quota enforcement remain mandatory.
	GroupCurrent bool `json:"group_current,omitempty"`
	// StudioCurrent is restricted to session-only studio identities by the model layer.
	// It follows current prices while retaining positive per-request and daily budgets.
	StudioCurrent  bool              `json:"studio_current,omitempty"`
	ProductID      string            `json:"product_id"`
	AcceptFloating bool              `json:"accept_floating"`
	Caps           map[string]Prices `json:"caps"`
	MaxRequestCNY  string            `json:"max_request_cny"`
	DailyBudgetCNY string            `json:"daily_budget_cny"`
}

// Quote freezes customer prices and the wallet conversion before procurement.
type Quote struct {
	Video        *VideoRequest `json:"video,omitempty"`
	VideoUnitCNY string        `json:"video_unit_cny,omitempty"`
	// VideoModel 固定首选线路的上游名称，后台恢复不再读取可能已修改的模型映射。
	VideoModel string `json:"video_model,omitempty"`
	// SaleFactor retains the unrounded group multiplier until optional tool fees are frozen.
	SaleFactor decimal.Decimal `json:"-"`
	// GroupMultiplier is the group rate before margin and fees. Search freezes this at quotation time.
	GroupMultiplier string `json:"group_multiplier,omitempty"`
	// ClientOutputLimit distinguishes an explicit request from a model or budget bound on new quotes.
	ClientOutputLimit   int    `json:"client_output_limit,omitempty"`
	SearchUnitCNY       string `json:"search_unit_cny,omitempty"`
	SearchLimit         int    `json:"search_limit,omitempty"`
	ImageToolUnitCNY    string `json:"image_tool_unit_cny,omitempty"`
	ImageToolLimit      int    `json:"image_tool_limit,omitempty"`
	Version             int64  `json:"version"`
	ProductID           string `json:"product_id"`
	Model               Model  `json:"model"`
	Sale                []Tier `json:"sale"`
	WalletCNYPerUnit    string `json:"wallet_cny_per_unit"`
	ValidUntil          int64  `json:"valid_until"`
	OutputLimit         int    `json:"output_limit"`
	EstimatedTextHold   bool   `json:"estimated_text_hold,omitempty"` // 冻结结算方式，旧账单默认按原预留上限结算。
	ReservedInputTokens int    `json:"reserved_input_tokens,omitempty"`
	ReservedCNY         string `json:"reserved_cny"`
	InputLimit          int    `json:"input_limit,omitempty"`
	Channels            []int  `json:"channels"`
}

// Amount parses bounded decimal money without accepting exponents or silently rounding invalid inputs.
func Amount(raw string) (decimal.Decimal, error) {
	if !amountPattern.MatchString(raw) {
		return decimal.Zero, ErrConfig
	}
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, ErrConfig
	}
	return value, nil
}

// Values returns the stable ordering shared by pricing, comparison, and settlement.
func (p Prices) Values() []string {
	return []string{p.Input, p.Output, p.CacheRead, p.CacheWrite, p.CacheWrite1h}
}

// pricesFromValues preserves category identity after decimal calculations.
func pricesFromValues(v []string) Prices { return Prices{v[0], v[1], v[2], v[3], v[4]} }

// Validate checks complete tariffs before publication; empty disabled configuration is allowed for first setup.
func (b Book) Validate(now int64) error {
	if err := b.ValidateBindings(now); err != nil {
		if errors.Is(err, ErrConfig) {
			return ConfigRule("book_bindings")
		}
		return err
	}
	if !b.Enabled && len(b.Models) == 0 && len(b.Products) == 0 && len(b.Rates) == 0 {
		return nil
	}
	wallet, err := Amount(b.WalletCNYPerUnit)
	if err != nil || !wallet.IsPositive() {
		return ConfigRule("wallet_conversion")
	}
	receipts, err := Amount(b.ReceiptRatio)
	if err != nil || !receipts.IsPositive() || receipts.GreaterThan(decimal.NewFromInt(1)) {
		return ConfigRule("receipt_ratio")
	}
	if b.MarginBPS < 0 || b.MarginBPS > 9500 || b.FeeBPS < 0 || b.FeeBPS > 5000 || b.BufferBPS < 0 || b.BufferBPS > 10000 || len(b.Models) == 0 || len(b.Models) > 500 || len(b.Products) == 0 || len(b.Products) > 100 || len(b.Rates) > 200 {
		return ConfigRule("book_limits")
	}
	models, rates, products := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range b.Models {
		if m.PriceMode != "" && m.PriceMode != "official" && m.PriceMode != "manual" {
			return ConfigRule("model_price_mode")
		}
		if m.Kind != "" && m.Kind != "text" && m.Kind != "image" && m.Kind != "video" {
			return ConfigRule("model_kind")
		}
		u, err := url.Parse(m.Source)
		if !idPattern.MatchString(m.ID) || !idPattern.MatchString(m.OfficialModel) || models[m.ID] || (m.Currency != "USD" && m.Currency != "CNY") || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || m.VerifiedAt <= 0 || m.VerifiedAt > now+30 || m.ValidUntil <= m.VerifiedAt || m.ValidUntil-m.VerifiedAt > 366*86400 {
			return ConfigRule("model_definition")
		}
		models[m.ID] = true
		if m.Kind == "video" {
			if m.Video == nil || m.Video.MaxDuration < 1 || m.Video.MaxDuration > 15 || len(m.Video.Prices) == 0 || len(m.Video.Prices) > 3 || len(m.Tiers) != 0 || m.MaxOutput != 0 {
				return ConfigRule("video_tariff")
			}
			for resolution, raw := range m.Video.Prices {
				price, err := Amount(raw)
				if !slices.Contains([]string{"480p", "720p", "1080p"}, resolution) || err != nil || !price.IsPositive() {
					return ConfigRule("video_price")
				}
			}
			continue
		}
		if m.Video != nil || m.MaxOutput < 1 || m.MaxOutput > 1000000 || len(m.Tiers) == 0 || len(m.Tiers) > 10 {
			return ConfigRule("text_tariff")
		}
		last := 0
		for _, tier := range m.Tiers {
			if tier.UpTo <= last || tier.UpTo > 2000000 {
				return ConfigRule("tier_boundary")
			}
			last = tier.UpTo
			for _, raw := range tier.Prices.Values() {
				if _, err := Amount(raw); err != nil {
					return ConfigRule("tier_price")
				}
			}
		}
	}
	for _, r := range b.Rates {
		value, err := Amount(r.Multiplier)
		// Supplier group names may contain Chinese; they are identifiers, never executable paths.
		if r.ID == "" || strings.TrimSpace(r.ID) != r.ID || len(r.ID) > 200 || strings.ContainsFunc(r.ID, unicode.IsControl) || rates[r.ID] || err != nil || !value.IsPositive() || value.GreaterThan(decimal.NewFromInt(100)) || r.VerifiedAt <= 0 || r.VerifiedAt > now+30 || r.ValidUntil <= r.VerifiedAt || r.ValidUntil-r.VerifiedAt > 7*86400 {
			return ConfigRule("rate_definition")
		}
		rates[r.ID] = true
	}
	for _, p := range b.Products {
		if p.MarginBPS != nil && (*p.MarginBPS < 0 || *p.MarginBPS > 9500) {
			return ConfigRule("product_margin")
		}
		if !idPattern.MatchString(p.ID) || products[p.ID] || strings.TrimSpace(p.Name) == "" || len(p.Name) > 100 || (!p.Public && len(p.UserGroups) == 0) || len(p.UserGroups) > 100 || len(p.Routes) == 0 || len(p.Routes) > 100 {
			return ConfigRule("product_definition")
		}
		// Public access must have an explicit procurement identity. Otherwise a
		// customer's account group could select a different multiplier or a stale manual rate.
		if (p.Public && p.BindingGroup == "") || (p.BindingGroup != "" && !ValidBindingGroup(p.BindingGroup)) {
			return ConfigRule("product_binding_group")
		}
		// 定价来源必须是本组明确绑定的上游分组。来源线路停用时，已核验的倍率仍可用于其他线路。
		if p.RateSourceChannelID != 0 && (!p.Public || b.Binding(p.BindingGroup, p.RateSourceChannelID) == nil) {
			return ConfigRule("product_price_source")
		}
		products[p.ID] = true
		seenChannels := map[int]bool{}
		for _, route := range p.Routes {
			if route.ChannelID <= 0 || seenChannels[route.ChannelID] || (route.RateID != "" && !rates[route.RateID]) || len(route.Models) == 0 || len(route.Models) > 500 {
				return ConfigRule("route_definition")
			}
			seenChannels[route.ChannelID] = true
			managedCost := b.RateSiteByChannel[route.ChannelID] != 0 && b.CredentialGroups[route.ChannelID] != ""
			if route.RateID == "" || (!managedCost && slices.ContainsFunc(b.GroupBindings, func(binding GroupBinding) bool { return binding.ChannelID == route.ChannelID })) {
				groups := p.UserGroups
				if p.BindingGroup != "" {
					groups = []string{p.BindingGroup}
				}
				for _, group := range groups {
					if b.Binding(group, route.ChannelID) == nil {
						return ConfigRule("route_binding")
					}
				}
			}
			for name, upstream := range route.Models {
				if !models[name] || !idPattern.MatchString(upstream) {
					return ConfigRule("route_model")
				}
			}
		}
	}
	return nil
}

// Validate separates simple current-group pricing from legacy explicit spending policies.
// Mixed modes are rejected rather than silently ignoring a caller-supplied limit.
func (p KeyPolicy) Validate() error {
	if p.GroupCurrent {
		if !idPattern.MatchString(p.ProductID) || p.StudioCurrent || p.AcceptFloating || len(p.Caps) != 0 || p.MaxRequestCNY != "" || p.DailyBudgetCNY != "" {
			return ErrConfig
		}
		return nil
	}
	if !idPattern.MatchString(p.ProductID) || !p.AcceptFloating || (!p.StudioCurrent && len(p.Caps) == 0) || len(p.Caps) > 500 {
		return ErrConfig
	}
	for name, cap := range p.Caps {
		if !idPattern.MatchString(name) {
			return ErrConfig
		}
		for _, value := range cap.Values() {
			if _, err := Amount(value); err != nil {
				return err
			}
		}
	}
	for _, raw := range []string{p.MaxRequestCNY, p.DailyBudgetCNY} {
		v, err := Amount(raw)
		if err != nil || !v.IsPositive() {
			return ErrConfig
		}
	}
	return nil
}

// SaleFactor includes receipts, fees and margin after the upstream cost buffer.
// USD numeric-as-CNY and native CNY both use their numeric official coefficients exactly once.
func (b Book) SaleFactor(multiplier string) (decimal.Decimal, error) {
	u, err := Amount(multiplier)
	if err != nil {
		return decimal.Zero, err
	}
	a, err := Amount(b.ReceiptRatio)
	if err != nil {
		return decimal.Zero, err
	}
	denominator := a.Mul(decimal.NewFromInt(int64(10000 - b.FeeBPS))).Mul(decimal.NewFromInt(int64(10000 - b.MarginBPS)))
	if !denominator.IsPositive() {
		return decimal.Zero, ErrConfig
	}
	// Round the factor upward only when division has a remainder. Rounding to nearest
	// can turn a small positive cost into zero before the final tariff is rounded.
	factor, remainder := u.Mul(decimal.NewFromInt(int64(10000+b.BufferBPS))).Mul(decimal.NewFromInt(10000)).QuoRem(denominator, 24)
	if remainder.IsPositive() {
		factor = factor.Add(decimal.New(1, -24))
	}
	return factor, nil
}

// AnchoredSaleFactor 读取商品指定的定价来源，并将该商品的毛利覆盖应用于价表副本。
// 管理列表与实际报价共用此入口。调用方应先核对渠道授权；来源缺失时不能用备用线路猜价。
func (b Book) AnchoredSaleFactor(product Product) (PublicRate, decimal.Decimal, error) {
	binding := b.Binding(product.BindingGroup, product.RateSourceChannelID)
	if product.RateSourceChannelID == 0 || binding == nil {
		return PublicRate{}, decimal.Zero, ErrConfig
	}
	rate, err := b.BoundRate(*binding)
	if err != nil {
		return PublicRate{}, decimal.Zero, err
	}
	if product.MarginBPS != nil {
		b.MarginBPS = *product.MarginBPS
	}
	factor, err := b.SaleFactor(rate.Multiplier)
	return rate, factor, err
}

// MakeQuote freezes the selected group's tariff before routing. Anchored groups
// admit all routes that can serve that tariff. Historical groups keep cheapest-route pricing.
func (b Book) MakeQuote(version int64, group string, key KeyPolicy, modelName string, outputLimit int, eligible []int, now int64, inputTokens ...int) (*Quote, error) {
	return b.makeQuote(version, group, key, modelName, outputLimit, eligible, now, nil, inputTokens...)
}

// MakeVideoQuote 按明确的视频规格报价，复用商品授权与采购选路，拒绝旧 Token 价格上限策略。
func (b Book) MakeVideoQuote(version int64, group string, key KeyPolicy, modelName string, video VideoRequest, eligible []int, now int64) (*Quote, error) {
	if video.Validate() != nil || (!key.StudioCurrent && !key.GroupCurrent) {
		return nil, ErrAccess
	}
	return b.makeQuote(version, group, key, modelName, 0, eligible, now, &video)
}

// MakeCatalogQuote 为服务端目录和上架复核生成展示报价，视频必须逐项复核全部分辨率。
// 此方法使用当前分组价，不代表用户已同意扣费，实际请求仍须调用对应的运行时报价方法。
func (b Book) MakeCatalogQuote(version int64, group string, key KeyPolicy, official Model, eligible []int, now int64) (*Quote, *VideoTariff, error) {
	if official.Kind != "video" || official.Video == nil {
		quote, err := b.MakeQuote(version, group, key, official.ID, 1, eligible, now)
		return quote, nil, err
	}
	// 目录展示只复用商品身份，不能把文字价格上限或 Studio 策略混入分组当前价策略。
	key = KeyPolicy{ProductID: key.ProductID, GroupCurrent: true}
	tariff := &VideoTariff{MaxDuration: official.Video.MaxDuration, Prices: map[string]string{}}
	var quote *Quote
	// 排序保证展示和发布摘要稳定。任一规格失败都拒绝整项预览，避免确认时漏掉价格。
	for _, resolution := range slices.Sorted(maps.Keys(official.Video.Prices)) {
		candidate, err := b.MakeVideoQuote(version, group, key, official.ID, VideoRequest{Duration: 1, Resolution: resolution, AspectRatio: "16:9"}, eligible, now)
		if err != nil {
			return nil, nil, err
		}
		quote = candidate
		tariff.Prices[resolution] = candidate.VideoUnitCNY
	}
	if quote == nil {
		return nil, nil, ErrConfig
	}
	return quote, tariff, nil
}

// makeQuote 在同一套授权与选路结果上冻结各模态价格，避免视频套用 Token 预留逻辑。
func (b Book) makeQuote(version int64, group string, key KeyPolicy, modelName string, outputLimit int, eligible []int, now int64, video *VideoRequest, inputTokens ...int) (*Quote, error) {
	// Persisted state is still an input boundary: corrupted or partially restored
	// catalogs must fail closed before indexing tiers or evaluating a cost floor.
	if err := b.Validate(now); err != nil {
		return nil, err
	}
	if err := key.Validate(); err != nil {
		return nil, ErrAccess
	}
	var product *Product
	var official *Model
	for i := range b.Products {
		if b.Products[i].ID == key.ProductID {
			product = &b.Products[i]
		}
	}
	for i := range b.Models {
		if b.Models[i].ID == modelName {
			official = &b.Models[i]
		}
	}
	if product == nil || official == nil || !product.AllowsGroup(group) {
		return nil, ErrAccess
	}
	if (official.Kind == "video") != (video != nil) {
		return nil, ErrAccess
	}
	if video != nil && (official.Video == nil || video.Duration > official.Video.MaxDuration || official.Video.Prices[video.Resolution] == "") {
		return nil, ErrUsage
	}
	if official.ValidUntil <= now {
		return nil, ErrExpired
	}
	// Image APIs do not accept a completion-token limit. Reserve the operator's
	// verified maximum for one image, never the UI's text completion setting.
	if official.Kind == "image" {
		outputLimit = official.MaxOutput
	}
	cap, ok := key.Caps[modelName]
	if !ok && !key.StudioCurrent && !key.GroupCurrent {
		return nil, ErrAccess
	}
	if video == nil && outputLimit <= 0 {
		return nil, ErrBudget
	}
	if video == nil && outputLimit > official.MaxOutput {
		return nil, &OutputLimitError{MaxOutput: official.MaxOutput}
	}
	var saleFactor decimal.Decimal
	var groupMultiplier string
	expires := official.ValidUntil
	routeFloor := b
	if product.RateSourceChannelID != 0 {
		rate, factor, err := b.AnchoredSaleFactor(*product)
		if err != nil {
			return nil, err
		}
		groupMultiplier = rate.Multiplier // 锚定组按报价来源计费，备用线路不改客户价格。
		saleFactor = factor
		// 备用线路可牺牲目标毛利，但必须覆盖采购、手续费、折扣和成本缓冲。
		routeFloor.MarginBPS = 0
	} else if product.MarginBPS != nil {
		// 旧商品由能承接模型的线路定价，同样遵循该商品的毛利覆盖。
		routeFloor.MarginBPS = *product.MarginBPS
	}
	channels := []int{}
	// Legacy private products used account groups as binding selectors. New call
	// groups pin procurement independently so every customer gets the same group price.
	bindingGroup := group
	if product.BindingGroup != "" {
		bindingGroup = product.BindingGroup
	}
	for _, route := range product.Routes {
		if route.Models[modelName] == "" || b.UnavailableRateChannels[route.ChannelID] || !slices.Contains(eligible, route.ChannelID) {
			continue
		}
		routeRates, rateID := b.Rates, route.RateID
		binding := b.Binding(bindingGroup, route.ChannelID)
		// 已登记归属的历史手工线路与同组的新线路共用采购价。读取快照无需重写原商品。
		if binding == nil && b.RateSiteByChannel[route.ChannelID] != 0 && b.CredentialGroups[route.ChannelID] != "" {
			binding = &GroupBinding{ChannelID: route.ChannelID, UpstreamGroup: b.CredentialGroups[route.ChannelID]}
		}
		if binding != nil {
			// Pauses are scoped to the bound credential and upstream ID; manual aliases survive outages.
			if binding.FollowModels && slices.Contains(b.ModelSync[binding.SyncID()].Paused, route.Models[modelName]) {
				continue
			}
			// A failed refresh cannot reactivate an older manually entered multiplier.
			rate, err := b.BoundRate(*binding)
			if err != nil {
				continue
			}
			// A public multiplier has no independent deadline. The official model price
			// still bounds the resulting quote, just as it does for manual rates.
			routeRates = []Rate{{ID: rate.Group, Multiplier: rate.Multiplier, ValidUntil: official.ValidUntil}}
			rateID = rate.Group
		}
		for _, r := range routeRates {
			if r.ID != rateID || r.ValidUntil <= now {
				continue
			}
			factor, err := routeFloor.SaleFactor(r.Multiplier)
			if err != nil {
				return nil, err
			}
			if product.RateSourceChannelID != 0 {
				if factor.LessThanOrEqual(saleFactor) {
					channels = append(channels, route.ChannelID)
					expires = min(expires, r.ValidUntil)
				}
			} else if len(channels) == 0 || factor.LessThan(saleFactor) {
				groupMultiplier = r.Multiplier
				saleFactor, channels, expires = factor, []int{route.ChannelID}, min(official.ValidUntil, r.ValidUntil)
			} else if factor.Equal(saleFactor) {
				channels = append(channels, route.ChannelID)
				expires = min(expires, r.ValidUntil)
			}
		}
	}
	if len(channels) == 0 {
		return nil, ErrExpired
	}
	quote := &Quote{SaleFactor: saleFactor, GroupMultiplier: groupMultiplier, Version: version, ProductID: product.ID, Model: *official, WalletCNYPerUnit: b.WalletCNYPerUnit, ValidUntil: expires, OutputLimit: outputLimit, Channels: channels}
	if video != nil {
		for _, route := range product.Routes {
			if route.ChannelID == channels[0] {
				quote.VideoModel = route.Models[modelName]
				break
			}
		}
		unit, _ := Amount(official.Video.Prices[video.Resolution])
		unit = unit.Mul(saleFactor).RoundCeil(8)
		reserve := unit.Mul(decimal.NewFromInt(int64(video.Duration))).RoundCeil(8)
		limit, _ := Amount(key.MaxRequestCNY)
		if !key.GroupCurrent && reserve.GreaterThan(limit) {
			return nil, ErrBudget
		}
		quote.Video, quote.VideoUnitCNY, quote.ReservedCNY = video, unit.String(), reserve.String()
		return quote, nil
	}
	maxInput, maxOutput := decimal.Zero, decimal.Zero
	for _, tier := range official.Tiers {
		values := make([]string, 5)
		for i, raw := range tier.Prices.Values() {
			base, _ := Amount(raw)
			price := base.Mul(saleFactor).RoundCeil(8)
			limit, _ := Amount(cap.Values()[i])
			if !key.StudioCurrent && !key.GroupCurrent && price.GreaterThan(limit) {
				return nil, ErrCap
			}
			values[i] = price.String()
			if i == 1 {
				maxOutput = decimal.Max(maxOutput, price)
			} else {
				maxInput = decimal.Max(maxInput, price)
			}
		}
		quote.Sale = append(quote.Sale, Tier{UpTo: tier.UpTo, Prices: pricesFromValues(values)})
	}
	// Runtime callers pass the token count for this request. Preview and legacy callers
	// omit it and retain the model-wide bound. Using the request estimate avoids holding
	// funds for an entire million-token context when the actual prompt is only a few KB.
	// Cache write/read counters can overlap in compatible providers, so retain the
	// conservative four-category input bound at the selected context length.
	maxInputBound := official.Tiers[len(official.Tiers)-1].UpTo
	reservedInputTokens := maxInputBound
	if len(inputTokens) > 0 {
		if inputTokens[0] <= 0 || inputTokens[0] > maxInputBound {
			return nil, ErrBudget
		}
		reservedInputTokens = inputTokens[0]
	}
	inputBound := int64(reservedInputTokens)
	// 分组文本按本次可估输入所在档位暂扣，输出与缓存由上游实际用量结算。
	// 保存模型输出上限用于验证 usage；模型允许的最大输出不等于本次预计费用。
	quote.EstimatedTextHold = key.GroupCurrent && official.Kind != "image"
	if quote.EstimatedTextHold {
		for _, tier := range quote.Sale {
			if reservedInputTokens <= tier.UpTo {
				unit, err := Amount(tier.Prices.Input)
				if err != nil {
					return nil, err
				}
				quote.ReservedInputTokens = reservedInputTokens
				quote.ReservedCNY = unit.Mul(decimal.NewFromInt(inputBound)).Shift(-6).RoundCeil(8).String()
				return quote, nil
			}
		}
		return nil, ErrBudget
	}
	reserve := maxInput.Mul(decimal.NewFromInt(inputBound * 4)).Add(maxOutput.Mul(decimal.NewFromInt(int64(outputLimit)))).Div(decimal.NewFromInt(1000000)).RoundCeil(8)
	limit, _ := Amount(key.MaxRequestCNY)
	if !key.GroupCurrent && reserve.GreaterThan(limit) {
		if !key.StudioCurrent || !maxInput.IsPositive() {
			return nil, ErrBudget
		}
		remaining := limit.Mul(decimal.NewFromInt(1000000)).Sub(maxOutput.Mul(decimal.NewFromInt(int64(outputLimit))))
		if !remaining.IsPositive() {
			return nil, ErrBudget
		}
		// The quotient is bounded by the already validated official context limit,
		// so conversion cannot overflow. Four input categories remain reserved.
		allowed := remaining.Div(maxInput.Mul(decimal.NewFromInt(4))).Floor()
		if allowed.LessThan(decimal.NewFromInt(1)) {
			return nil, ErrBudget
		}
		if allowed.LessThan(decimal.NewFromInt(inputBound)) {
			inputBound = allowed.IntPart()
		}
		quote.InputLimit = int(inputBound)
		reservedInputTokens = quote.InputLimit
		reserve = maxInput.Mul(decimal.NewFromInt(inputBound * 4)).Add(maxOutput.Mul(decimal.NewFromInt(int64(outputLimit)))).Div(decimal.NewFromInt(1000000)).RoundCeil(8)
		if reserve.GreaterThan(limit) {
			return nil, ErrBudget
		}
	}
	quote.ReservedInputTokens = reservedInputTokens
	quote.ReservedCNY = reserve.String()
	return quote, nil
}

// Charge evaluates normalized text/cache counts using the accepted CNY schedule.
// Caller must not feed provider estimates or non-text modalities into this contract.
func (q Quote) Charge(context int, counts [5]int) (decimal.Decimal, error) {
	if q.Video != nil || q.Model.Kind == "video" {
		return decimal.Zero, ErrUsage
	}
	return q.charge(context, counts, 0, 0)
}

// ChargeVideo 只接受上游确认的完成时长，超过预留规格时保留预扣并交人工核对。
func (q Quote) ChargeVideo(seconds int) (decimal.Decimal, error) {
	if q.Video == nil || q.Video.Validate() != nil || q.Model.Kind != "video" || seconds < 1 || seconds > q.Video.Duration {
		return decimal.Zero, ErrUsage
	}
	unit, err := Amount(q.VideoUnitCNY)
	if err != nil || !unit.IsPositive() {
		return decimal.Zero, ErrUsage
	}
	return unit.Mul(decimal.NewFromInt(int64(seconds))).RoundCeil(8), nil
}

// ChargeImage prices image input separately from text using the published GPT Image ratio
// ($8/$5 uncached and $2/$1.25 cached). Apply it to the frozen sale price before rounding the total.
func (q Quote) ChargeImage(context int, counts [5]int, image, cachedImage int) (decimal.Decimal, error) {
	if q.Model.Kind != "image" || image < 0 || cachedImage < 0 || image > context || cachedImage > context-image || counts[0] != context-image-cachedImage-counts[2] || counts[3] != 0 || counts[4] != 0 {
		return decimal.Zero, ErrUsage
	}
	return q.charge(context, counts, image, cachedImage)
}

// charge uses the full input context for tier selection and rounds once after summing all modalities.
func (q Quote) charge(context int, counts [5]int, image, cachedImage int) (decimal.Decimal, error) {
	if context < 0 || (q.InputLimit > 0 && context > q.InputLimit) || len(q.Sale) == 0 || context > q.Sale[len(q.Sale)-1].UpTo || counts[1] > q.OutputLimit {
		return decimal.Zero, ErrUsage
	}
	for i, count := range counts {
		if count < 0 || i != 1 && count > context {
			return decimal.Zero, ErrUsage
		}
	}
	for _, tier := range q.Sale {
		if context > tier.UpTo {
			continue
		}
		total := decimal.Zero
		for i, raw := range tier.Prices.Values() {
			price, err := Amount(raw)
			if err != nil {
				return decimal.Zero, err
			}
			total = total.Add(price.Mul(decimal.NewFromInt(int64(counts[i]))))
			if i == 0 || i == 2 {
				n := image
				if i == 2 {
					n = cachedImage
				}
				total = total.Add(price.Mul(decimal.New(16, -1)).Mul(decimal.NewFromInt(int64(n))))
			}
		}
		return total.Div(decimal.NewFromInt(1000000)).RoundCeil(8), nil
	}
	return decimal.Zero, fmt.Errorf("%w: missing context tier", ErrUsage)
}
