package model

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const SiteSSOProvider = "site-sso"
const SiteSSOCodePurpose = "site_sso_code"

// LogoutSiteSSOGrant ends only the issuer session recorded at code exchange. Retrying after
// a lost response must succeed even though that session is no longer valid for login.
func LogoutSiteSSOGrant(clientID string, grantID int64) error {
	if clientID == "" || grantID <= 0 {
		return ErrAuthFlowInvalid
	}
	var flow AuthFlow
	err := DB.Where("id = ? AND purpose = ? AND provider = ? AND intent IN ? AND consumed_at IS NOT NULL", grantID, SiteSSOCodePurpose, clientID, []string{AuthFlowIntentLogin, "revoked"}).First(&flow).Error
	if err != nil {
		return err
	}
	var grant SiteSSOGrant
	if common.UnmarshalJsonStr(flow.Payload, &grant) != nil || grant.Verified || grant.Identity.UserID <= 0 || grant.Identity.SessionID == "" {
		return ErrAuthFlowInvalid
	}
	_, err = RevokeUserSession(grant.Identity.UserID, grant.Identity.SessionID, "federated_logout")
	return err
}

// SiteSSOGrant 不携带角色和余额。身份源只证明是谁，消费站独立决定权限。
type SiteSSOGrant struct {
	Identity      AuthSessionIdentity `json:"identity"`
	Challenge     string              `json:"challenge"`
	CodeExpiresAt int64               `json:"code_expires_at"`
	Verified      bool                `json:"verified"`
}

// SiteSSOSubject 将签发者与不可复用的用户 ID 组合，用户名、邮箱变更不改变绑定。
// @param issuer 运维固定的签发者标识；迁机和换域名时保持不变。
// @param userID 签发者数据库的用户 ID。
// @return 可用于现有唯一身份声明表的稳定标识。
func SiteSSOSubject(issuer string, userID int) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + strconv.Itoa(userID)))
	return hex.EncodeToString(sum[:])
}

// ResolveSiteSSOUser 原子创建零余额普通账户；已声明的映射永远不按用户名或邮箱重绑。
// @param issuer 已校验的身份源。
// @param subjectID 身份源返回的用户 ID。
// @param displayName 仅用于展示，不作为登录或授权依据。
// @return 本站账户；并发首次进入时最多创建一个有效绑定，失败事务不留下孤儿账户。
func ResolveSiteSSOUser(issuer string, subjectID int, displayName string) (*User, error) {
	if issuer == "" || subjectID <= 0 {
		return nil, ErrAuthFlowInvalid
	}
	subject := SiteSSOSubject(issuer, subjectID)
	lookup := func() (*User, error) {
		var claim ExternalIdentityClaim
		if err := DB.Where("provider = ? AND subject = ?", SiteSSOProvider, subject).First(&claim).Error; err != nil {
			return nil, err
		}
		var user User
		err := DB.First(&user, claim.UserId).Error
		return &user, err
	}
	user, err := lookup()
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	name := []rune(displayName)
	if len(name) > 20 {
		name = name[:20]
	}
	user = &User{Username: "sso_" + subject[:16], DisplayName: string(name), Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1, Quota: 0, Password: "", AffCode: "sso_" + subject[:28]}
	err = DB.Transaction(func(tx *gorm.DB) error {
		// 不调用普通注册的赠额/邀请奖励，防止切站生成新的付费资产。
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		return ClaimExternalIdentityWithTx(tx, SiteSSOProvider, subject, user.Id)
	})
	if err != nil {
		// 唯一约束解决并发首次登录；仅接受已经提交的同一身份映射。
		if existing, lookupErr := lookup(); lookupErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return user, nil
}

// ExchangeSiteSSOCode 在同一事务校验 PKCE、会话版本和一次性消费；合法重放撤销关联授权。
// @param code 浏览器带回的随机授权码。
// @param clientID 已认证的固定客户端。
// @param verifier 消费站服务端保存的 PKCE verifier。
// @return 授权行和身份；调用方不能把失败时返回的身份用于创建登录。
func ExchangeSiteSSOCode(code, clientID, verifier string) (*AuthFlow, *SiteSSOGrant, error) {
	var flow AuthFlow
	var grant SiteSSOGrant
	replay := false
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	err := DB.Transaction(func(tx *gorm.DB) error {
		// 先执行写语句，SQLite 的并发读事务不能可靠升级为写事务。
		result := tx.Model(&AuthFlow{}).Where("token_hash = ? AND purpose = ? AND provider = ?", authFlowTokenHash(code), SiteSSOCodePurpose, clientID).UpdateColumn("purpose", SiteSSOCodePurpose)
		if result.Error != nil {
			return result.Error
		}
		if err := tx.Where("token_hash = ? AND purpose = ? AND provider = ?", authFlowTokenHash(code), SiteSSOCodePurpose, clientID).First(&flow).Error; err != nil {
			return ErrAuthFlowInvalid
		}
		if common.UnmarshalJsonStr(flow.Payload, &grant) != nil || len(verifier) != 43 || subtle.ConstantTimeCompare([]byte(challenge), []byte(grant.Challenge)) != 1 {
			return ErrAuthFlowInvalid
		}
		if flow.ConsumedAt != nil {
			replay = true
			return tx.Model(&flow).Update("intent", "revoked").Error
		}
		if grant.CodeExpiresAt <= time.Now().Unix() || !flow.ExpiresAt.After(time.Now()) {
			return ErrAuthFlowExpired
		}
		if err := ValidateAuthSessionWithTx(tx, grant.Identity); err != nil {
			return err
		}
		now := time.Now()
		return tx.Model(&flow).Update("consumed_at", now).Error
	})
	if replay && err == nil {
		err = ErrAuthFlowConsumed
	}
	return &flow, &grant, err
}

// ValidateSiteSSOGrant 每次消费站管理请求都核对授权和源会话，身份源不可用时拒绝放行。
// @param clientID 已认证的客户端。
// @param grantID 先前成功换码所得的授权行 ID。
// @return 仍有效的源身份，不能当作消费站权限。
func ValidateSiteSSOGrant(clientID string, grantID int64) (*SiteSSOGrant, error) {
	var grant SiteSSOGrant
	err := DB.Transaction(func(tx *gorm.DB) error {
		var flow AuthFlow
		if err := tx.Where("id = ? AND purpose = ? AND provider = ? AND intent = ? AND consumed_at IS NOT NULL AND expires_at > ?", grantID, SiteSSOCodePurpose, clientID, AuthFlowIntentLogin, time.Now()).First(&flow).Error; err != nil {
			return ErrAuthFlowInvalid
		}
		if common.UnmarshalJsonStr(flow.Payload, &grant) != nil {
			return ErrAuthFlowInvalid
		}
		return ValidateAuthSessionWithTx(tx, grant.Identity)
	})
	return &grant, err
}
