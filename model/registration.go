package model

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	registrationCodePurpose  = "registration_code"
	registrationLimitPurpose = "registration_limit"
)

var (
	ErrRegistrationRateLimit = errors.New("Too many registration requests. Please try again later.")
	ErrRegistrationCode      = errors.New("Verification code is incorrect or has expired")
)

// registrationCodeState 不保存可直接使用的验证码；发送成功后才允许消费。
type registrationCodeState struct {
	Hash        string `json:"hash"`
	Ready       bool   `json:"ready"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	Locked      bool   `json:"locked,omitempty"`
}

// registrationLimit 在已有认证状态表中保存计数，进程重启或切换入口不会清零。
type registrationLimit struct {
	Key   string
	Start time.Time
	End   time.Time
	Max   int
}

// RegistrationNetwork 归并 IPv4 映射地址和 IPv6 /64，防止同一出口换地址绕过限制。
// 调用方必须传入由可信代理配置解析出的 ClientIP，不能直接传请求头。
func RegistrationNetwork(ip string) string {
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return "unknown"
	}
	address = address.Unmap()
	if address.Is6() {
		return netip.PrefixFrom(address, 64).Masked().String()
	}
	return address.String()
}

// reserveRegistrationLimitsWithTx 以固定顺序锁定计数并原子预占，超限时由外层回滚。
// 首条 SQL 必须为写入，使 SQLite 不需要把并发读事务升级成写事务。
func reserveRegistrationLimitsWithTx(tx *gorm.DB, limits []registrationLimit) error {
	for _, limit := range limits {
		hash := authFlowTokenHash("registration-limit:" + limit.Key + ":" + limit.Start.UTC().Format(time.RFC3339))
		flow := AuthFlow{TokenHash: hash, Purpose: registrationLimitPurpose, Payload: "0", ExpiresAt: limit.End}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "token_hash"}}, DoNothing: true}).Create(&flow).Error; err != nil {
			return err
		}
		// PostgreSQL 的 DO NOTHING 不保留已有行的写锁；额外 UPDATE 统一三库锁语义。
		query := tx.Model(&AuthFlow{}).Where("token_hash = ? AND purpose = ?", hash, registrationLimitPurpose).Session(&gorm.Session{})
		if err := query.UpdateColumn("payload", gorm.Expr("payload")).Error; err != nil {
			return err
		}
		if err := query.First(&flow).Error; err != nil {
			return err
		}
		count, err := strconv.Atoi(flow.Payload)
		if err != nil {
			return err
		}
		if count >= limit.Max {
			return ErrRegistrationRateLimit
		}
		if err := query.Update("payload", strconv.Itoa(count+1)).Error; err != nil {
			return err
		}
	}
	return nil
}

// ReservePublicEmail 限制匿名注册及密码重置邮件的共用预算，防止从另一个公开入口刷信。
// 发送失败也保留预占；退款会让故障期间的重试无限触发 SMTP。额度按 UTC 日/月计算。
func ReservePublicEmail(email, ip string) error {
	policy := CurrentRegistrationAbusePolicy()
	return DB.Transaction(func(tx *gorm.DB) error { return reservePublicEmailWithTx(tx, email, ip, policy) })
}

// reservePublicEmailWithTx 允许注册挑战与邮件预算一起提交，冷却期拒绝不浪费全站配额。
func reservePublicEmailWithTx(tx *gorm.DB, email, ip string, policy RegistrationAbusePolicy) error {
	now := time.Now().UTC()
	day := now.Truncate(24 * time.Hour)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	network := RegistrationNetwork(ip)
	return reserveRegistrationLimitsWithTx(tx, []registrationLimit{
		{Key: "mail-month", Start: month, End: month.AddDate(0, 1, 0), Max: policy.EmailGlobalMonthly},
		{Key: "mail-day", Start: day, End: day.AddDate(0, 0, 1), Max: policy.EmailGlobalDaily},
		{Key: "mail-ip-day:" + network, Start: day, End: day.AddDate(0, 0, 1), Max: policy.IPEmailDaily},
		{Key: "mail-ip-hour:" + network, Start: now.Truncate(time.Hour), End: now.Truncate(time.Hour).Add(time.Hour), Max: policy.IPEmailHourly},
		{Key: "mail-email-day:" + NormalizeEmail(email), Start: day, End: day.AddDate(0, 0, 1), Max: policy.EmailDaily},
	})
}

// RegistrationEmailChallenge 携带本次挑战实际采用的设置，邮件和客户端不再读取可能已变化的配置。
type RegistrationEmailChallenge struct {
	Code          string
	ValidMinutes  int
	ResendSeconds int
}

// registrationAttemptLimit 兼容升级前的五次限制；放宽设置不能解锁已有挑战。
func registrationAttemptLimit(state registrationCodeState, policy RegistrationAbusePolicy) int {
	previous := state.MaxAttempts
	if previous == 0 {
		previous = 5
	}
	return min(previous, policy.CodeMaxAttempts)
}

// BeginRegistrationEmail 生成随机六位验证码并原子替换该邮箱的挑战。
// 重发继承尚未过期挑战的错误次数和更严格的上限；已发验证码的过期时间不会被设置修改。
func BeginRegistrationEmail(email, ip string) (RegistrationEmailChallenge, error) {
	policy := CurrentRegistrationAbusePolicy()
	email = NormalizeEmail(email)
	number, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return RegistrationEmailChallenge{}, err
	}
	code := fmt.Sprintf("%06d", number.Int64())
	now := time.Now().UTC()
	hash := authFlowTokenHash("registration-email:" + email)
	err = DB.Transaction(func(tx *gorm.DB) error {
		// MySQL DATETIME 会按列精度舍入，初始占位必须明确过期，不能用当前纳秒时间。
		flow := AuthFlow{TokenHash: hash, Purpose: registrationCodePurpose, ExpiresAt: now.Add(-time.Second), Payload: "{}"}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "token_hash"}}, DoNothing: true}).Create(&flow).Error; err != nil {
			return err
		}
		query := tx.Model(&AuthFlow{}).Where("token_hash = ? AND purpose = ?", hash, registrationCodePurpose).Session(&gorm.Session{})
		if err := query.UpdateColumn("payload", gorm.Expr("payload")).Error; err != nil {
			return err
		}
		if err := query.First(&flow).Error; err != nil {
			return err
		}
		var previous registrationCodeState
		if err := common.UnmarshalJsonStr(flow.Payload, &previous); err != nil {
			return err
		}
		state := registrationCodeState{Hash: authFlowTokenHash("registration-code:" + email + ":" + code), MaxAttempts: policy.CodeMaxAttempts}
		expires := now.Add(time.Duration(policy.CodeTTLMinutes) * time.Minute)
		if flow.ExpiresAt.After(now) {
			if flow.CreatedAt.Add(time.Duration(policy.ResendSeconds)*time.Second).After(now) || previous.Locked || previous.Attempts >= registrationAttemptLimit(previous, policy) {
				return ErrRegistrationRateLimit
			}
			state.Attempts = previous.Attempts
			state.MaxAttempts = registrationAttemptLimit(previous, policy)
		}
		if err := reservePublicEmailWithTx(tx, email, ip, policy); err != nil {
			return err
		}
		payload, err := common.Marshal(state)
		if err != nil {
			return err
		}
		return query.Updates(map[string]any{"payload": string(payload), "created_at": now, "expires_at": expires, "consumed_at": nil}).Error
	})
	return RegistrationEmailChallenge{Code: code, ValidMinutes: policy.CodeTTLMinutes, ResendSeconds: policy.ResendSeconds}, err
}

// MarkRegistrationEmailDelivered 只激活本次 SMTP 已接受的验证码；迟到的发送不能覆盖新码。
func MarkRegistrationEmailDelivered(email, code string) error {
	email = NormalizeEmail(email)
	return DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&AuthFlow{}).Where("token_hash = ? AND purpose = ?", authFlowTokenHash("registration-email:"+email), registrationCodePurpose).Session(&gorm.Session{})
		if err := query.UpdateColumn("payload", gorm.Expr("payload")).Error; err != nil {
			return err
		}
		var flow AuthFlow
		if err := query.First(&flow).Error; err != nil {
			return err
		}
		var state registrationCodeState
		if err := common.UnmarshalJsonStr(flow.Payload, &state); err != nil {
			return err
		}
		if state.Hash != authFlowTokenHash("registration-code:"+email+":"+code) || !flow.ExpiresAt.After(time.Now()) || flow.ConsumedAt != nil {
			return ErrRegistrationCode
		}
		state.Ready = true
		payload, err := common.Marshal(state)
		if err != nil {
			return err
		}
		return query.Update("payload", string(payload)).Error
	})
}

// ReserveRegistrationAttempt 在密码计算前限制注册尝试，错误验证码和无效表单也消耗预算。
func ReserveRegistrationAttempt(ip string) error {
	policy := CurrentRegistrationAbusePolicy()
	hour := time.Now().UTC().Truncate(time.Hour)
	return DB.Transaction(func(tx *gorm.DB) error {
		return reserveRegistrationLimitsWithTx(tx, []registrationLimit{{Key: "attempt:" + RegistrationNetwork(ip), Start: hour, End: hour.Add(time.Hour), Max: policy.RegistrationIPHourly}})
	})
}

// RegisterUserWithEmail 将验证码单次消费、每出口每日成功注册限额和用户创建放进同一事务。
// 错码计数必须提交；用户创建失败则回滚消费，允许在限制内纠正用户名后重试。
func RegisterUserWithEmail(user *User, code, ip string) error {
	policy := CurrentRegistrationAbusePolicy()
	var verificationErr error
	err := DB.Transaction(func(tx *gorm.DB) error {
		day := time.Now().UTC().Truncate(24 * time.Hour)
		query := tx.Model(&AuthFlow{}).Where("token_hash = ? AND purpose = ?", authFlowTokenHash("registration-email:"+NormalizeEmail(user.Email)), registrationCodePurpose).Session(&gorm.Session{})
		if err := query.UpdateColumn("payload", gorm.Expr("payload")).Error; err != nil {
			return err
		}
		var flow AuthFlow
		if err := query.First(&flow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRegistrationCode
			}
			return err
		}
		var state registrationCodeState
		if err := common.UnmarshalJsonStr(flow.Payload, &state); err != nil {
			return err
		}
		if !state.Ready || state.Locked || state.Attempts >= registrationAttemptLimit(state, policy) || flow.ConsumedAt != nil || !flow.ExpiresAt.After(time.Now()) {
			return ErrRegistrationCode
		}
		expected := authFlowTokenHash("registration-code:" + NormalizeEmail(user.Email) + ":" + code)
		if subtle.ConstantTimeCompare([]byte(state.Hash), []byte(expected)) != 1 {
			state.Attempts++
			// 收紧后触发锁定必须落库，后续放宽设置或重发都不能恢复猜码机会。
			state.MaxAttempts = registrationAttemptLimit(state, policy)
			state.Locked = state.Attempts >= state.MaxAttempts
			payload, err := common.Marshal(state)
			if err != nil {
				return err
			}
			verificationErr = ErrRegistrationCode
			return query.Update("payload", string(payload)).Error
		}
		if err := query.Update("consumed_at", time.Now()).Error; err != nil {
			return err
		}
		if err := reserveRegistrationLimitsWithTx(tx, []registrationLimit{{Key: "success:" + RegistrationNetwork(ip), Start: day, End: day.AddDate(0, 0, 1), Max: policy.RegistrationIPDaily}}); err != nil {
			return err
		}
		return user.InsertWithTx(tx, user.InviterId)
	})
	if err != nil {
		return err
	}
	if verificationErr != nil {
		return verificationErr
	}
	user.FinishInsert(user.InviterId)
	return nil
}
