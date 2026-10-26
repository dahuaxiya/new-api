package controller

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdminUserTokens 验证真实数据库上的权限边界、单次证明、掩码响应及状态更新隔离。
func TestAdminUserTokens(t *testing.T) {
	actor, identity, user := setupAdminUserTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	key := &model.Token{UserId: user.Id, Key: strings.Repeat("a", 48), Name: "Codex_%", Status: 1, ExpiredTime: -1, UnlimitedQuota: true, HostingSeatID: "seat-1", Group: "hosted", AccessedTime: 1234}
	other := &model.Token{UserId: actor.Id, Key: strings.Repeat("b", 48), Name: "Root key", Status: 1, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, model.DB.Create(key).Error)
	require.NoError(t, model.DB.Create(other).Error)
	params := gin.Params{{Key: "id", Value: fmt.Sprint(user.Id)}, {Key: "token_id", Value: fmt.Sprint(key.Id)}}

	t.Run("masked list owner pagination literal search and counts", func(t *testing.T) {
		response := adminUserRequest("GET", "/api/user/tokens?p=1&page_size=1&keyword=_%", "", "", identity, 100, params, GetAdminUserTokens)
		require.Equal(t, 200, response.Code)
		assert.NotContains(t, response.Body.String(), key.Key)
		assert.NotContains(t, response.Body.String(), other.Name)
		var result struct {
			Success bool
			Data    struct {
				Items []model.AdminTokenItem
				Total int64
			}
		}
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		require.True(t, result.Success)
		require.Len(t, result.Data.Items, 1)
		assert.Equal(t, "seat-1", result.Data.Items[0].HostingSeatID)
		counts, err := model.AdminUserTokenCounts(actor.Id, []int{user.Id, actor.Id})
		require.NoError(t, err)
		assert.Equal(t, int64(1), counts[user.Id])
		_, total, err := model.ListAdminUserTokens(actor.Id, user.Id, 0, 10, 2, "")
		require.NoError(t, err)
		assert.Zero(t, total)
		response = adminUserRequest("GET", "/api/user/tokens?p=2&page_size=1", "", "", identity, 100, params, GetAdminUserTokens)
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &result))
		assert.Empty(t, result.Data.Items)
		assert.Equal(t, int64(1), result.Data.Total)
	})
	t.Run("role boundaries and PAT rejection", func(t *testing.T) {
		for _, role := range []int{1, 10} {
			require.NoError(t, model.DB.Model(actor).Update("role", role).Error)
			require.NoError(t, model.DB.Model(user).Update("role", 10).Error)
			_, _, err := model.ListAdminUserTokens(actor.Id, user.Id, 0, 10, 0, "")
			assert.ErrorIs(t, err, model.ErrAdminTokenForbidden)
			if role == 10 {
				_, _, err = model.ListAdminUserTokens(actor.Id, actor.Id, 0, 10, 0, "")
				assert.NoError(t, err)
			}
		}
		require.NoError(t, model.DB.Model(user).Update("role", 100).Error)
		_, _, err := model.ListAdminUserTokens(actor.Id, user.Id, 0, 10, 0, "")
		assert.ErrorIs(t, err, model.ErrAdminTokenForbidden)
		require.NoError(t, model.DB.Model(actor).Update("role", 100).Error)
		require.NoError(t, model.DB.Model(user).Update("role", 1).Error)
		pat := identity
		pat.SessionID = "pat:1"
		response := adminUserRequest("GET", "/api/user/tokens", "", "", pat, 100, params, GetAdminUserTokens)
		assert.Equal(t, 403, response.Code)
		response = adminUserRequest("GET", "/api/user/tokens", "", "", identity, 1, params, GetAdminUserTokens)
		assert.Equal(t, 403, response.Code)
	})
	operation := service.VerificationOperation{Scope: service.VerificationScopeAdminUserToken, Context: []byte(fmt.Sprintf(`{"user_id":%d,"token_id":%d,"action":"view"}`, user.Id, key.Id))}
	proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
	t.Run("missing proof wrong owner wrong action and replay", func(t *testing.T) {
		response := adminUserRequest("POST", "/api/user/token/key", `{"action":"view"}`, "", identity, 100, params, ManageAdminUserToken)
		assert.Equal(t, 403, response.Code)
		assert.Contains(t, response.Body.String(), "SECURITY_PROOF_REQUIRED")
		response = adminUserRequest("POST", "/api/user/token/key", `{"action":"copy"}`, proof, identity, 100, params, ManageAdminUserToken)
		assert.Equal(t, 403, response.Code)
		assert.Contains(t, response.Body.String(), "SECURITY_PROOF_CONTEXT_MISMATCH")
		foreign := gin.Params{{Key: "id", Value: fmt.Sprint(user.Id)}, {Key: "token_id", Value: fmt.Sprint(other.Id)}}
		response = adminUserRequest("POST", "/api/user/token/key", `{"action":"view"}`, proof, identity, 100, foreign, ManageAdminUserToken)
		assert.Equal(t, 403, response.Code)
		assert.NotContains(t, response.Body.String(), other.Key)
		response = adminUserRequest("POST", "/api/user/token/key", `{"action":"view"}`, proof, identity, 100, params, ManageAdminUserToken)
		assert.Equal(t, 200, response.Code)
		assert.Contains(t, response.Body.String(), "sk-"+key.Key)
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		response = adminUserRequest("POST", "/api/user/token/key", `{"action":"view"}`, proof, identity, 100, params, ManageAdminUserToken)
		assert.Equal(t, 403, response.Code)
		assert.Contains(t, response.Body.String(), "SECURITY_PROOF_CONSUMED")
	})
	t.Run("status only mutation and unusable enable rejection", func(t *testing.T) {
		operation.Context = []byte(fmt.Sprintf(`{"user_id":%d,"token_id":%d,"action":"disable"}`, user.Id, key.Id))
		proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
		response := adminUserRequest("PATCH", "/api/user/token/status", `{"action":"disable"}`, proof, identity, 100, params, ManageAdminUserToken)
		require.Equal(t, 200, response.Code)
		assert.Contains(t, response.Body.String(), `"success":true`)
		stored, err := model.GetTokenByIds(key.Id, user.Id)
		require.NoError(t, err)
		assert.Equal(t, 2, stored.Status)
		assert.Equal(t, key.HostingSeatID, stored.HostingSeatID)
		assert.Equal(t, key.AccessedTime, stored.AccessedTime)
		assert.Equal(t, key.Name, stored.Name)
		assert.Equal(t, key.Group, stored.Group)
		for _, values := range []map[string]any{{"expired_time": common.GetTimestamp() - 1}, {"expired_time": int64(-1), "unlimited_quota": false, "remain_quota": 0}} {
			require.NoError(t, model.DB.Model(key).Updates(values).Error)
			_, err = model.AdminUserTokenOperation(identity, user.Id, key.Id, 1)
			assert.ErrorIs(t, err, model.ErrAdminTokenUnusable)
		}
		require.NoError(t, model.DB.Model(key).Updates(map[string]any{"expired_time": int64(-1), "unlimited_quota": true}).Error)
		_, err = model.AdminUserTokenOperation(identity, user.Id, key.Id, 1)
		require.NoError(t, err)
	})
	t.Run("expired proof and revoked session cannot disclose", func(t *testing.T) {
		operation.Context = []byte(fmt.Sprintf(`{"user_id":%d,"token_id":%d,"action":"view"}`, user.Id, key.Id))
		proof := issueSecurityEnrollmentProof(t, identity, operation, service.VerificationMethodPassword)
		require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", model.AuthFlowPurposeSecurityProof).Update("expires_at", time.Now().Add(-time.Minute)).Error)
		response := adminUserRequest("POST", "/api/user/token/key", `{"action":"view"}`, proof, identity, 100, params, ManageAdminUserToken)
		assert.Equal(t, http.StatusForbidden, response.Code, response.Body.String())
		assert.NotContains(t, response.Body.String(), key.Key)
		require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", identity.SessionID).Update("revoked_at", common.GetTimestamp()).Error)
		_, err := model.AdminUserTokenOperation(identity, user.Id, key.Id, 0)
		assert.Error(t, err)
	})
	t.Run("audits include target and outcome but never credentials", func(t *testing.T) {
		var audits []model.AuditLog
		require.NoError(t, model.LOG_DB.Where("action = ?", "user.token.manage").Find(&audits).Error)
		require.NotEmpty(t, audits)
		var successes, failures int
		for _, audit := range audits {
			if audit.Success {
				successes++
			} else {
				failures++
			}
			data, err := common.Marshal(audit)
			require.NoError(t, err)
			assert.NotContains(t, string(data), key.Key)
			assert.NotContains(t, string(data), proof)
			assert.NotContains(t, string(data), "enrollment-password")
		}
		assert.Positive(t, successes)
		assert.Positive(t, failures)
	})
	t.Run("deleted token and user are inaccessible", func(t *testing.T) {
		require.NoError(t, model.DB.Delete(key).Error)
		assert.ErrorIs(t, model.CheckAdminUserToken(actor.Id, user.Id, key.Id), model.ErrAdminTokenForbidden)
		require.NoError(t, model.DB.Delete(user).Error)
		_, _, err := model.ListAdminUserTokens(actor.Id, user.Id, 0, 10, 0, "")
		assert.ErrorIs(t, err, model.ErrAdminTokenForbidden)
	})
}
