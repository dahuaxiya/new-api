package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupRegistrationTest 复用真实数据库与 SMTP 夹具；测试不接触生产用户或真实邮箱。
func setupRegistrationTest(t *testing.T) *securityMailbox {
	t.Helper()
	setupSecurityEnrollmentTest(t)
	common.OptionMapRWMutex.Lock()
	oldPolicy := common.OptionMap[model.RegistrationAbusePolicyKey]
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	delete(common.OptionMap, model.RegistrationAbusePolicyKey)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap[model.RegistrationAbusePolicyKey] = oldPolicy
		common.OptionMapRWMutex.Unlock()
	})
	oldRegister, oldPassword, oldEmail := common.RegisterEnabled, common.PasswordRegisterEnabled, common.EmailVerificationEnabled
	oldToken, oldQuota := constant.GenerateDefaultToken, common.QuotaForNewUser
	common.RegisterEnabled, common.PasswordRegisterEnabled, common.EmailVerificationEnabled = true, true, true
	constant.GenerateDefaultToken, common.QuotaForNewUser = false, 0
	t.Cleanup(func() {
		common.RegisterEnabled, common.PasswordRegisterEnabled, common.EmailVerificationEnabled = oldRegister, oldPassword, oldEmail
		constant.GenerateDefaultToken, common.QuotaForNewUser = oldToken, oldQuota
	})
	return newSecurityMailbox(t)
}

// registrationRequest 直接经过 HTTP 控制器并禁止信任伪造代理头，验证服务端合同。
func registrationRequest(t *testing.T, method, path, ip, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/api/verification", SendEmailVerification)
	router.GET("/api/reset_password", SendPasswordResetEmail)
	router.POST("/api/user/register", Register)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "198.51.100.222")
	req.RemoteAddr = ip + ":1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

// registrationBody 仅为测试生成正常注册请求，并加入必须被忽略的权限字段。
func registrationBody(t *testing.T, username, email, code string) string {
	t.Helper()
	body, err := common.Marshal(map[string]any{"username": username, "password": "private-registration-password", "email": email, "verification_code": code, "role": 100, "quota": 999999999})
	require.NoError(t, err)
	return string(body)
}

// TestRegistrationMailGuard 验证关闭注册、密码注册或邮箱验证时都不允许调用 SMTP。
func TestRegistrationMailGuard(t *testing.T) {
	mailbox := setupRegistrationTest(t)
	for _, flag := range []*bool{&common.RegisterEnabled, &common.PasswordRegisterEnabled, &common.EmailVerificationEnabled} {
		*flag = false
		response := registrationRequest(t, "GET", "/api/verification?email=test@example.com", "203.0.113.1", "")
		require.Contains(t, response.Body.String(), `"success":false`)
		response = registrationRequest(t, "POST", "/api/user/register", "203.0.113.1", registrationBody(t, "blocked", "test@example.com", "123456"))
		require.Contains(t, response.Body.String(), `"success":false`)
		*flag = true
	}
	require.Empty(t, mailbox.mail)
}

// TestRegistrationEmailRoundTrip 验证真实邮件、地址标准化、角色隔离及验证码单次消费。
func TestRegistrationEmailRoundTrip(t *testing.T) {
	mailbox := setupRegistrationTest(t)
	response := registrationRequest(t, "GET", "/api/verification?email=Test@Example.com", "203.0.113.2", "")
	require.Contains(t, response.Body.String(), `"success":true`)
	code := mailbox.code(t, "test@example.com")
	require.Contains(t, registrationRequest(t, "GET", "/api/verification?email=test@example.com", "203.0.113.3", "").Body.String(), `"success":false`)
	body := registrationBody(t, "registered", "test@example.com", code)
	response = registrationRequest(t, "POST", "/api/user/register", "203.0.113.2", body)
	require.Contains(t, response.Body.String(), `"success":true`, response.Body.String())
	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "registered").First(&user).Error)
	require.Equal(t, common.RoleCommonUser, user.Role)
	require.Zero(t, user.Quota)
	require.True(t, common.ValidatePasswordAndHash("private-registration-password", user.Password))
	require.Contains(t, registrationRequest(t, "POST", "/api/user/register", "203.0.113.2", registrationBody(t, "replay", "test@example.com", code)).Body.String(), "Verification code is incorrect")
	var flows []model.AuthFlow
	require.NoError(t, model.DB.Find(&flows).Error)
	for _, flow := range flows {
		require.NotContains(t, flow.Payload, code)
		require.NotContains(t, flow.TokenHash, "test@example.com")
	}
}

// TestRegistrationCodeBoundaries 验证错误次数、过期、发送失败和重发的状态边界。
func TestRegistrationCodeBoundaries(t *testing.T) {
	for _, scenario := range []string{"five failures", "expired", "undelivered", "resend", "insert failure"} {
		t.Run(scenario, func(t *testing.T) {
			setupRegistrationTest(t)
			email := "boundary@example.com"
			challenge, err := model.BeginRegistrationEmail(email, "203.0.113.4")
			code := challenge.Code
			require.NoError(t, err)
			if scenario != "undelivered" {
				require.NoError(t, model.MarkRegistrationEmailDelivered(email, code))
			}
			register := func(code, name string) error {
				return model.RegisterUserWithEmail(&model.User{Username: name, Email: email, Password: "private-registration-password", Role: common.RoleCommonUser}, code, "203.0.113.4")
			}
			switch scenario {
			case "five failures":
				for range 5 {
					require.ErrorIs(t, register("not-a-code", "wrong"), model.ErrRegistrationCode)
				}
				require.ErrorIs(t, register(code, "locked"), model.ErrRegistrationCode)
				require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", "registration_code").Update("created_at", time.Now().Add(-2*time.Minute)).Error)
				_, err = model.BeginRegistrationEmail(email, "203.0.113.4")
				require.ErrorIs(t, err, model.ErrRegistrationRateLimit)
			case "expired":
				require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", "registration_code").Update("expires_at", time.Now().Add(-time.Second)).Error)
				require.ErrorIs(t, register(code, "expired"), model.ErrRegistrationCode)
			case "undelivered":
				require.ErrorIs(t, register(code, "undelivered"), model.ErrRegistrationCode)
			case "resend":
				require.ErrorIs(t, register("wrong", "wrong"), model.ErrRegistrationCode)
				require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("purpose = ?", "registration_code").Update("created_at", time.Now().Add(-2*time.Minute)).Error)
				freshChallenge, err := model.BeginRegistrationEmail(email, "203.0.113.4")
				fresh := freshChallenge.Code
				require.NoError(t, err)
				require.NoError(t, model.MarkRegistrationEmailDelivered(email, fresh))
				if fresh != code {
					require.ErrorIs(t, register(code, "old"), model.ErrRegistrationCode)
				}
				require.NoError(t, register(fresh, "fresh"))
			case "insert failure":
				require.Error(t, register(code, "enrollment-user"))
				require.NoError(t, register(code, "corrected"))
			}
		})
	}
}

// TestRegistrationConcurrencyAndLimits 验证并发请求不能重复发信或复用一个验证码创建多个用户。
func TestRegistrationConcurrencyAndLimits(t *testing.T) {
	setupRegistrationTest(t)
	codes := make(chan string, 4)
	errors := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			challenge, err := model.BeginRegistrationEmail("race@example.com", "203.0.113.5")
			code := challenge.Code
			if err == nil {
				codes <- code
			} else {
				errors <- err
			}
		})
	}
	wg.Wait()
	close(codes)
	close(errors)
	require.Len(t, codes, 1)
	for err := range errors {
		require.ErrorIs(t, err, model.ErrRegistrationRateLimit)
	}
	code := <-codes
	require.NoError(t, model.MarkRegistrationEmailDelivered("race@example.com", code))
	results := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			results <- model.RegisterUserWithEmail(&model.User{Username: fmt.Sprintf("race%d", i), Email: "race@example.com", Password: "private-registration-password", Role: common.RoleCommonUser}, code, "203.0.113.5")
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, model.ErrRegistrationCode)
		}
	}
	require.Equal(t, 1, successes)
	// 同一 IP 第四次成功注册被拒绝，即使邮箱不同、进程内没有计数缓存。
	for i := range 3 {
		email := fmt.Sprintf("next%d@example.com", i)
		challenge, err := model.BeginRegistrationEmail(email, "203.0.113.5")
		code := challenge.Code
		require.NoError(t, err)
		require.NoError(t, model.MarkRegistrationEmailDelivered(email, code))
		err = model.RegisterUserWithEmail(&model.User{Username: fmt.Sprintf("next%d", i), Email: email, Password: "private-registration-password", Role: common.RoleCommonUser}, code, "203.0.113.5")
		if i < 2 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, model.ErrRegistrationRateLimit)
		}
	}
	require.Equal(t, model.RegistrationNetwork("2001:db8:12:34::1"), model.RegistrationNetwork("2001:db8:12:34::99"))
	require.Equal(t, model.RegistrationNetwork("::ffff:192.0.2.1"), model.RegistrationNetwork("192.0.2.1"))
}

// TestRegistrationPersistentBudgets 验证邮箱、IP、全站预算、失败事务以及旧表的重复迁移兼容。
func TestRegistrationPersistentBudgets(t *testing.T) {
	setupRegistrationTest(t)
	for range 5 {
		require.NoError(t, model.ReservePublicEmail("limit@example.com", "203.0.113.6"))
	}
	require.ErrorIs(t, model.ReservePublicEmail("limit@example.com", "203.0.113.7"), model.ErrRegistrationRateLimit)
	// 仅迁移已发布的同一个 AuthFlow 模型，不增加新 schema；读回之前的预算仍有效。
	require.NoError(t, model.DB.AutoMigrate(&model.AuthFlow{}))
	require.NoError(t, model.DB.AutoMigrate(&model.AuthFlow{}))
	require.ErrorIs(t, model.ReservePublicEmail("limit@example.com", "203.0.113.7"), model.ErrRegistrationRateLimit)
	for range 20 {
		require.NoError(t, model.ReserveRegistrationAttempt("203.0.113.8"))
	}
	require.ErrorIs(t, model.ReserveRegistrationAttempt("203.0.113.8"), model.ErrRegistrationRateLimit)
	// 把全站预算放到边界，两个不同 IP 并发只允许最后一封。
	day := time.Now().UTC().Truncate(24 * time.Hour)
	hash := common.GenerateHMACWithKey([]byte("auth-flow-v1:"+common.SessionSecret), "registration-limit:mail-day:"+day.Format(time.RFC3339))
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("token_hash = ?", hash).Update("payload", "79").Error)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Go(func() {
			results <- model.ReservePublicEmail(fmt.Sprintf("global%d@example.com", i), fmt.Sprintf("203.0.113.%d", 20+i))
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, model.ErrRegistrationRateLimit)
		}
	}
	require.Equal(t, 1, successes)
	// 密码重置也被共用预算阻止，不能绕过注册邮件上限。
	response := registrationRequest(t, "GET", "/api/reset_password?email=nobody@example.com", "203.0.113.99", "")
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", hash).First(&flow).Error)
	require.Equal(t, "80", flow.Payload)
	require.NoError(t, model.DB.Session(&gorm.Session{}).Where("token_hash = ?", hash).First(&flow).Error)
	require.Equal(t, "80", flow.Payload)
}

// TestRegistrationSMTPFailure 不泄露上游错误，且不能消费没有成功发送的验证码。
func TestRegistrationSMTPFailure(t *testing.T) {
	setupRegistrationTest(t)
	common.SMTPPort = 1
	response := registrationRequest(t, "GET", "/api/verification?email=fail@example.com", "203.0.113.60", "")
	require.Contains(t, response.Body.String(), "Verification email could not be sent")
	require.NotContains(t, response.Body.String(), "connection refused")
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", "registration_code").First(&flow).Error)
	require.Contains(t, flow.Payload, `"ready":false`)
}

// saveRegistrationPolicy 通过实际选项保存路径写入整组策略，返回可用于核对数据库的 JSON。
func saveRegistrationPolicy(t *testing.T, policy model.RegistrationAbusePolicy) string {
	t.Helper()
	raw, err := common.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, model.UpdateOption(model.RegistrationAbusePolicyKey, string(raw)))
	return string(raw)
}

// TestRegistrationPolicyValidation 验证整组保存、持久化、非法参数拒绝及写库失败不能提前生效。
func TestRegistrationPolicyValidation(t *testing.T) {
	setupRegistrationTest(t)
	defaults := model.DefaultRegistrationAbusePolicy()
	require.Equal(t, defaults, model.CurrentRegistrationAbusePolicy())
	policy := defaults
	policy.EmailDaily = 2
	good := saveRegistrationPolicy(t, policy)
	var stored model.Option
	require.NoError(t, model.DB.Where(&model.Option{Key: model.RegistrationAbusePolicyKey}).First(&stored).Error)
	require.Equal(t, good, stored.Value)
	for _, raw := range []string{`{}`, `null`, `{"email_daily":1.5}`, strings.Replace(good, `"email_daily":2`, `"email_daily":0`, 1), strings.Replace(good, `"code_max_attempts":5`, `"code_max_attempts":11`, 1), strings.Replace(good, `"resend_seconds":60`, `"resend_seconds":601`, 1), strings.Replace(good, `"ip_email_hourly":10`, `"ip_email_hourly":31`, 1)} {
		require.Error(t, model.UpdateOption(model.RegistrationAbusePolicyKey, raw))
		require.Equal(t, policy, model.CurrentRegistrationAbusePolicy())
		require.NoError(t, model.DB.Where(&model.Option{Key: model.RegistrationAbusePolicyKey}).First(&stored).Error)
		require.Equal(t, good, stored.Value)
	}
	// 模拟进程失去内存值并从 options 恢复，数据库中无新增 schema。
	common.OptionMapRWMutex.Lock()
	delete(common.OptionMap, model.RegistrationAbusePolicyKey)
	common.OptionMapRWMutex.Unlock()
	require.NoError(t, model.UpdateOptionsBulk(map[string]string{model.RegistrationAbusePolicyKey: stored.Value}))
	require.Equal(t, policy, model.CurrentRegistrationAbusePolicy())
	require.NoError(t, model.DB.Migrator().DropTable(&model.Option{}))
	policy.EmailDaily = 3
	raw, err := common.Marshal(policy)
	require.NoError(t, err)
	require.Error(t, model.UpdateOption(model.RegistrationAbusePolicyKey, string(raw)))
	require.Equal(t, 2, model.CurrentRegistrationAbusePolicy().EmailDaily)
}

// TestRegistrationPolicyLiveCounters 验证修改阈值不清零计数，注册与密码重置继续共用限制。
func TestRegistrationPolicyLiveCounters(t *testing.T) {
	setupRegistrationTest(t)
	for range 2 {
		require.NoError(t, model.ReservePublicEmail("saved@example.com", "203.0.113.101"))
	}
	policy := model.DefaultRegistrationAbusePolicy()
	policy.EmailDaily = 2
	policy.RegistrationIPHourly = 2
	saveRegistrationPolicy(t, policy)
	require.ErrorIs(t, model.ReservePublicEmail("saved@example.com", "203.0.113.102"), model.ErrRegistrationRateLimit)
	require.Equal(t, http.StatusTooManyRequests, registrationRequest(t, "GET", "/api/reset_password?email=saved@example.com", "203.0.113.103", "").Code)
	policy.EmailDaily = 3
	saveRegistrationPolicy(t, policy)
	require.NoError(t, model.ReservePublicEmail("saved@example.com", "203.0.113.104"))
	require.ErrorIs(t, model.ReservePublicEmail("saved@example.com", "203.0.113.105"), model.ErrRegistrationRateLimit)
	for range 2 {
		require.NoError(t, model.ReserveRegistrationAttempt("203.0.113.110"))
	}
	saveRegistrationPolicy(t, policy)
	require.ErrorIs(t, model.ReserveRegistrationAttempt("203.0.113.110"), model.ErrRegistrationRateLimit)
}

// TestRegistrationPolicyChallenge 验证邮件文案与冷却返回值使用同一快照，现有码过期时间及锁定不能被放宽设置改写。
func TestRegistrationPolicyChallenge(t *testing.T) {
	mailbox := setupRegistrationTest(t)
	policy := model.DefaultRegistrationAbusePolicy()
	policy.CodeTTLMinutes, policy.ResendSeconds, policy.CodeMaxAttempts = 2, 90, 2
	saveRegistrationPolicy(t, policy)
	response := registrationRequest(t, "GET", "/api/verification?email=policy@example.com", "203.0.113.111", "")
	require.Contains(t, response.Body.String(), `"resend_seconds":90`)
	code := mailbox.code(t, "policy@example.com")
	mailbox.mutex.Lock()
	message := mailbox.mail["policy@example.com"][0]
	mailbox.mutex.Unlock()
	require.Regexp(t, `2 (分钟|分鐘|minutes)`, message)
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("purpose = ?", "registration_code").First(&flow).Error)
	require.InDelta(t, 120, flow.ExpiresAt.Sub(flow.CreatedAt).Seconds(), 1)
	// 旧的 60 秒硬编码若残留，这个重发会被错误接受。
	require.NoError(t, model.DB.Model(&model.AuthFlow{}).Where("id = ?", flow.Id).Update("created_at", time.Now().Add(-70*time.Second)).Error)
	_, err := model.BeginRegistrationEmail("policy@example.com", "203.0.113.111")
	require.ErrorIs(t, err, model.ErrRegistrationRateLimit)
	user := &model.User{Username: "policyuser", Email: "policy@example.com", Password: "private-registration-password", Role: common.RoleCommonUser}
	for range 2 {
		require.ErrorIs(t, model.RegisterUserWithEmail(user, "wrong", "203.0.113.111"), model.ErrRegistrationCode)
	}
	policy.CodeTTLMinutes, policy.CodeMaxAttempts, policy.ResendSeconds = 10, 10, 30
	saveRegistrationPolicy(t, policy)
	require.ErrorIs(t, model.RegisterUserWithEmail(user, code, "203.0.113.111"), model.ErrRegistrationCode)
	_, err = model.BeginRegistrationEmail("policy@example.com", "203.0.113.111")
	require.ErrorIs(t, err, model.ErrRegistrationRateLimit)
	var after model.AuthFlow
	require.NoError(t, model.DB.Where("id = ?", flow.Id).First(&after).Error)
	require.Equal(t, flow.ExpiresAt, after.ExpiresAt)
	fresh, err := model.BeginRegistrationEmail("freshpolicy@example.com", "203.0.113.111")
	require.NoError(t, err)
	require.Equal(t, 10, fresh.ValidMinutes)
	require.Equal(t, 30, fresh.ResendSeconds)
}

// TestRegistrationPolicyPermission 验证实际 RootAuth 拒绝普通用户，并允许 root 保存与回读。
func TestRegistrationPolicyPermission(t *testing.T) {
	setupRegistrationTest(t)
	router := gin.New()
	router.PUT("/api/option/", middleware.RootAuth(), UpdateOption)
	router.GET("/api/option/", middleware.RootAuth(), GetOptions)
	policy := model.DefaultRegistrationAbusePolicy()
	policy.EmailDaily = 4
	raw, err := common.Marshal(policy)
	require.NoError(t, err)
	body, err := common.Marshal(map[string]string{"key": model.RegistrationAbusePolicyKey, "value": string(raw)})
	require.NoError(t, err)
	for _, role := range []int{common.RoleCommonUser, common.RoleRootUser} {
		user := &model.User{Username: fmt.Sprintf("policyrole%d", role), AffCode: fmt.Sprintf("policy%d", role), Password: "unused", Role: role, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
		require.NoError(t, model.DB.Create(user).Error)
		require.NoError(t, model.PublishUserAuthCache(user.Id))
		_, token := createAccessTokenTestSession(t, user.Id, fmt.Sprintf("policy-session-%d", role))
		request := httptest.NewRequest("PUT", "/api/option/", strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if role == common.RoleCommonUser {
			require.Equal(t, http.StatusForbidden, response.Code)
			require.Equal(t, model.DefaultRegistrationAbusePolicy(), model.CurrentRegistrationAbusePolicy())
		} else {
			require.Contains(t, response.Body.String(), `"success":true`)
			require.Equal(t, policy, model.CurrentRegistrationAbusePolicy())
			request = httptest.NewRequest("GET", "/api/option/", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Contains(t, response.Body.String(), model.RegistrationAbusePolicyKey)
		}
	}
}
