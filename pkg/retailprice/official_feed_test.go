package retailprice

import (
	"github.com/stretchr/testify/require"
	"slices"
	"strings"
	"testing"
)

// TestOfficialStandardFeed fixes the currency, tier ordering and table boundary at the HTTP document boundary.
func TestOfficialStandardFeed(t *testing.T) {
	header := "Flagship models\nSpeed\nPrices per 1M tokens.\nStandard\n### Standard pricing data\n" + standardHeader + "\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n"
	row := "| gpt-6.1-sol | $2 | $10 | $0.1 | $2.5 | $5 | $4 | $15 | $0.2 | $5 | $10 |"
	doc := header + row + "\n\n| Model | Other |\n| --- | --- |\n\nShort context: ≤272K input tokens. Long context: >272K input tokens.\n"
	m := OfficialModels()[0]
	prices := ParseOfficialPrices(OpenAIPriceURL, doc)
	require.Equal(t, m.Tiers, prices[m.ID])
	for _, bad := range []string{strings.Replace(doc, "Standard pricing data", "Batch pricing data", 1), strings.Replace(doc, "Prices per 1M", "Prices per 1K", 1), strings.Replace(doc, "$0.1", "€0.1", 1), strings.Replace(doc, row, row+"\n"+row, 1)} {
		require.Empty(t, ParseOfficialPrices(OpenAIPriceURL, bad))
	}
	now := m.VerifiedAt + 3600
	require.True(t, RefreshOfficialModel(&m, OpenAIPriceURL, prices[m.ID], now))
	old := m
	require.True(t, RefreshOfficialModel(&m, OpenAIPriceURL, prices[m.ID], now+3600))
	require.Equal(t, old, m, "identical observations must not generate an audit version")
	changed := slices.Clone(prices[m.ID])
	changed[0].Prices.Input = "3"
	require.True(t, RefreshOfficialModel(&m, OpenAIPriceURL, changed, now+4000))
	require.Equal(t, "2", old.Tiers[0].Prices.Input, "accepted snapshots keep their prices")
	m.PriceMode = "manual"
	require.False(t, RefreshOfficialModel(&m, OpenAIPriceURL, prices[m.ID], now+5000))
	m.PriceMode = ""
	require.False(t, RefreshOfficialModel(&m, OpenAIPriceURL, prices[m.ID], now+5000))
	require.False(t, RefreshOfficialModel(&old, OpenAIPriceURL, nil, now+5000))
}

// TestOfficialVendorDimensions verifies Claude cache durations and image modality pairing.
// A missing footnote or duplicated modality must never renew an old price.
func TestOfficialVendorDimensions(t *testing.T) {
	note := "Cache hits and refreshes on Claude Opus 5.5 and Claude Sonnet 5.5 are priced at 0.05x the base input price."
	doc := "All prices are in USD.\n## Model pricing\nThe following table shows pricing for all Claude models:\n" + claudeHeader + "\n| :---- | :---- | :---- | :---- | :---- | :---- |\n| Claude Opus 5.5 | $4 / MTok | $5 / MTok | $8 / MTok | $0.2 / MTok<sup>2</sup> | $20 / MTok |\n\n" + note + "\ninclude the full [1M token context window](https://platform.claude.com/docs/en/build-with-claude/context-windows) at standard pricing."
	require.Equal(t, []Tier{{1000000, Prices{"4", "20", "0.2", "5", "8"}}}, ParseOfficialPrices(ClaudePriceURL, doc)["claude-opus-5-5"])
	require.Empty(t, ParseOfficialPrices(ClaudePriceURL, strings.Replace(doc, note, "new condition", 1)))
	doc = "Multimodal models\nPrices per 1M tokens unless noted.\nImages\nSpeed\nStandard\n### Grouped Pricing Table data\n" + imagesHeader + "\n| --- | --- | --- | --- | --- |\n| gpt-image-2 | Image | $8 | $30 | $2 |\n| gpt-image-2 | Text | $5 | - | $1.25 |"
	require.Equal(t, []Tier{{32000, Prices{"5", "30", "1.25", "0", "0"}}}, ParseOfficialPrices(OpenAIPriceURL, doc)["gpt-image-2"])
	require.Empty(t, ParseOfficialPrices(OpenAIPriceURL, strings.Replace(doc, "| Text |", "| Image |", 1)))
}
