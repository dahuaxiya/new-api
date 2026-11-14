package common

import (
	"fmt"
	"os"
)

// SiteMode 返回进程所属站点；未配置的既有部署继续作为托管站运行。
// 模式只来自部署环境，不允许通过用户设置或请求参数切换。
func SiteMode() string {
	if mode := os.Getenv("SITE_MODE"); mode != "" {
		return mode
	}
	return "hosting"
}

// IsRetailSite 判断当前进程是否为独立中转站，供接口和后台任务共用业务边界。
func IsRetailSite() bool { return SiteMode() == "retail" }

// ValidateSiteMode 在连接数据库前拒绝拼写错误及两种业务配置混用。
// 两站必须由部署层提供独立数据库、会话密钥和入口；同 IP 不同端口使用不同 Cookie 名称，不能靠此开关迁移旧数据。
func ValidateSiteMode() error {
	if SiteMode() != "hosting" && !IsRetailSite() {
		return fmt.Errorf("SITE_MODE must be hosting or retail")
	}
	if IsRetailSite() {
		for _, name := range []string{"GPT_QUOTA_CONFIG", "VM2API_INSTANCES_CONFIG", "ALIPAY_HOSTING_CONFIG"} {
			if os.Getenv(name) != "" {
				return fmt.Errorf("%s is not allowed on a retail site", name)
			}
		}
		if os.Getenv("GPT_HOSTING_ENABLED") == "true" {
			return fmt.Errorf("GPT_HOSTING_ENABLED must be false on a retail site")
		}
	} else if os.Getenv("RETAIL_RELAY_CONFIG") != "" || os.Getenv("ALIPAY_WALLET_CONFIG") != "" {
		return fmt.Errorf("retail supply and wallet payment require SITE_MODE=retail")
	}
	return ValidateSiteSSO()
}

// SiteAllowsToken 在实际请求时校验 Key 类型；旧库误挂到中转站也不能按旧规则消费。
func SiteAllowsToken(serviceMode, hostingSeatID string) bool {
	if IsRetailSite() {
		return serviceMode == "retail" && hostingSeatID == ""
	}
	return SiteMode() == "hosting" && (serviceMode == "" || serviceMode == "hosting")
}
