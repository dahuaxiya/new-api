package controller

import (
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
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

type retailPublicRate struct {
	Group      string `json:"group"`
	Name       string `json:"name"`
	Multiplier string `json:"multiplier"`
}

// GetRetailPublicRates reads a fixed, credential-free supplier snapshot for root preview only.
// It deliberately never publishes rates, guesses channel bindings or changes wallet conversion.
func GetRetailPublicRates(c *gin.Context) {
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, retailPublicRatesURL, nil)
	if err != nil {
		common.ApiError(c, common.NewMessage("Unable to read upstream public multipliers"))
		return
	}
	// Do not forward the operator's cookies, authorization headers, URL parameters or upstream Key.
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	response, err := retailPublicRatesClient.Do(req)
	if err != nil {
		common.ApiError(c, common.NewMessage("Unable to read upstream public multipliers"))
		return
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" {
		// Never relay an upstream error body: it may contain HTML, diagnostics or misleading prices.
		common.ApiError(c, common.NewMessage("Unable to read upstream public multipliers"))
		return
	}
	const maxBytes = 256 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		common.ApiError(c, common.NewMessage("Invalid upstream public multiplier response"))
		return
	}
	fetchedAt := time.Now().UTC()
	groups, updatedAt, ok := parseRetailPublicRates(body, fetchedAt)
	if !ok {
		common.ApiError(c, common.NewMessage("Invalid upstream public multiplier response"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"source": retailPublicRatesURL, "updated_at": updatedAt, "fetched_at": fetchedAt.Unix(), "groups": groups,
	}})
}

// parseRetailPublicRates validates the entire snapshot before displaying any group.
// Source update time is informational: its freshness semantics are undocumented, so it must not
// become a verified_at/valid_until pair for billing. No partial or zero-price fallback is allowed.
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
