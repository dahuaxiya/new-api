package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/retail_setting"
)

// RetailAttempt 每次实际上游尝试独立记录，失败也保留未知成本；未知值不能按零利润计算。
type RetailAttempt struct {
	ID             int    `json:"id"`
	RequestID      string `json:"request_id" gorm:"type:varchar(80);index"`
	ChannelID      int    `json:"channel_id" gorm:"index"`
	SupplyKind     string `json:"supply_kind" gorm:"type:varchar(24)"`
	State          string `json:"state" gorm:"type:varchar(24)"`
	StatusCode     int    `json:"status_code"`
	CostState      string `json:"cost_state" gorm:"type:varchar(24)"`
	Usage          string `json:"usage" gorm:"type:text"`
	SupplySnapshot string `json:"-" gorm:"type:text"`
	CreatedAt      int64  `json:"created_at"`
	CompletedAt    int64  `json:"completed_at"`
}

// BeginRetailAttempt 必须在网络调用前持久化供货归属，失败时调用方不得发送请求。
func BeginRetailAttempt(requestID string, channelID int) (int, error) {
	cfg, err := RetailSupplyConfig(DB)
	if err != nil || !cfg.Enabled {
		return 0, retail_setting.ErrUnavailable
	}
	for _, supply := range cfg.Supplies {
		if supply.ChannelID != channelID {
			continue
		}
		snapshot, err := common.Marshal(supply)
		if err != nil {
			return 0, err
		}
		attempt := RetailAttempt{RequestID: requestID, ChannelID: channelID, SupplyKind: supply.Kind, State: "started", CostState: "unknown", SupplySnapshot: string(snapshot), CreatedAt: time.Now().Unix()}
		if err = DB.Create(&attempt).Error; err != nil {
			return 0, err
		}
		return attempt.ID, nil
	}
	return 0, retail_setting.ErrUnavailable
}

// FinishRetailAttempt 只更新结果和已知用量；成功响应也不等同于供应商采购账单已确认。
func FinishRetailAttempt(id, status int, state string) error {
	return DB.Model(&RetailAttempt{}).Where("id = ?", id).Updates(map[string]any{"status_code": status, "state": state, "completed_at": time.Now().Unix()}).Error
}

// RecordRetailAttemptUsage 保存协议用量，内容只允许结构化 usage，不保存提示词和响应正文。
func RecordRetailAttemptUsage(id int, usage any) error {
	raw, err := common.Marshal(usage)
	if err != nil {
		return err
	}
	return DB.Model(&RetailAttempt{}).Where("id = ?", id).Update("usage", string(raw)).Error
}
