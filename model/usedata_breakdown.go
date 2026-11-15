package model

import "github.com/QuantumNous/new-api/common"

// UserQuotaBreakdown 仅暴露个人用量的时间、密钥标识、模型及分组；不携带密钥值或供货渠道。
type UserQuotaBreakdown struct {
	CreatedAt int64  `json:"created_at"`
	TokenID   int    `json:"token_id"`
	TokenName string `json:"token_name" gorm:"-"`
	ModelName string `json:"model_name"`
	UseGroup  string `json:"use_group"`
	TokenUsed int64  `json:"token_used"`
	Count     int64  `json:"count"`
	Quota     int64  `json:"quota"`
}

// GetUserQuotaBreakdown 按小时及个人维度汇总，调用方必须校验时间范围并传入会话用户 ID。
// 历史记录未包含密钥或分组时保留零值，不能遗漏历史费用或猜测其归属。
func GetUserQuotaBreakdown(userID int, startTime, endTime int64) ([]UserQuotaBreakdown, error) {
	rows := make([]UserQuotaBreakdown, 0)
	if userID <= 0 {
		return rows, nil
	}
	err := DB.Table("quota_data").
		Select("created_at, token_id, model_name, use_group, sum(token_used) as token_used, sum(count) as count, sum(quota) as quota").
		Where("user_id = ? AND created_at >= ? AND created_at <= ?", userID, startTime, endTime).
		Group("created_at, token_id, model_name, use_group").
		Order("created_at, token_id, model_name, use_group").
		Limit(10001).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	// 拒绝返回截断的财务统计，避免合计看似成功但少算费用；用户可缩短区间再查。
	if len(rows) > 10000 {
		return nil, common.NewMessage("Too many usage groups. Select a shorter time range.")
	}
	ids := make([]int, 0)
	seen := make(map[int]bool)
	for _, row := range rows {
		if row.TokenID > 0 && !seen[row.TokenID] {
			ids = append(ids, row.TokenID)
			seen[row.TokenID] = true
		}
	}
	if len(ids) == 0 {
		return rows, nil
	}
	var tokens []struct {
		Id   int
		Name string
	}
	// 名称查询再次绑定用户归属且只取 id/name；删除或归属不符的密钥保留空名供前端标注。
	if err := DB.Model(&Token{}).Select("id, name").Where("user_id = ? AND id IN ?", userID, ids).Find(&tokens).Error; err != nil {
		return nil, err
	}
	names := make(map[int]string, len(tokens))
	for _, token := range tokens {
		names[token.Id] = token.Name
	}
	for i := range rows {
		rows[i].TokenName = names[rows[i].TokenID]
	}
	return rows, nil
}
