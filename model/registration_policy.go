package model

import (
	"errors"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

const RegistrationAbusePolicyKey = "RegistrationAbusePolicy"

// registrationPolicyOptionMutex 串行化设置写入和定时回读，避免旧快照覆盖刚保存的值。
var registrationPolicyOptionMutex sync.Mutex

// RegistrationAbusePolicy 作为单个 JSON 选项持久化，所有字段必须一起校验和生效。
// 数值只控制阈值；验证码单次消费和已累计的防刷记录不能通过配置关闭或清零。
type RegistrationAbusePolicy struct {
	CodeTTLMinutes       int `json:"code_ttl_minutes"`
	ResendSeconds        int `json:"resend_seconds"`
	CodeMaxAttempts      int `json:"code_max_attempts"`
	EmailDaily           int `json:"email_daily"`
	IPEmailHourly        int `json:"ip_email_hourly"`
	IPEmailDaily         int `json:"ip_email_daily"`
	RegistrationIPHourly int `json:"registration_ip_hourly"`
	RegistrationIPDaily  int `json:"registration_ip_daily"`
	EmailGlobalDaily     int `json:"email_global_daily"`
	EmailGlobalMonthly   int `json:"email_global_monthly"`
}

// DefaultRegistrationAbusePolicy 返回升级前的限额副本，未配置时继续执行原保护。
func DefaultRegistrationAbusePolicy() RegistrationAbusePolicy {
	return RegistrationAbusePolicy{
		CodeTTLMinutes: 10, ResendSeconds: 60, CodeMaxAttempts: 5,
		EmailDaily: 5, IPEmailHourly: 10, IPEmailDaily: 30,
		RegistrationIPHourly: 20, RegistrationIPDaily: 3,
		EmailGlobalDaily: 80, EmailGlobalMonthly: 2500,
	}
}

// ParseRegistrationAbusePolicy 拒绝缺失、零值、负值、分数和超界配置；失败时不得部分保存。
func ParseRegistrationAbusePolicy(value string) (RegistrationAbusePolicy, error) {
	var policy RegistrationAbusePolicy
	if err := common.UnmarshalJsonStr(value, &policy); err != nil {
		return policy, errors.New("Invalid registration and email abuse settings")
	}
	limits := []struct{ value, minimum, maximum int }{
		{policy.CodeTTLMinutes, 1, 30}, {policy.ResendSeconds, 30, 600}, {policy.CodeMaxAttempts, 1, 10},
		{policy.EmailDaily, 1, 1000}, {policy.IPEmailHourly, 1, 10000}, {policy.IPEmailDaily, 1, 100000},
		{policy.RegistrationIPHourly, 1, 10000}, {policy.RegistrationIPDaily, 1, 10000},
		{policy.EmailGlobalDaily, 1, 1000000}, {policy.EmailGlobalMonthly, 1, 31000000},
	}
	for _, limit := range limits {
		if limit.value < limit.minimum || limit.value > limit.maximum {
			return policy, errors.New("Registration and email abuse limits are outside the allowed range")
		}
	}
	if policy.ResendSeconds > policy.CodeTTLMinutes*60 || policy.IPEmailHourly > policy.IPEmailDaily || policy.IPEmailDaily > policy.EmailGlobalDaily || policy.EmailGlobalDaily > policy.EmailGlobalMonthly {
		return policy, errors.New("Check the resend interval and hourly, daily, and monthly email limits")
	}
	return policy, nil
}

// CurrentRegistrationAbusePolicy 在锁内只复制完整 JSON，解析后返回独立值，单个请求不混用两个版本。
// 缺失或损坏的旧配置使用安全默认值，不能把解析失败当作无限制。
func CurrentRegistrationAbusePolicy() RegistrationAbusePolicy {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[RegistrationAbusePolicyKey]
	common.OptionMapRWMutex.RUnlock()
	policy, err := ParseRegistrationAbusePolicy(value)
	if err != nil {
		return DefaultRegistrationAbusePolicy()
	}
	return policy
}
