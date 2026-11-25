package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/service"
)

var errRetailModelList = errors.New("retail model list unavailable or incomplete")
var retailModelName = regexp.MustCompile(`^[[:alnum:]_.:/-]{1,100}$`)

// readRetailSiteModelList reads a complete catalog using the protocol, destination and Key verified
// together by the caller. No page can redirect the credential or publish a partial catalog.
func readRetailSiteModelList(ctx context.Context, channelType int, base, key string) ([]string, error) {
	if retailprice.RateFeedOrigin(base) == "" || key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errRetailModelList
	}
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	endpoint, err := url.Parse(base + "/v1/models")
	if err != nil {
		return nil, errRetailModelList
	}
	var headers http.Header
	switch channelType {
	case constant.ChannelTypeOpenAI:
		headers = GetAuthHeader(key)
	case constant.ChannelTypeAnthropic:
		headers = GetClaudeAuthHeader(key)
		endpoint.RawQuery = "limit=100"
	default:
		return nil, errRetailModelList
	}
	headers.Set("Cache-Control", "no-cache")
	client := retailPublicRatesClient
	if base != "https://aixingxing.pro" {
		protected := service.GetSSRFProtectedHTTPClient()
		if protected == nil {
			return nil, errRetailModelList
		}
		client = &http.Client{Transport: protected.Transport, Timeout: 8 * time.Second, CheckRedirect: retailPublicRatesClient.CheckRedirect}
	}
	// One deadline for the entire snapshot, even when the supplier ignores the requested page size.
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var names []string
	seen := map[string]bool{}
	for range 16 {
		ids, cursor, err := readRetailModelPage(ctx, client, endpoint, headers, channelType)
		if err != nil || len(names)+len(ids) > 500 {
			return nil, errRetailModelList
		}
		names = append(names, ids...)
		if cursor == "" {
			slices.Sort(names)
			return slices.Compact(names), nil
		}
		if seen[cursor] || len(names) == 500 {
			return nil, errRetailModelList
		}
		seen[cursor] = true
		// Only the cursor is taken from the response. Keep protocol, URL path and headers pinned.
		query := endpoint.Query()
		query.Set("after_id", cursor)
		endpoint.RawQuery = query.Encode()
	}
	return nil, errRetailModelList
}

// readRetailModelPage closes each bounded response before another request can be issued. Upstream
// errors are intentionally opaque because even an error body can contain credentials.
func readRetailModelPage(ctx context.Context, client *http.Client, endpoint *url.URL, headers http.Header, channelType int) ([]string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, "", errRetailModelList
	}
	req.Header = headers.Clone()
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", errRetailModelList
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Link") != "" {
		return nil, "", errRetailModelList
	}
	const limit = 256 << 10
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(raw) > limit {
		return nil, "", errRetailModelList
	}
	return parseRetailModelPage(raw, channelType, endpoint.Query().Has("after_id"))
}

// parseRetailModelPage distinguishes a complete OpenAI-compatible list from Anthropic cursor pages.
// A bare data array or an unrecognized pagination scheme cannot prove that any model disappeared.
func parseRetailModelPage(raw []byte, channelType int, continuation bool) ([]string, string, error) {
	var list struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
		HasMore    *bool             `json:"has_more"`
		LastID     string            `json:"last_id"`
		Next       string            `json:"next"`
		NextPage   string            `json:"next_page"`
		NextCursor string            `json:"next_cursor"`
		Error      common.RawMessage `json:"error"`
		Success    *bool             `json:"success"`
	}
	if common.Unmarshal(raw, &list) != nil || len(list.Data) == 0 || len(list.Data) > 500 || list.Next != "" || list.NextPage != "" || list.NextCursor != "" || (len(list.Error) > 0 && string(list.Error) != "null") || (list.Success != nil && !*list.Success) {
		return nil, "", errRetailModelList
	}
	cursor := ""
	switch {
	case channelType == constant.ChannelTypeAnthropic && list.Object == "" && list.HasMore != nil:
		if *list.HasMore {
			if list.LastID == "" || list.LastID != list.Data[len(list.Data)-1].ID {
				return nil, "", errRetailModelList
			}
			cursor = list.LastID
		}
	case list.Object == "list" && !continuation && (list.HasMore == nil || !*list.HasMore):
		// Some relays accept Anthropic authentication but return one standard OpenAI list.
	default:
		return nil, "", errRetailModelList
	}
	names := make([]string, 0, len(list.Data))
	for _, item := range list.Data {
		if !retailModelName.MatchString(item.ID) {
			return nil, "", errRetailModelList
		}
		names = append(names, item.ID)
	}
	return names, cursor, nil
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

// Run fetches the bound Key's catalog from its own site and protocol. Paused routes keep their
// preference without contacting a supplier; one failed read cannot stop the other suppliers.
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
			if errors.Is(e, model.ErrRetailModelSourcePaused) {
				continue
			}
			var names []string
			if e == nil {
				names, e = readRetailSiteModelList(ctx, src.Type, src.GetBaseURL(), src.Key)
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
