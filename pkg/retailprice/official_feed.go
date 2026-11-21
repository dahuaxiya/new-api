package retailprice

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	OpenAIPriceURL = "https://developers.openai.com/api/docs/pricing.md"
	ClaudePriceURL = "https://platform.claude.com/docs/en/about-claude/pricing.md"
)

const standardHeader = "| Model | Short context input | Short context output | Short context cached input | Short context 30m write | Short context 12h write | Long context input | Long context output | Long context cached input | Long context 30m write | Long context 12h write |"
const imagesHeader = "| Model | Modality | Input | Output | Cached input |"
const claudeHeader = "| Model | Base input tokens | 5m cache writes | 1h cache writes | Cache hits and refreshes | Output tokens |"

var claudeName = regexp.MustCompile(`^Claude (Opus|Sonnet|Fable) ([0-9]+(\.[0-9]+)?)$`)
var claudeUSD = regexp.MustCompile(`^\$([0-9.]+) / MTok(?:<sup>([123])</sup>)?$`)

// officialTable requires one exact standard section and header. Reading stops at the end
// of that table so Batch, Fast or a new adjacent table cannot fill a missing price.
func officialTable(doc, section, header string) [][]string {
	marker := section + "\n" + header + "\n"
	if strings.Count(doc, marker) != 1 {
		return nil
	}
	_, rows, _ := strings.Cut(doc, marker)
	divider, rows, _ := strings.Cut(rows, "\n")
	if strings.Trim(divider, "|:- ") != "" || strings.Count(divider, "|") != strings.Count(header, "|") {
		return nil
	}
	var result [][]string
	for line := range strings.SplitSeq(rows, "\n") {
		if !strings.HasPrefix(line, "|") {
			break
		}
		if !strings.HasSuffix(line, "|") || strings.Count(line, "|") != strings.Count(header, "|") {
			return nil
		}
		cells := strings.Split(line[1:len(line)-1], "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		result = append(result, cells)
	}
	return result
}

// moneyColumns refuses missing prices, rounding and unknown footnotes. A zero rate must
// have an explicit, independently supported billing meaning (e.g. no cache writes on image output).
func moneyColumns(cells []string, claude bool, doc string) []string {
	values := make([]string, 0, len(cells))
	for _, cell := range cells {
		raw, ok := strings.CutPrefix(cell, "$")
		if claude {
			match := claudeUSD.FindStringSubmatch(cell)
			if match == nil {
				return nil
			}
			raw = match[1]
			// A reused footnote number is not evidence that the previous condition still applies.
			switch match[2] {
			case "1":
				ok = strings.Contains(doc, "Cache hits and refreshes on Claude Fable 5.2, Claude Mythos 5.2, Claude Fable 5.1, and Claude Mythos 5.1 are priced at 0.025x the base input price.")
			case "2":
				ok = strings.Contains(doc, "Cache hits and refreshes on Claude Opus 5.5 and Claude Sonnet 5.5 are priced at 0.05x the base input price.")
			case "3":
				ok = strings.Contains(doc, "The $2/$10 per million input/output token pricing for Claude Sonnet 5, announced at launch as introductory pricing through August 31, 2026, is now the standard price.")
			}
		}
		n, err := Amount(raw)
		if !ok || err != nil || !n.IsPositive() {
			return nil
		}
		values = append(values, n.String())
	}
	return values
}

// ParseOfficialPrices reads only standard USD tariffs that match established retail billing
// dimensions. Downstream enrollment independently checks ID, provenance, kind and tier bounds.
func ParseOfficialPrices(url, raw string) map[string][]Tier {
	var lines []string
	for l := range strings.SplitSeq(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		} else if len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "|") {
			// A blank line separates adjacent Markdown tables, even when the next header also starts with a pipe.
			lines = append(lines, "")
		}
	}
	doc := "\n" + strings.Join(lines, "\n") + "\n"
	result := map[string][]Tier{}
	var rows [][]string
	switch url {
	case ClaudePriceURL:
		if !strings.Contains(doc, "All prices are in USD.") || !strings.Contains(doc, "include the full [1M token context window](https://platform.claude.com/docs/en/build-with-claude/context-windows) at standard pricing.") {
			return result
		}
		rows = officialTable(doc, "\n## Model pricing\nThe following table shows pricing for all Claude models:", claudeHeader)
	case OpenAIPriceURL:
		result = parseImagePrices(doc)
		if !strings.Contains(doc, "\nShort context: ≤272K input tokens. Long context: >272K input tokens.\n") {
			return result
		}
		rows = officialTable(doc, "\nFlagship models\nSpeed\nPrices per 1M tokens.\nStandard\n### Standard pricing data", standardHeader)
	default:
		return result
	}
	seen := map[string]bool{}
	for _, row := range rows {
		id := row[0]
		if url == ClaudePriceURL {
			if !claudeName.MatchString(id) {
				continue
			}
			id = strings.ToLower(strings.NewReplacer(" ", "-", ".", "-").Replace(id))
		}
		if seen[id] {
			delete(result, id)
			continue
		}
		seen[id] = true
		a := moneyColumns(row[1:], url == ClaudePriceURL, doc)
		if a == nil {
			continue
		}
		if url == ClaudePriceURL {
			result[id] = []Tier{{1000000, Prices{a[0], a[4], a[3], a[1], a[2]}}}
			continue
		}
		result[id] = []Tier{{272000, pricesFromValues(a[:5])}, {1050000, pricesFromValues(a[5:])}}
	}
	return result
}

// parseImagePrices maps the existing text-prompt image contract only when both modalities
// occur exactly once. Image input, edits and text output need a separate usage contract.
func parseImagePrices(doc string) map[string][]Tier {
	pairs := map[string][][]string{}
	for _, row := range officialTable(doc, "\nMultimodal models\nPrices per 1M tokens unless noted.\nImages\nSpeed\nStandard\n### Grouped Pricing Table data", imagesHeader) {
		pairs[row[0]] = append(pairs[row[0]], row)
	}
	result := map[string][]Tier{}
	for id, pair := range pairs {
		if len(pair) != 2 {
			continue
		}
		im, txt := pair[0], pair[1]
		if im[1] == "Text" {
			im, txt = txt, im
		}
		if im[1] != "Image" || txt[1] != "Text" || txt[3] != "-" {
			continue
		}
		a := moneyColumns([]string{txt[2], im[3], txt[4], im[2], im[4]}, false, doc)
		if a != nil {
			result[id] = []Tier{{32000, Prices{a[0], a[1], a[2], "0", "0"}}}
		}
	}
	return result
}

// RefreshOfficialModel updates a registered model only after a fresh exact match. Never
// extend a promotional tariff beyond its verified deadline or adopt a divergent manual price.
func RefreshOfficialModel(m *Model, url string, tiers []Tier, now int64) bool {
	if m.ID != m.OfficialModel || m.Currency != "USD" || m.PriceMode == "manual" || len(tiers) == 0 || len(m.Tiers) != len(tiers) {
		return false
	}
	if url == ClaudePriceURL {
		if m.Source != "https://claude.com/pricing" && m.Source != url {
			return false
		}
	} else if url != OpenAIPriceURL || (m.Source != url && m.Source != "https://developers.openai.com/api/docs/pricing" && m.Source != "https://developers.openai.com/api/docs/models/"+m.ID) {
		return false
	}
	if (m.Kind == "image") != strings.HasPrefix(m.ID, "gpt-image-") {
		return false
	}
	for i, t := range tiers {
		if t.UpTo != m.Tiers[i].UpTo {
			return false
		}
	}
	equal := slices.Equal(m.Tiers, tiers)
	if m.PriceMode == "" && !equal {
		return false
	}
	end := now + 7*86400
	// A new promotion or a changed deadline needs a reviewed billing contract.
	if m.ID == "gpt-5.6-sol" {
		end = min(end, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC).Unix())
	}
	if end <= now {
		return false
	}
	if m.VerifiedAt > now {
		return false
	}
	if m.PriceMode == "official" && equal && (m.ValidUntil == end || (m.ValidUntil > now+86400 && m.ValidUntil <= end)) {
		return true
	}
	m.PriceMode, m.Tiers, m.VerifiedAt, m.ValidUntil = "official", tiers, now, end
	return true
}
