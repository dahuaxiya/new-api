package retailprice_test

import (
	"os"
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
