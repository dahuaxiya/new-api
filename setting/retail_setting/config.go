// Package retail_setting 定义商业供货白名单；客户托管渠道不会因为同机部署而自动成为供货。
package retail_setting

import (
	"os"
	"path/filepath"
	"regexp"

	"github.com/QuantumNous/new-api/common"
)

var ErrUnavailable = common.NewMessage("Retail API is unavailable")
var groupName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Supply 是站长显式登记的供货渠道，类型用于采购成本归类，不代表账号已通过实测。
type Supply struct {
	ChannelID int    `json:"channel_id"`
	Kind      string `json:"kind"` // subscription 或 reseller
}

type Config struct {
	Enabled  bool     `json:"enabled"`
	Group    string   `json:"group"`
	Supplies []Supply `json:"supplies"`
}

// Load 每次读取小型本地配置，使停止销售立即生效；路径只来自部署环境，不能由请求指定。
// 未配置默认关闭，损坏配置拒绝服务，不回退到客户专属组。
func Load() (Config, error) {
	path := os.Getenv("RETAIL_RELAY_CONFIG")
	if path == "" {
		return Config{}, nil
	}
	if !filepath.IsAbs(path) {
		return Config{}, ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return Config{}, ErrUnavailable
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return Config{}, ErrUnavailable
	}
	var cfg Config
	if common.Unmarshal(raw, &cfg) != nil {
		return Config{}, ErrUnavailable
	}
	if len(cfg.Supplies) > 100 {
		return Config{}, ErrUnavailable
	}
	if cfg.Enabled && (!groupName.MatchString(cfg.Group) || cfg.Group == "auto" || len(cfg.Group) >= 5 && cfg.Group[:5] == "acct_" || len(cfg.Supplies) == 0) {
		return Config{}, ErrUnavailable
	}
	// 停售时供货仍需隔离，不能跳过条目校验，否则拼错 ID 会重新暴露给旧 Key。
	seen := map[int]bool{}
	for _, supply := range cfg.Supplies {
		if supply.ChannelID <= 0 || seen[supply.ChannelID] || (supply.Kind != "subscription" && supply.Kind != "reseller") {
			return Config{}, ErrUnavailable
		}
		seen[supply.ChannelID] = true
	}
	return cfg, nil
}
