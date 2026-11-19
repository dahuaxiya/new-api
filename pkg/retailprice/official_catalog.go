package retailprice

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// CatalogSupply is a credential-free snapshot of an explicitly enrolled supply channel.
type CatalogSupply struct {
	ID        int
	Models    string
	Available bool
}

// CatalogExclusion identifies a configured model that could not safely enter a generated route.
type CatalogExclusion struct {
	ChannelID int    `json:"channel_id"`
	Model     string `json:"model"`
	Reason    string `json:"reason"`
}

// OfficialDraft contains an unpublished, additive catalog; generating it never opens sales or grants consent.
type OfficialDraft struct {
	Book     Book               `json:"book"`
	Excluded []CatalogExclusion `json:"excluded"`
}

// OfficialModels returns the Standard API tariff snapshot checked on 2026-11-17.
// Verification dates are fixed: clicking Generate must never renew an unverified price.
// Only exact official IDs are included; supplier-specific aliases require an operator mapping.
func OfficialModels() []Model {
	verified := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC).Unix()
	expires := time.Date(2026, 12, 17, 0, 0, 0, 0, time.UTC).Unix()
	models := []Model{}
	// Source: https://developers.openai.com/api/docs/pricing and each exact model page.
	// Model pages confirm the >272K full-request input/cache ×2 and output ×1.5 tiers.
	for _, row := range []struct{ id, input, output, read, write, longInput, longOutput, longRead, longWrite string }{
		{"gpt-6.1-sol", "2", "10", "0.1", "2.5", "4", "15", "0.2", "5"},
		{"gpt-6-sol", "2", "10", "0.2", "2.5", "4", "15", "0.4", "5"},
		{"gpt-6-astra", "10", "50", "1", "12.5", "20", "75", "2", "25"},
		{"gpt-5.6-sol", "4", "20", "0.4", "5", "8", "30", "0.8", "10"},
		{"gpt-5.6-terra", "2", "12", "0.2", "2.5", "4", "18", "0.4", "5"},
	} {
		end := expires
		if row.id == "gpt-5.6-sol" {
			// The published promotional price ends Nov 30; stop conservatively at the start of that day.
			end = time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC).Unix()
		}
		write, _ := Amount(row.write)
		longWrite, _ := Amount(row.longWrite)
		models = append(models, Model{ID: row.id, Kind: "text", OfficialModel: row.id, Currency: "USD",
			Source: "https://developers.openai.com/api/docs/models/" + row.id, VerifiedAt: verified, ValidUntil: end,
			// These are site operating limits, below the official 128K output maximum.
			MaxOutput: 32768, Tiers: []Tier{
				{UpTo: 272000, Prices: Prices{row.input, row.output, row.read, row.write, write.Add(write).String()}},
				{UpTo: 1050000, Prices: Prices{row.longInput, row.longOutput, row.longRead, row.longWrite, longWrite.Add(longWrite).String()}},
			}})
	}
	// The retail image contract allows text prompts only. Image input/edit tariffs
	// are deliberately excluded; ambiguous usage remains reserved for manual review.
	for _, id := range []string{"gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-2.6-sunburst", "gpt-image-2.6-flare"} {
		models = append(models, Model{ID: id, Kind: "image", OfficialModel: id, Currency: "USD",
			Source: "https://developers.openai.com/api/docs/pricing", VerifiedAt: verified, ValidUntil: expires,
			MaxOutput: 32768, Tiers: []Tier{{UpTo: 32000, Prices: Prices{"5", "30", "1.25", "0", "0"}}}})
	}
	return models
}

// GenerateOfficialDraft adds exact official models and one public call group per saved supply binding group.
// An explicit generate-and-publish action upgrades legacy generated groups; custom access rules remain intact.
// Saved tariffs take precedence over the bundled snapshot. Only exact supported models may gain a new route.
// Bindings and supplies must be server-owned snapshots; this function performs no network or database writes.
func GenerateOfficialDraft(book Book, supplies []CatalogSupply, now int64) OfficialDraft {
	book.Models, book.Products = slices.Clone(book.Models), slices.Clone(book.Products)
	result := OfficialDraft{Book: book, Excluded: []CatalogExclusion{}}
	known := map[string]Model{}
	for _, model := range OfficialModels() {
		known[model.ID] = model
	}
	existing := map[string]bool{}
	for _, model := range book.Models {
		// Use the same authoritative tariff as quotation and model discovery. Falling back
		// to a bundled price here could route a model whose saved tariff already expired.
		known[model.ID] = model
		existing[model.ID] = true
	}
	products := map[string]Product{}
	order := []string{}
	for _, binding := range book.GroupBindings {
		channel := slices.IndexFunc(supplies, func(s CatalogSupply) bool { return s.ID == binding.ChannelID })
		if channel < 0 || !supplies[channel].Available {
			result.Excluded = append(result.Excluded, CatalogExclusion{ChannelID: binding.ChannelID, Reason: "supply_unavailable"})
			continue
		}
		routes := map[string]string{}
		for name := range strings.SplitSeq(supplies[channel].Models, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			official, ok := known[name]
			if !ok || official.OfficialModel != name || (official.Kind != "text" && official.Kind != "image") || official.VerifiedAt > now || official.ValidUntil <= now {
				result.Excluded = append(result.Excluded, CatalogExclusion{binding.ChannelID, name, "official_price_missing_or_expired"})
				continue
			}
			if !existing[name] {
				result.Book.Models = append(result.Book.Models, official)
				existing[name] = true
			}
			routes[name] = name
		}
		if len(routes) == 0 {
			continue
		}
		// Hash only names for stable, bounded IDs. Never derive an executable path from a group label.
		id := officialProductID(binding.LocalGroup)
		product, ok := products[id]
		if !ok {
			product = Product{ID: id, Name: binding.LocalGroup, Public: true, BindingGroup: binding.LocalGroup, UserGroups: []string{}}
			if previous := slices.IndexFunc(book.Products, func(p Product) bool { return p.ID == id }); previous >= 0 {
				product = book.Products[previous]
				// Only the exact historical generated shape can be upgraded by this
				// explicit draft action. Reading a book never expands its access.
				if product.BindingGroup == "" && !product.Public && len(product.UserGroups) == 1 && product.UserGroups[0] == binding.LocalGroup {
					product.Public, product.BindingGroup, product.UserGroups = true, binding.LocalGroup, []string{}
				}
				if product.BindingGroup != binding.LocalGroup {
					result.Excluded = append(result.Excluded, CatalogExclusion{ChannelID: binding.ChannelID, Reason: "product_access_conflict"})
					continue
				}
				product.Routes = slices.Clone(product.Routes)
			}
			order = append(order, id)
		}
		if route := slices.IndexFunc(product.Routes, func(r Route) bool { return r.ChannelID == binding.ChannelID }); route >= 0 {
			// Preserve explicit operator model mappings while filling missing exact IDs.
			product.Routes[route].Models = maps.Clone(product.Routes[route].Models)
			for name, upstream := range routes {
				if _, exists := product.Routes[route].Models[name]; !exists {
					product.Routes[route].Models[name] = upstream
				}
			}
		} else {
			product.Routes = append(product.Routes, Route{ChannelID: binding.ChannelID, Models: routes})
		}
		products[id] = product
	}
	for _, id := range order {
		if previous := slices.IndexFunc(result.Book.Products, func(p Product) bool { return p.ID == id }); previous >= 0 {
			result.Book.Products[previous] = products[id]
		} else {
			result.Book.Products = append(result.Book.Products, products[id])
		}
	}
	// One credential can serve several local groups; show each excluded model once.
	seen := map[CatalogExclusion]bool{}
	unique := []CatalogExclusion{}
	for _, entry := range result.Excluded {
		if !seen[entry] {
			unique = append(unique, entry)
			seen[entry] = true
		}
	}
	result.Excluded = unique
	return result
}

// officialProductID keeps the public call group stable across manual publication and discovery.
func officialProductID(group string) string {
	sum := sha256.Sum256([]byte(group))
	return fmt.Sprintf("official-%x", sum[:8])
}
