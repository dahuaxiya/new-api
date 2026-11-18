package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
)

var errRetailModelList = errors.New("retail model list unavailable or incomplete")
var retailModelName = regexp.MustCompile(`^[[:alnum:]_.:/-]{1,100}$`)

// readRetailModelList sends only the enrolled group's bearer token to the fixed model-list endpoint.
// The shared client rejects redirects. Never include response bodies or request headers in task errors.
func readRetailModelList(ctx context.Context, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://aixingxing.pro/v1/models", nil)
	if err != nil {
		return nil, errRetailModelList
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := retailPublicRatesClient.Do(req)
	if err != nil {
		return nil, errRetailModelList
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Link") != "" {
		return nil, errRetailModelList
	}
	const limit = 256 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return nil, errRetailModelList
	}
	return parseRetailModelList(raw)
}

// parseRetailModelList rejects incomplete snapshots as a whole; partial pages cannot prove removal.
func parseRetailModelList(raw []byte) ([]string, error) {
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
		HasMore    bool              `json:"has_more"`
		Next       string            `json:"next"`
		NextPage   string            `json:"next_page"`
		NextCursor string            `json:"next_cursor"`
		Error      common.RawMessage `json:"error"`
		Success    *bool             `json:"success"`
	}
	if common.Unmarshal(raw, &list) != nil || list.Object != "list" || len(list.Data) == 0 || len(list.Data) > 500 || list.HasMore || list.Next != "" || list.NextPage != "" || list.NextCursor != "" || (len(list.Error) > 0 && string(list.Error) != "null") || (list.Success != nil && !*list.Success) {
		return nil, errRetailModelList
	}
	names := make([]string, 0, len(list.Data))
	for _, item := range list.Data {
		if !retailModelName.MatchString(item.ID) {
			return nil, errRetailModelList
		}
		names = append(names, item.ID)
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

type retailModelsHandler struct{}

// Type identifies the database lease shared by all retail instances.
func (retailModelsHandler) Type() string { return "retail_group_models" }

// Enabled requires a published active book and explicit per-binding consent.
func (retailModelsHandler) Enabled() bool {
	if !common.IsRetailSite() {
		return false
	}
	b, _, err := model.GetRetailPriceBook()
	return err == nil && b.Enabled && slices.ContainsFunc(b.GroupBindings, func(g retailprice.GroupBinding) bool { return g.FollowModels })
}

// Interval spaces independent observations used to confirm removals.
func (retailModelsHandler) Interval() time.Duration { return 10 * time.Minute }

// NewPayload accepts no caller-supplied destinations or credentials.
func (retailModelsHandler) NewPayload() any { return nil }

// Run fetches once per credential and lets other suppliers progress during an isolated outage.
func (h retailModelsHandler) Run(ctx context.Context, task *model.SystemTask, runner string) {
	b, _, err := model.GetRetailPriceBook()
	seen := map[int]bool{}
	failed := 0
	if err == nil && common.IsRetailSite() && b.Enabled {
		for _, binding := range b.GroupBindings {
			id := binding.ChannelID
			if !binding.FollowModels || seen[id] {
				continue
			}
			seen[id] = true
			if err = model.UpdateSystemTaskState(task.TaskID, runner, nil); err != nil {
				break
			}
			latest, v, e := model.GetRetailPriceBook()
			if e != nil {
				err = e
				break
			}
			if !latest.Enabled || !slices.ContainsFunc(latest.GroupBindings, func(g retailprice.GroupBinding) bool { return g.ChannelID == id && g.FollowModels }) {
				continue
			}
			src, e := model.RetailModelListSource(model.DB.WithContext(ctx), id)
			var names []string
			if e == nil {
				names, e = readRetailModelList(ctx, src.Key)
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}
			if changed, writeErr := model.ApplyRetailModelList(ctx, v, id, src, names); writeErr != nil || e != nil {
				failed++
			} else if changed {
				model.InitChannelCache()
			}
		}
	}
	status := model.SystemTaskStatusSucceeded
	if err != nil || failed != 0 {
		status = model.SystemTaskStatusFailed
		err = errRetailModelList
	}
	finishSystemTaskHandler(task, runner, status, map[string]int{"failed": failed}, err)
}
