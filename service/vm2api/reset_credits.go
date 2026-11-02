package vm2api

import (
	"context"
	"encoding/hex"
	"net/http"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ResetCredit 只包含用户可以查看的官方重置次数和期限，不包含上游账号标识。
type ResetCredit struct {
	Label      string `json:"label"`
	StartsAt   string `json:"starts_at"`
	ExpiresAt  string `json:"expires_at"`
	ResetsLeft int    `json:"resets_left"`
	Redeemable bool   `json:"redeemable"`
}

// ResetCreditsResult 是 vm2api 托管协议的白名单结果；不携带官方凭据或原始响应。
type ResetCreditsResult struct {
	Platform       string        `json:"platform,omitempty"`
	AvailableCount int           `json:"available_count"`
	Items          []ResetCredit `json:"items"`
	FetchedAt      string        `json:"fetched_at,omitempty"`
	CooldownUntil  string        `json:"cooldown_until,omitempty"`
	Outcome        string        `json:"outcome,omitempty"`
	Reason         string        `json:"reason,omitempty"`
	BlockedUntil   int64         `json:"blocked_until,omitempty"`
}

// HostedResetCredits 只向预登记实例的托管协议发送固定动作，调用方必须先核验本人绑定并持有名额租约。
// operationID 仅在使用时传入；网络错误不会在此方法中重复发送消费请求。
// @param ctx 控制上游请求的取消和截止时间。
// @param slot 已登记实例内的槽位编号。
// @param bindingID 已验证归属的托管绑定编号。
// @param accountHash 官方账号的服务端摘要。
// @param subjectHash 官方主体的服务端摘要。
// @param platform 当前绑定的平台，只能是 codex 或 claude。
// @param action 只允许 reset-query 或 reset-use。
// @param operationID 消费时的唯一编号，查询时必须为空。
// @param generation 防止过期工作器操作远端槽位的代数。
// @return 受限官方结果；协议或网络异常返回错误，不泄露上游响应正文。
func (c *Client) HostedResetCredits(ctx context.Context, slot, bindingID, accountHash, subjectHash, platform, action, operationID string, generation int64) (ResetCreditsResult, error) {
	var result ResetCreditsResult
	parseID := func(value string) bool {
		id, err := uuid.Parse(value)
		return err == nil && id != uuid.Nil && id.String() == value
	}
	validHash := func(value string) bool {
		decoded, err := hex.DecodeString(value)
		return err == nil && len(value) == 64 && len(decoded) == 32
	}
	if !identifier.MatchString(slot) || !parseID(bindingID) || !validHash(accountHash) || !validHash(subjectHash) || generation < 0 || (platform != "codex" && platform != "claude") || (action != "reset-query" && action != "reset-use") || (action == "reset-use" && !parseID(operationID)) || (action == "reset-query" && operationID != "") {
		return result, &Error{Code: "hosting_conflict", HTTPStatus: 400}
	}
	body := map[string]any{"binding_id": bindingID, "account_hash": accountHash, "subject_hash": subjectHash, "generation": generation, "platform": platform, "operation_id": operationID}
	var out struct {
		Data ResetCreditsResult `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/panel/hosting/slots/"+slot+"/"+action, body, &out); err != nil {
		return result, err
	}
	result = out.Data
	if action == "reset-use" {
		if result.Outcome != "reset" && result.Outcome != "not_used" && result.Outcome != "unknown" {
			return ResetCreditsResult{}, &Error{Code: "invalid_upstream_response", HTTPStatus: 502}
		}
		switch result.Reason {
		case "", "query_unavailable", "not_available", "not_limited", "cooldown", "ineligible":
		default:
			result.Reason = ""
		}
		return result, nil
	}
	if result.Platform != platform || result.AvailableCount < 0 || result.AvailableCount > 10000 || len(result.Items) > 100 || len(result.FetchedAt) > 40 || len(result.CooldownUntil) > 40 {
		return ResetCreditsResult{}, &Error{Code: "invalid_upstream_response", HTTPStatus: 502}
	}
	if result.Items == nil {
		result.Items = []ResetCredit{}
	}
	for _, item := range result.Items {
		if !utf8.ValidString(item.Label) || len(item.Label) > 480 || len(item.StartsAt) > 40 || len(item.ExpiresAt) > 40 || item.ResetsLeft < 0 || item.ResetsLeft > 10000 {
			return ResetCreditsResult{}, &Error{Code: "invalid_upstream_response", HTTPStatus: 502}
		}
	}
	return result, nil
}
