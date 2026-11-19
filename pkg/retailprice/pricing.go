// Package retailprice implements an explicitly CNY-denominated retail contract.
// It never changes the USD semantics of the existing billing expression engine.
package retailprice

import (
	"errors"
	"fmt"
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
	// Kind distinguishes text completion from text-only image generation. Empty preserves existing books.
	Kind          string `json:"kind,omitempty"`
	ID            string `json:"id"`
	OfficialModel string `json:"official_model"`
	Currency      string `json:"currency"`
	Source        string `json:"source"`
	VerifiedAt    int64  `json:"verified_at"`
	ValidUntil    int64  `json:"valid_until"`
	MaxOutput     int    `json:"max_output"`
	Tiers         []Tier `json:"tiers"`
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
	ChannelID int               `json:"channel_id"`
	RateID    string            `json:"rate_id"`
	Models    map[string]string `json:"models"`
}

// Product defines independently authorized sales access and equivalent procurement routes.
type Product struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Public       bool     `json:"public,omitempty"`
	BindingGroup string   `json:"binding_group,omitempty"`
	UserGroups   []string `json:"user_groups"`
	Routes       []Route  `json:"routes"`
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
	Version             int64  `json:"version"`
	ProductID           string `json:"product_id"`
	Model               Model  `json:"model"`
	Sale                []Tier `json:"sale"`
	WalletCNYPerUnit    string `json:"wallet_cny_per_unit"`
	ValidUntil          int64  `json:"valid_until"`
	OutputLimit         int    `json:"output_limit"`
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
		return err
	}
	if !b.Enabled && len(b.Models) == 0 && len(b.Products) == 0 && len(b.Rates) == 0 {
		return nil
	}
	wallet, err := Amount(b.WalletCNYPerUnit)
	if err != nil || !wallet.IsPositive() {
		return ErrConfig
	}
	receipts, err := Amount(b.ReceiptRatio)
	if err != nil || !receipts.IsPositive() || receipts.GreaterThan(decimal.NewFromInt(1)) {
		return ErrConfig
	}
	if b.MarginBPS < 0 || b.MarginBPS > 9500 || b.FeeBPS < 0 || b.FeeBPS > 5000 || b.BufferBPS < 0 || b.BufferBPS > 10000 || len(b.Models) == 0 || len(b.Models) > 500 || len(b.Products) == 0 || len(b.Products) > 100 || len(b.Rates) > 200 {
		return ErrConfig
	}
	models, rates, products := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range b.Models {
		if m.Kind != "" && m.Kind != "text" && m.Kind != "image" {
			return ErrConfig
		}
		u, err := url.Parse(m.Source)
		if !idPattern.MatchString(m.ID) || !idPattern.MatchString(m.OfficialModel) || models[m.ID] || (m.Currency != "USD" && m.Currency != "CNY") || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || m.VerifiedAt <= 0 || m.VerifiedAt > now+30 || m.ValidUntil <= m.VerifiedAt || m.ValidUntil-m.VerifiedAt > 366*86400 || m.MaxOutput < 1 || m.MaxOutput > 1000000 || len(m.Tiers) == 0 || len(m.Tiers) > 10 {
			return ErrConfig
		}
		models[m.ID] = true
		last := 0
		for _, tier := range m.Tiers {
			if tier.UpTo <= last || tier.UpTo > 2000000 {
				return ErrConfig
			}
			last = tier.UpTo
			for _, raw := range tier.Prices.Values() {
				if _, err := Amount(raw); err != nil {
					return err
				}
			}
		}
	}
	for _, r := range b.Rates {
		value, err := Amount(r.Multiplier)
		// Supplier group names may contain Chinese; they are identifiers, never executable paths.
		if r.ID == "" || strings.TrimSpace(r.ID) != r.ID || len(r.ID) > 200 || strings.ContainsFunc(r.ID, unicode.IsControl) || rates[r.ID] || err != nil || !value.IsPositive() || value.GreaterThan(decimal.NewFromInt(100)) || r.VerifiedAt <= 0 || r.VerifiedAt > now+30 || r.ValidUntil <= r.VerifiedAt || r.ValidUntil-r.VerifiedAt > 7*86400 {
			return ErrConfig
		}
		rates[r.ID] = true
	}
	for _, p := range b.Products {
		if !idPattern.MatchString(p.ID) || products[p.ID] || strings.TrimSpace(p.Name) == "" || len(p.Name) > 100 || (!p.Public && len(p.UserGroups) == 0) || len(p.UserGroups) > 100 || len(p.Routes) == 0 || len(p.Routes) > 100 {
			return ErrConfig
		}
		// Public access must have an explicit procurement identity. Otherwise a
		// customer's account group could select a different multiplier or a stale manual rate.
		if (p.Public && p.BindingGroup == "") || (p.BindingGroup != "" && !validBindingGroup(p.BindingGroup)) {
			return ErrConfig
		}
		products[p.ID] = true
		seenChannels := map[int]bool{}
		for _, route := range p.Routes {
			if route.ChannelID <= 0 || seenChannels[route.ChannelID] || (route.RateID != "" && !rates[route.RateID]) || len(route.Models) == 0 || len(route.Models) > 500 {
				return ErrConfig
			}
			seenChannels[route.ChannelID] = true
			if route.RateID == "" || slices.ContainsFunc(b.GroupBindings, func(binding GroupBinding) bool { return binding.ChannelID == route.ChannelID }) {
				groups := p.UserGroups
				if p.BindingGroup != "" {
					groups = []string{p.BindingGroup}
				}
				for _, group := range groups {
					if b.Binding(group, route.ChannelID) == nil {
						return ErrConfig
					}
				}
			}
			for name, upstream := range route.Models {
				if !models[name] || !idPattern.MatchString(upstream) {
					return ErrConfig
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

// MakeQuote uses the cheapest eligible group and freezes its entire sales schedule.
// More expensive fallbacks are not admitted under an already accepted cheaper quote.
func (b Book) MakeQuote(version int64, group string, key KeyPolicy, modelName string, outputLimit int, eligible []int, now int64, inputTokens ...int) (*Quote, error) {
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
	if outputLimit <= 0 || outputLimit > official.MaxOutput {
		return nil, ErrBudget
	}
	var cheapest decimal.Decimal
	expires := official.ValidUntil
	channels := []int{}
	// Legacy private products used account groups as binding selectors. New call
	// groups pin procurement independently so every customer gets the same group price.
	bindingGroup := group
	if product.BindingGroup != "" {
		bindingGroup = product.BindingGroup
	}
	for _, route := range product.Routes {
		if route.Models[modelName] == "" || !slices.Contains(eligible, route.ChannelID) {
			continue
		}
		routeRates, rateID := b.Rates, route.RateID
		if binding := b.Binding(bindingGroup, route.ChannelID); binding != nil {
			// Pauses are scoped to the bound credential and upstream ID; manual aliases survive outages.
			if binding.FollowModels && slices.Contains(b.ModelSync[binding.SyncID()].Paused, route.Models[modelName]) {
				continue
			}
			// A failed refresh cannot reactivate an older manually entered multiplier.
			rate, err := b.BoundRate(*binding, now)
			if err != nil {
				continue
			}
			routeRates, rateID = []Rate{rate}, rate.ID
		}
		for _, r := range routeRates {
			if r.ID != rateID || r.ValidUntil <= now {
				continue
			}
			factor, err := b.SaleFactor(r.Multiplier)
			if err != nil {
				return nil, err
			}
			if len(channels) == 0 || factor.LessThan(cheapest) {
				cheapest, channels, expires = factor, []int{route.ChannelID}, min(official.ValidUntil, r.ValidUntil)
			} else if factor.Equal(cheapest) {
				channels = append(channels, route.ChannelID)
				expires = min(expires, r.ValidUntil)
			}
		}
	}
	if len(channels) == 0 {
		return nil, ErrExpired
	}
	quote := &Quote{Version: version, ProductID: product.ID, Model: *official, WalletCNYPerUnit: b.WalletCNYPerUnit, ValidUntil: expires, OutputLimit: outputLimit, Channels: channels}
	maxInput, maxOutput := decimal.Zero, decimal.Zero
	for _, tier := range official.Tiers {
		values := make([]string, 5)
		for i, raw := range tier.Prices.Values() {
			base, _ := Amount(raw)
			price := base.Mul(cheapest).RoundCeil(8)
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
		}
		return total.Div(decimal.NewFromInt(1000000)).RoundCeil(8), nil
	}
	return decimal.Zero, fmt.Errorf("%w: missing context tier", ErrUsage)
}
