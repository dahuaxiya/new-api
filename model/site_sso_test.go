package model

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// TestSiteSSODatabases 验证真实三方言的升级幂等、零余额建号、身份隔离、授权重放及源会话撤销。
func TestSiteSSODatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			oldRedis := common.RedisEnabled
			common.RedisEnabled = false
			t.Cleanup(func() { common.RedisEnabled = oldRedis })
			user := User{Username: "existing-retail", Password: "unchanged-hash", Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", Quota: 12345, AuthVersion: 1}
			require.NoError(t, db.Create(&user).Error)
			session := UserSession{SID: "old-session", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: UserSessionStatusActive, RefreshHash: "old-hash", LoginMethod: "password", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, CreateUserSession(&session))
			// 模拟发布版缺少新增列，升级两次都不得改动历史余额、密码和会话。
			require.NoError(t, db.Migrator().DropColumn(&UserSession{}, "federated_identity"))
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			var stored User
			require.NoError(t, db.First(&stored, user.Id).Error)
			require.Equal(t, 12345, stored.Quota)
			require.Equal(t, "unchanged-hash", stored.Password)
			legacy, err := GetUserSessionBySID(session.SID)
			require.NoError(t, err)
			require.Empty(t, legacy.FederatedIdentity)
			issuer := "https://identity.example"
			// 显式映射允许现有管理员继续使用原账本，源角色不会参与赋权。
			require.NoError(t, ClaimExternalIdentityWithTx(db, SiteSSOProvider, SiteSSOSubject(issuer, 1), user.Id))
			mapped, err := ResolveSiteSSOUser(issuer, 1, "changed-name")
			require.NoError(t, err)
			require.Equal(t, user.Id, mapped.Id)
			require.Equal(t, 12345, mapped.Quota)
			require.Error(t, ClaimExternalIdentityWithTx(db, SiteSSOProvider, SiteSSOSubject(issuer, 2), user.Id))
			customer, err := ResolveSiteSSOUser(issuer, 2, "existing-retail")
			require.NoError(t, err)
			require.NotEqual(t, user.Id, customer.Id)
			require.Zero(t, customer.Quota)
			require.Empty(t, customer.Password)
			require.Equal(t, common.RoleCommonUser, customer.Role)
			otherIssuer, err := ResolveSiteSSOUser("https://other.example", 2, "existing-retail")
			require.NoError(t, err)
			require.NotEqual(t, customer.Id, otherIssuer.Id)
			var wg sync.WaitGroup
			ids := make(chan int, 2)
			errs := make(chan error, 2)
			for range 2 {
				wg.Go(func() {
					u, e := ResolveSiteSSOUser(issuer, 3, "concurrent")
					errs <- e
					if e == nil {
						ids <- u.Id
					}
				})
			}
			wg.Wait()
			close(errs)
			close(ids)
			for err := range errs {
				require.NoError(t, err)
			}
			var id int
			for got := range ids {
				if id != 0 {
					require.Equal(t, id, got)
				}
				id = got
			}
			identity := AuthSessionIdentity{UserID: user.Id, SessionID: session.SID, UserAuthVersion: 1, SessionVersion: 1}
			verifier := strings.Repeat("v", 43)
			sum := sha256.Sum256([]byte(verifier))
			challenge := base64.RawURLEncoding.EncodeToString(sum[:])
			makeCode := func(expiry int64) (string, int64) {
				raw, e := common.Marshal(SiteSSOGrant{Identity: identity, Challenge: challenge, CodeExpiresAt: expiry})
				require.NoError(t, e)
				code, flow, e := CreateAuthFlow(AuthFlowCreate{Purpose: SiteSSOCodePurpose, Provider: "retail", Intent: "login", UserId: user.Id, SessionId: session.SID, Payload: string(raw), ExpiresAt: time.Now().Add(time.Hour)})
				require.NoError(t, e)
				return code, flow.Id
			}
			code, grantID := makeCode(time.Now().Add(time.Minute).Unix())
			_, _, err = ExchangeSiteSSOCode(code, "wrong-client", verifier)
			require.Error(t, err)
			_, _, err = ExchangeSiteSSOCode(code, "retail", strings.Repeat("x", 43))
			require.Error(t, err)
			_, _, err = ExchangeSiteSSOCode(code, "retail", verifier)
			require.NoError(t, err)
			_, err = ValidateSiteSSOGrant("retail", grantID)
			require.NoError(t, err)
			_, _, err = ExchangeSiteSSOCode(code, "retail", verifier)
			require.ErrorIs(t, err, ErrAuthFlowConsumed)
			_, err = ValidateSiteSSOGrant("retail", grantID)
			require.Error(t, err, "合法重放必须撤销先前授出的会话")
			expired, _ := makeCode(time.Now().Add(-time.Second).Unix())
			_, _, err = ExchangeSiteSSOCode(expired, "retail", verifier)
			require.ErrorIs(t, err, ErrAuthFlowExpired)
			concurrent, concurrentID := makeCode(time.Now().Add(time.Minute).Unix())
			outcomes := make(chan error, 2)
			for range 2 {
				wg.Go(func() { _, _, e := ExchangeSiteSSOCode(concurrent, "retail", verifier); outcomes <- e })
			}
			wg.Wait()
			close(outcomes)
			successes := 0
			for e := range outcomes {
				if e == nil {
					successes++
				} else {
					require.ErrorIs(t, e, ErrAuthFlowConsumed)
				}
			}
			require.Equal(t, 1, successes)
			_, err = ValidateSiteSSOGrant("retail", concurrentID)
			require.Error(t, err)
			valid, validID := makeCode(time.Now().Add(time.Minute).Unix())
			_, _, err = ExchangeSiteSSOCode(valid, "retail", verifier)
			require.NoError(t, err)
			require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("auth_version", 2).Error)
			_, err = ValidateSiteSSOGrant("retail", validID)
			require.Error(t, err, "密码变化应使消费站会话失效")
			require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("auth_version", 1).Error)
			require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("status", common.UserStatusDisabled).Error)
			_, err = ValidateSiteSSOGrant("retail", validID)
			require.Error(t, err)
			require.NoError(t, db.Model(&User{}).Where("id = ?", user.Id).Update("status", common.UserStatusEnabled).Error)
			// Only an exchanged grant from this client can end the issuer session.
			_, pending := makeCode(time.Now().Add(time.Minute).Unix())
			require.Error(t, LogoutSiteSSOGrant("retail", pending))
			require.Error(t, LogoutSiteSSOGrant("other", validID))
			_, err = ValidateSiteSSOGrant("retail", validID)
			require.NoError(t, err)
			other := UserSession{SID: "other-device", UserID: user.Id, Version: 1, UserAuthVersion: 1, Status: UserSessionStatusActive, RefreshHash: "other-hash", LoginMethod: "password", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, CreateUserSession(&other))
			require.NoError(t, LogoutSiteSSOGrant("retail", validID))
			require.NoError(t, LogoutSiteSSOGrant("retail", validID))
			otherState, err := GetUserSessionBySID(other.SID)
			require.NoError(t, err)
			require.Equal(t, UserSessionStatusActive, otherState.Status)
			_, err = ValidateSiteSSOGrant("retail", validID)
			require.Error(t, err)
		})
	}
}
