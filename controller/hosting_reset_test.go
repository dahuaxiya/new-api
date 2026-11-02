package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestHostingResetBoundary 用真实会话和一次性证明验证归属、跨站拒绝、状态限制与响应丢失保护。
func TestHostingResetBoundary(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	oldCookieSecure := common.SessionCookieSecure
	common.SessionCookieSecure = true
	t.Cleanup(func() { common.SessionCookieSecure = oldCookieSecure })
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.HostingResetAttempt{}))
	now := time.Now().Unix()
	seat, bindingID, inventory := uuid.NewString(), uuid.NewString(), uuid.NewString()
	account, subject := strings.Repeat("a", 64), strings.Repeat("b", 64)
	require.NoError(t, model.DB.Create(&model.GPTHostingGrant{SeatID: seat, UserID: user.Id, StartsAt: now - 1, EndsAt: -1, Enabled: true, Group: "acct_reset"}).Error)
	require.NoError(t, model.DB.Create(&model.GPTHostingBinding{SeatID: seat, UserID: user.Id, BindingID: bindingID, InventoryID: &inventory, InstanceID: "fixture", SlotID: "slot-a", Platform: "codex", State: "active", AccountHash: &account, SubjectHash: &subject}).Error)
	foreignSeat := uuid.NewString()
	require.NoError(t, model.DB.Create(&model.GPTHostingGrant{SeatID: foreignSeat, UserID: user.Id + 1, StartsAt: now - 1, EndsAt: -1, Enabled: true, Group: "acct_foreign"}).Error)
	require.NoError(t, model.DB.Create(&model.GPTHostingBinding{SeatID: foreignSeat, UserID: user.Id + 1, BindingID: uuid.NewString(), State: "active"}).Error)
	var uses atomic.Int32
	var unknown atomic.Bool
	var queryFailure atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/panel/login" {
			fmt.Fprint(w, `{"ok":true,"token":"fixture","role":"admin","expires_in":3600}`)
			return
		}
		var body struct {
			Platform string `json:"platform"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.HasSuffix(r.URL.Path, "/reset-use") {
			uses.Add(1)
			if unknown.Load() {
				fmt.Fprint(w, `{"ok":true,"data":{"outcome":"unknown"}}`)
			} else {
				fmt.Fprint(w, `{"ok":true,"data":{"outcome":"reset"}}`)
			}
			return
		}
		if queryFailure.Load() != 0 {
			w.WriteHeader(http.StatusConflict)
			code := "reset_unsupported"
			if queryFailure.Load() == 2 {
				code = "reset_unavailable"
			}
			fmt.Fprintf(w, `{"ok":false,"error":{"code":%q}}`, code)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"data":{"platform":%q,"available_count":1,"items":[]}}`, body.Platform)
	}))
	defer remote.Close()
	registry, err := vm2api.New(vm2api.Config{Instances: []vm2api.InstanceConfig{{ID: "fixture", Name: "fixture", BaseURL: remote.URL, Username: "fixture", Password: "fixture", Models: []string{"model"}}}})
	require.NoError(t, err)
	oldRegistry := onboardingRegistry
	onboardingRegistry = func() (*vm2api.Registry, error) { return registry, nil }
	t.Cleanup(func() { onboardingRegistry = oldRegistry })
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	r := gin.New()
	r.POST("/query", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), QueryHostingResetCredits)
	r.POST("/use", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), UseHostingResetCredit)
	request := func(path, auth, origin, proof string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://portal.test"+path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if proof != "" {
			req.Header.Set("X-Security-Proof", proof)
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	newProof := func(action, target string) string {
		operation := service.VerificationOperation{Scope: service.VerificationScopeGPTHosting, Context: []byte(fmt.Sprintf(`{"action":%q,"binding_id":%q}`, action, target))}
		bound, bindErr := service.BindVerificationOperation(operation)
		require.NoError(t, bindErr)
		proof, proofErr := service.CompleteSecurityVerification(identity, bound, service.VerificationMethodPassword)
		require.NoError(t, proofErr)
		return proof.ProofToken
	}
	query, use := "/query?seat_id="+seat, "/use?seat_id="+seat
	require.Equal(t, 401, request(query, "", "", "").Code)
	require.NotEqual(t, 200, request("/query?seat_id="+foreignSeat, token, "http://portal.test", "").Code)
	require.Equal(t, 403, request(query, token, "https://evil.test", "").Code)
	require.NotEqual(t, 200, request(use, token, "http://portal.test", "").Code)
	require.NotEqual(t, 200, request(use, token, "http://portal.test", newProof("login", bindingID)).Code)
	require.EqualValues(t, 0, uses.Load())
	for _, platform := range []string{"codex", "claude"} {
		require.NoError(t, model.DB.Model(&model.GPTHostingBinding{}).Where("seat_id = ?", seat).Update("platform", platform).Error)
		response := request(query, token, "http://portal.test", "")
		require.Equal(t, 200, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), `"platform":"`+platform+`"`)
		require.NotContains(t, response.Body.String(), account)
	}
	queryFailure.Store(1)
	require.Contains(t, request(query, token, "http://portal.test", "").Body.String(), `"reset_unsupported"`)
	queryFailure.Store(2)
	require.Contains(t, request(query, token, "http://portal.test", "").Body.String(), `"reset_unavailable"`)
	queryFailure.Store(0)
	proof := newProof("reset", bindingID)
	require.Equal(t, 200, request(use, token, "http://portal.test", proof).Code)
	require.NotEqual(t, 200, request(use, token, "http://portal.test", proof).Code)
	require.EqualValues(t, 1, uses.Load())
	require.NoError(t, model.DB.Model(&model.GPTHostingBinding{}).Where("seat_id = ?", seat).Update("paused", true).Error)
	require.NotEqual(t, 200, request(query, token, "http://portal.test", "").Code)
	require.NoError(t, model.DB.Model(&model.GPTHostingBinding{}).Where("seat_id = ?", seat).Updates(map[string]any{"paused": false, "migration_state": "running"}).Error)
	require.NotEqual(t, 200, request(query, token, "http://portal.test", "").Code)
	require.NoError(t, model.DB.Model(&model.GPTHostingBinding{}).Where("seat_id = ?", seat).Update("migration_state", "").Error)
	require.NoError(t, model.DB.Model(&model.GPTHostingGrant{}).Where("seat_id = ?", seat).Update("ends_at", now-1).Error)
	require.NotEqual(t, 200, request(query, token, "http://portal.test", "").Code)
	require.NoError(t, model.DB.Model(&model.GPTHostingGrant{}).Where("seat_id = ?", seat).Update("ends_at", -1).Error)
	unknown.Store(true)
	response := request(use, token, "http://portal.test", newProof("reset", bindingID))
	require.Contains(t, response.Body.String(), `"outcome":"unknown"`)
	require.Contains(t, request(query, token, "http://portal.test", "").Body.String(), `"blocked_until"`)
	require.NotEqual(t, 200, request(use, token, "http://portal.test", newProof("reset", bindingID)).Code)
	require.EqualValues(t, 2, uses.Load())
}
