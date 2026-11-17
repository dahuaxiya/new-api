package retailprice_test

import (
	"os"
	"slices"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/stretchr/testify/require"
)

// fixture loads synthetic tariffs, not a claim about any provider's official prices.
func fixture(t *testing.T) (retailprice.Book, retailprice.KeyPolicy) {
	t.Helper()
	raw, err := os.ReadFile("testdata/book.json")
	require.NoError(t, err)
	var book retailprice.Book
	require.NoError(t, common.Unmarshal(raw, &book))
	cap := retailprice.Prices{Input: "100", Output: "100", CacheRead: "100", CacheWrite: "100", CacheWrite1h: "100"}
	return book, retailprice.KeyPolicy{ProductID: "standard", AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-4o": cap}, MaxRequestCNY: "10", DailyBudgetCNY: "10"}
}

// TestCurrencyAndProfitFloor verifies native CNY and numeric USD have no additional FX conversion.
func TestCurrencyAndProfitFloor(t *testing.T) {
	for _, currency := range []string{"USD", "CNY"} {
		t.Run(currency, func(t *testing.T) {
			book, key := fixture(t)
			book.Models[0].Currency = currency
			book.Rates[0].ID = "ChatGPT-稳定"
			book.Products[0].Routes[0].RateID = book.Rates[0].ID
			quote, err := book.MakeQuote(1, "default", key, "gpt-4o", 20, []int{101}, 2)
			require.NoError(t, err)
			require.Equal(t, "3", quote.Sale[0].Prices.Input)
			require.Equal(t, "6", quote.Sale[0].Prices.Output)
			charge, err := quote.Charge(10, [5]int{10, 2, 0, 0, 0})
			require.NoError(t, err)
			require.Equal(t, "0.000042", charge.String())
			book.ReceiptRatio, book.FeeBPS, book.BufferBPS = "0.8", 500, 1000
			quote, err = book.MakeQuote(2, "default", key, "gpt-4o", 20, []int{101}, 2)
			require.NoError(t, err)
			require.Equal(t, "4.34210527", quote.Sale[0].Prices.Input)
		})
	}
}

// TestQuoteFailsClosed checks prices, permissions, validity, and bounded usage before billing.
func TestQuoteFailsClosed(t *testing.T) {
	book, key := fixture(t)
	quote, err := book.MakeQuote(1, "default", key, "gpt-4o", 20, []int{101}, 2)
	require.NoError(t, err)
	key.Caps["gpt-4o"] = quote.Sale[0].Prices
	book.Rates[0].Multiplier = "0.4"
	_, err = book.MakeQuote(2, "default", key, "gpt-4o", 20, []int{101}, 2)
	require.ErrorIs(t, err, retailprice.ErrCap)
	charge, err := quote.Charge(10, [5]int{10, 2, 0, 0, 0})
	require.NoError(t, err)
	require.Equal(t, "0.000042", charge.String(), "accepted quote stays frozen")
	_, err = book.MakeQuote(2, "unauthorized", key, "gpt-4o", 20, []int{101}, 2)
	require.ErrorIs(t, err, retailprice.ErrAccess)
	_, err = book.MakeQuote(2, "default", key, "gpt-4o", 20, []int{101}, 3601)
	require.ErrorIs(t, err, retailprice.ErrExpired)
	for _, counts := range [][5]int{{-1, 2, 0, 0, 0}, {10, 21, 0, 0, 0}, {10, 2, 11, 0, 0}} {
		_, err = quote.Charge(10, counts)
		require.ErrorIs(t, err, retailprice.ErrUsage)
	}
	book.Models[0].Tiers = nil
	require.NotPanics(t, func() {
		_, err = book.MakeQuote(2, "default", key, "gpt-4o", 20, []int{101}, 2)
		require.ErrorIs(t, err, retailprice.ErrConfig)
	})
}

// TestContextTiersAndRouteFloor ensures a more expensive fallback is never bought under a cheap quote.
func TestContextTiersAndRouteFloor(t *testing.T) {
	book, key := fixture(t)
	high := book.Models[0].Tiers[0]
	high.UpTo, high.Prices.Input = 2000, "30"
	book.Models[0].Tiers = append(book.Models[0].Tiers, high)
	book.Rates = append(book.Rates, retailprice.Rate{ID: "expensive", Multiplier: "0.5", VerifiedAt: 1, ValidUntil: 3601})
	book.Products[0].Routes = append(book.Products[0].Routes, retailprice.Route{ChannelID: 102, RateID: "expensive", Models: map[string]string{"gpt-4o": "gpt-4o"}})
	quote, err := book.MakeQuote(1, "default", key, "gpt-4o", 20, []int{101, 102}, 2)
	require.NoError(t, err)
	require.Equal(t, []int{101}, quote.Channels)
	amount, err := quote.Charge(1500, [5]int{1000, 2, 500, 0, 0})
	require.NoError(t, err)
	require.Equal(t, "0.009162", amount.String())
	key.Caps["gpt-4o"] = quote.Sale[0].Prices
	_, err = book.MakeQuote(1, "default", key, "gpt-4o", 20, []int{101}, 2)
	require.ErrorIs(t, err, retailprice.ErrCap, "all reachable context tiers must fit the cap")
}

// TestOfficialDraftUsesSavedBindings protects access, overrides and fixed verification expiry.
func TestOfficialDraftUsesSavedBindings(t *testing.T) {
	now := int64(1794916800) // 2026-11-17, within the checked catalog's validity.
	book := retailprice.Book{WalletCNYPerUnit: "1", ReceiptRatio: "1", MarginBPS: 2000,
		GroupBindings: []retailprice.GroupBinding{{LocalGroup: "gpt-normal", ChannelID: 101, UpstreamGroup: "test-group"}},
		PublicRateSync: retailprice.PublicRateSync{Enabled: true, Snapshot: &retailprice.PublicRateSnapshot{
			Source: "https://aixingxing.pro/api/public/rates", UpdatedAt: now, FetchedAt: now,
			Groups: []retailprice.PublicRate{{Group: "test-group", Name: "Test", Multiplier: "0.12"}}}}}
	supplies := []retailprice.CatalogSupply{
		{ID: 101, Models: "gpt-6.1-sol,gpt-image-2.6-sunburst,unknown-alias", Available: true},
		{ID: 102, Models: "gpt-6-astra", Available: true},
	}
	draft := retailprice.GenerateOfficialDraft(book, supplies, now)
	require.NoError(t, draft.Book.Validate(now))
	require.False(t, draft.Book.Enabled, "generation never opens sales")
	require.Empty(t, book.Models, "generation cannot mutate the source book")
	require.Len(t, draft.Book.Models, 2)
	require.Len(t, draft.Book.Products, 1)
	require.True(t, draft.Book.Products[0].Public)
	require.Equal(t, "gpt-normal", draft.Book.Products[0].BindingGroup)
	require.Empty(t, draft.Book.Products[0].UserGroups)
	require.Len(t, draft.Book.Products[0].Routes, 1, "unbound supply never enters a product")
	require.Equal(t, 101, draft.Book.Products[0].Routes[0].ChannelID)
	require.Equal(t, 2000, draft.Book.MarginBPS)
	require.Len(t, draft.Excluded, 1)
	require.Equal(t, "unknown-alias", draft.Excluded[0].Model)
	cap := retailprice.Prices{Input: "100", Output: "100", CacheRead: "100", CacheWrite: "100", CacheWrite1h: "100"}
	policy := retailprice.KeyPolicy{ProductID: draft.Book.Products[0].ID, AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-6.1-sol": cap}, MaxRequestCNY: "999", DailyBudgetCNY: "999"}
	quote, err := draft.Book.MakeQuote(1, "gpt-normal", policy, "gpt-6.1-sol", 1024, []int{101}, now)
	require.NoError(t, err)
	require.Equal(t, "0.3", quote.Sale[0].Prices.Input, "upstream 0.12 and 20% margin yield 0.15 sale factor")
	require.Equal(t, "0.6", quote.Sale[1].Prices.Input, "long context cannot use the short tier")
	defaultQuote, err := draft.Book.MakeQuote(1, "default", policy, "gpt-6.1-sol", 1024, []int{101}, now)
	require.NoError(t, err)
	require.Equal(t, quote.Sale, defaultQuote.Sale, "all registered users receive the call group price")
	legacy := draft.Book
	legacy.Products = slices.Clone(draft.Book.Products)
	legacy.Products[0].Public, legacy.Products[0].BindingGroup, legacy.Products[0].UserGroups = false, "", []string{"gpt-normal"}
	_, err = legacy.MakeQuote(1, "default", policy, "gpt-6.1-sol", 1024, []int{101}, now)
	require.ErrorIs(t, err, retailprice.ErrAccess, "reading a legacy book cannot expand access")
	upgraded := retailprice.GenerateOfficialDraft(legacy, supplies, now)
	require.True(t, upgraded.Book.Products[0].Public)
	require.False(t, legacy.Products[0].Public, "draft conversion never modifies the published book")
	legacy.Products[0].UserGroups = []string{"private"}
	require.False(t, retailprice.GenerateOfficialDraft(legacy, supplies, now).Book.Products[0].Public, "custom restrictions survive generation")
	draft.Book.Models[0].Tiers[0].Prices.Input = "3"
	repeated := retailprice.GenerateOfficialDraft(draft.Book, supplies, now+60)
	require.Equal(t, draft.Book, repeated.Book, "operator overrides and existing products survive repeated generation")
	expired := retailprice.GenerateOfficialDraft(book, supplies, now+40*86400)
	require.Empty(t, expired.Book.Models, "generation does not renew the fixed verification date")
	supplies[0].Available = false
	require.Empty(t, retailprice.GenerateOfficialDraft(book, supplies, now).Book.Products, "disabled supply is not offered")
}

// TestPublicCallGroupsPinProcurement prevents account membership from changing a public group's cost.
func TestPublicCallGroupsPinProcurement(t *testing.T) {
	now := int64(1794916800)
	book := retailprice.Book{WalletCNYPerUnit: "1", ReceiptRatio: "1", MarginBPS: 2000,
		GroupBindings:  []retailprice.GroupBinding{{LocalGroup: "normal", ChannelID: 101, UpstreamGroup: "normal"}, {LocalGroup: "premium", ChannelID: 102, UpstreamGroup: "premium"}},
		PublicRateSync: retailprice.PublicRateSync{Enabled: true, Snapshot: &retailprice.PublicRateSnapshot{Source: "https://aixingxing.pro/api/public/rates", UpdatedAt: now, FetchedAt: now, Groups: []retailprice.PublicRate{{Group: "normal", Multiplier: "0.12"}, {Group: "premium", Multiplier: "0.24"}}}}}
	draft := retailprice.GenerateOfficialDraft(book, []retailprice.CatalogSupply{{ID: 101, Models: "gpt-6.1-sol", Available: true}, {ID: 102, Models: "gpt-6.1-sol", Available: true}}, now)
	require.NoError(t, draft.Book.Validate(now))
	cap := retailprice.Prices{Input: "100", Output: "100", CacheRead: "100", CacheWrite: "100", CacheWrite1h: "100"}
	for _, p := range draft.Book.Products {
		policy := retailprice.KeyPolicy{ProductID: p.ID, AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-6.1-sol": cap}, MaxRequestCNY: "999", DailyBudgetCNY: "999"}
		for _, account := range []string{"default", "normal", "premium"} {
			q, err := draft.Book.MakeQuote(1, account, policy, "gpt-6.1-sol", 1024, []int{101, 102}, now)
			require.NoError(t, err)
			if p.BindingGroup == "normal" {
				require.Equal(t, "0.3", q.Sale[0].Prices.Input)
				require.Equal(t, []int{101}, q.Channels)
			} else {
				require.Equal(t, "0.6", q.Sale[0].Prices.Input)
				require.Equal(t, []int{102}, q.Channels)
			}
		}
		failed := draft.Book
		failed.PublicRateSync.LastError = "failed"
		_, err := failed.MakeQuote(1, "default", policy, "gpt-6.1-sol", 1024, []int{101, 102}, now)
		require.Error(t, err, "failed synchronization must not fall back to account pricing")
		_, err = draft.Book.MakeQuote(1, "default", policy, "gpt-6.1-sol", 1024, []int{101, 102}, now+301)
		require.Error(t, err, "stale feed cannot admit requests")
	}
	invalid := draft.Book
	invalid.Products = slices.Clone(draft.Book.Products)
	invalid.Products[0].BindingGroup = ""
	require.ErrorIs(t, invalid.Validate(now), retailprice.ErrConfig)
}

// TestStudioCurrentBudgets bounds automatic pricing without requiring model-by-model ceilings.
func TestStudioCurrentBudgets(t *testing.T) {
	book, policy := fixture(t)
	policy.StudioCurrent, policy.Caps = true, nil
	policy.MaxRequestCNY, policy.DailyBudgetCNY = "0.01", "50"
	quote, err := book.MakeQuote(1, "default", policy, "gpt-4o", 20, []int{101}, 2)
	require.NoError(t, err)
	require.Equal(t, 411, quote.InputLimit)
	require.Equal(t, "0.009984", quote.ReservedCNY)
	charge, err := quote.Charge(411, [5]int{411, 20, 411, 411, 411})
	require.NoError(t, err)
	reserve, err := retailprice.Amount(quote.ReservedCNY)
	require.NoError(t, err)
	require.True(t, charge.LessThanOrEqual(reserve))
	_, err = quote.Charge(412, [5]int{412, 20, 0, 0, 0})
	require.ErrorIs(t, err, retailprice.ErrUsage)
	book.Rates[0].Multiplier = "0.48"
	increased, err := book.MakeQuote(2, "default", policy, "gpt-4o", 20, []int{101}, 2)
	require.NoError(t, err)
	require.Less(t, increased.InputLimit, quote.InputLimit)
	require.Equal(t, "6", increased.Sale[0].Prices.Input)
	_, err = book.MakeQuote(2, "unauthorized", policy, "gpt-4o", 20, []int{101}, 2)
	require.ErrorIs(t, err, retailprice.ErrAccess)
	_, err = book.MakeQuote(2, "default", policy, "gpt-4o", 20, []int{101}, 3601)
	require.ErrorIs(t, err, retailprice.ErrExpired)
	// Output alone cannot fit: do not silently lower the requested output limit.
	policy.MaxRequestCNY = "0.00001"
	_, err = book.MakeQuote(2, "default", policy, "gpt-4o", 20, []int{101}, 2)
	require.ErrorIs(t, err, retailprice.ErrBudget)
	for _, invalid := range []string{"", "0", "-1", "NaN"} {
		policy.MaxRequestCNY = invalid
		require.Error(t, policy.Validate())
	}
	policy.MaxRequestCNY, policy.StudioCurrent = "5", false
	require.Error(t, policy.Validate(), "ordinary keys still require model ceilings")
}
