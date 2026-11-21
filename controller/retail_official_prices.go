package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
)

// retailOfficialPricesHandler fetches public tariffs without supply credentials or administrator URLs.
type retailOfficialPricesHandler struct{}

// Type gives the worker a separate lease and an operator-visible run history.
func (retailOfficialPricesHandler) Type() string { return "retail_official_prices" }

// Interval bounds supplier polling independently of the upstream group multiplier job.
func (retailOfficialPricesHandler) Interval() time.Duration { return 6 * time.Hour }

// NewPayload takes no client input. The two supported sources are compile-time constants.
func (retailOfficialPricesHandler) NewPayload() any { return nil }

// Enabled avoids changing an unconfigured or paused retail book.
func (retailOfficialPricesHandler) Enabled() bool {
	if !common.IsRetailSite() {
		return false
	}
	book, _, err := model.GetRetailPriceBook()
	return err == nil && book.Enabled
}

// fetchOfficialPrices rejects redirects and unexpectedly large or unrecognized documents.
// External I/O completes before taking the price-book lock.
func fetchOfficialPrices(ctx context.Context, url string) (map[string][]retailprice.Tier, error) {
	if url != retailprice.OpenAIPriceURL && url != retailprice.ClaudePriceURL {
		return nil, retailprice.ErrConfig
	}
	client := http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("official pricing %s: HTTP %d", url, resp.StatusCode)
	}
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, retailprice.ErrConfig
	}
	prices := retailprice.ParseOfficialPrices(url, string(raw))
	if len(prices) == 0 {
		return nil, fmt.Errorf("no recognized standard prices at %s", url)
	}
	return prices, nil
}

// Run records verified, manual and unmatched IDs. A partial failure can update the
// successful vendor and stays visible as a failed run for the missing source.
func (retailOfficialPricesHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	var result map[string][]string
	err := model.UpdateSystemTaskState(task.TaskID, runnerID, nil)
	if err == nil && common.IsRetailSite() {
		feeds := map[string]map[string][]retailprice.Tier{}
		for _, url := range []string{retailprice.OpenAIPriceURL, retailprice.ClaudePriceURL} {
			prices, readErr := fetchOfficialPrices(ctx, url)
			err = errors.Join(err, readErr)
			if len(prices) > 0 {
				feeds[url] = prices
			}
		}
		_, version, readErr := model.GetRetailPriceBook()
		if readErr == nil && len(feeds) > 0 {
			result, readErr = model.SyncOfficialRetailPrices(ctx, version, feeds, time.Now().Unix())
		}
		err = errors.Join(err, readErr)
	}
	status := model.SystemTaskStatusSucceeded
	if err != nil {
		status = model.SystemTaskStatusFailed
	}
	finishSystemTaskHandler(task, runnerID, status, result, err)
}
