package controller

import (
	"context"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const retailPublicRatesURL = "https://aixingxing.pro/api/public/rates"

var retailPublicRatesClient = &http.Client{
	Timeout: 8 * time.Second,
	// Only this public endpoint is trusted. Never follow a redirect to another host or internal service.
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}
var retailPublicRateNumber = regexp.MustCompile(`^(0|[1-9][0-9]{0,2})(\.[0-9]{1,8})?$`)

type retailPublicRate = retailprice.PublicRate

// GetRetailPublicRates reads a fixed credential-free source without changing a binding or price.
func GetRetailPublicRates(c *gin.Context) {
	snapshot, err := fetchRetailPublicRates(c.Request.Context())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": snapshot})
}

// fetchRetailPublicRates is shared by manual preview and the leased synchronizer.
// It never forwards operator headers or follows redirects to another endpoint.
func fetchRetailPublicRates(ctx context.Context) (*retailprice.PublicRateSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, retailPublicRatesURL, nil)
	if err != nil {
		return nil, common.NewMessage("Unable to read upstream public multipliers")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	response, err := retailPublicRatesClient.Do(req)
	if err != nil {
		return nil, common.NewMessage("Unable to read upstream public multipliers")
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" {
		return nil, common.NewMessage("Unable to read upstream public multipliers")
	}
	const maxBytes = 256 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		return nil, common.NewMessage("Invalid upstream public multiplier response")
	}
	fetchedAt := time.Now().UTC()
	groups, updatedAt, ok := parseRetailPublicRates(body, fetchedAt)
	if !ok {
		return nil, common.NewMessage("Invalid upstream public multiplier response")
	}
	// Stable order makes unchanged-price renewals independent of the supplier's display ordering.
	slices.SortFunc(groups, func(a, b retailPublicRate) int { return strings.Compare(a.Group, b.Group) })
	return &retailprice.PublicRateSnapshot{Source: retailPublicRatesURL, UpdatedAt: updatedAt, FetchedAt: fetchedAt.Unix(), Groups: groups}, nil
}

// parseRetailPublicRates validates the entire snapshot before displaying any group.
// Preview accepts historical snapshots for inspection. Billing separately requires an explicit binding
// and both timestamps within its five-minute validity window; no partial or zero-price fallback is allowed.
func parseRetailPublicRates(body []byte, fetchedAt time.Time) ([]retailPublicRate, int64, bool) {
	var source struct {
		Success   bool   `json:"success"`
		Server    string `json:"server"`
		UpdatedAt string `json:"updated_at"`
		Groups    []struct {
			Group string            `json:"group"`
			Name  string            `json:"name"`
			Rate  common.RawMessage `json:"rate"`
		} `json:"groups"`
	}
	if common.Unmarshal(body, &source) != nil || !source.Success || source.Server != "https://aixingxing.pro" || len(source.Groups) == 0 || len(source.Groups) > 200 {
		return nil, 0, false
	}
	updatedAt, err := time.Parse(time.RFC3339, source.UpdatedAt)
	if err != nil || updatedAt.Unix() <= 0 || updatedAt.After(fetchedAt.Add(30*time.Second)) {
		return nil, 0, false
	}
	groups := make([]retailPublicRate, 0, len(source.Groups))
	seen := make(map[string]bool, len(source.Groups))
	for _, group := range source.Groups {
		if group.Group == "" || group.Name == "" || len(group.Group) > 128 || len(group.Name) > 256 || !utf8.ValidString(group.Group+group.Name) || strings.TrimSpace(group.Group) != group.Group || strings.ContainsFunc(group.Group+group.Name, unicode.IsControl) || seen[group.Group] {
			return nil, 0, false
		}
		// Parse the JSON number as a decimal string; float64 rounding must never alter a supplier multiplier.
		raw := strings.TrimSpace(string(group.Rate))
		if !retailPublicRateNumber.MatchString(raw) {
			return nil, 0, false
		}
		rate, err := decimal.NewFromString(raw)
		if err != nil || !rate.IsPositive() || rate.GreaterThan(decimal.NewFromInt(100)) {
			return nil, 0, false
		}
		seen[group.Group] = true
		groups = append(groups, retailPublicRate{Group: group.Group, Name: group.Name, Multiplier: rate.String()})
	}
	return groups, updatedAt.Unix(), true
}
