package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrAdminTokenForbidden = errors.New("You do not have permission to manage this user's API keys")
var ErrAdminTokenUnusable = errors.New("An expired or exhausted API key cannot be enabled")

// AdminTokenItem 是管理员列表的最小投影；Key 只能装掩码，完整密钥通过单独的验证接口读取。
type AdminTokenItem struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Key           string `json:"key"`
	Status        int    `json:"status"`
	HostingSeatID string `json:"hosting_seat_id"`
	CreatedTime   int64  `json:"created_time"`
	AccessedTime  int64  `json:"accessed_time"`
	ExpiredTime   int64  `json:"expired_time"`
}

// authorizeAdminTokens 每次从数据库核对管理员及目标用户，避免角色缓存滞后导致越权。
// 禁用用户的密钥仍允许管理员查看和停用；普通管理员只能管理自己和低权限用户。
func authorizeAdminTokens(tx *gorm.DB, actorID, targetID int, lock bool) error {
	query := tx
	if lock {
		query = lockForUpdate(tx)
	}
	var users []User
	if err := query.Select("id", "role", "status").Where("id IN ?", []int{actorID, targetID}).Order("id").Find(&users).Error; err != nil {
		return err
	}
	var actor, target *User
	for i := range users {
		if users[i].Id == actorID {
			actor = &users[i]
		}
		if users[i].Id == targetID {
			target = &users[i]
		}
	}
	if actor == nil || target == nil || actor.Status != common.UserStatusEnabled || actor.Role < common.RoleAdminUser {
		return ErrAdminTokenForbidden
	}
	if actor.Role != common.RoleRootUser && actorID != targetID && actor.Role <= target.Role {
		return ErrAdminTokenForbidden
	}
	return nil
}

// AdminUserTokenCounts 批量返回可管理用户的密钥数；无权访问的用户不出现在结果中。
func AdminUserTokenCounts(actorID int, ids []int) (map[int]int64, error) {
	var actor User
	if err := DB.Select("id", "role", "status").First(&actor, actorID).Error; err != nil {
		return nil, err
	}
	if actor.Role < common.RoleAdminUser || actor.Status != common.UserStatusEnabled {
		return nil, ErrAdminTokenForbidden
	}
	query := DB.Model(&User{}).Where("id IN ?", ids)
	if actor.Role != common.RoleRootUser {
		query = query.Where("id = ? OR role < ?", actorID, actor.Role)
	}
	var allowed []int
	if err := query.Pluck("id", &allowed).Error; err != nil {
		return nil, err
	}
	result := make(map[int]int64, len(allowed))
	for _, id := range allowed {
		result[id] = 0
	}
	if len(allowed) == 0 {
		return result, nil
	}
	var counts []struct {
		UserID int
		Count  int64
	}
	if err := DB.Model(&Token{}).Select("user_id, COUNT(*) AS count").Where("user_id IN ?", allowed).Group("user_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	for _, count := range counts {
		result[count.UserID] = count.Count
	}
	return result, nil
}

// ListAdminUserTokens 只按用户名下的密钥名称搜索，完整凭据永不参与查询参数或响应。
func ListAdminUserTokens(actorID, userID, offset, limit, status int, keyword string) ([]AdminTokenItem, int64, error) {
	if err := authorizeAdminTokens(DB, actorID, userID, false); err != nil {
		return nil, 0, err
	}
	query := DB.Model(&Token{}).Where("user_id = ?", userID)
	if status != 0 {
		query = query.Where("status = ?", status)
	}
	if keyword != "" {
		// 使用显式 ESCAPE，三种数据库中百分号和下划线都按普通文字处理。
		pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(keyword)
		query = query.Where("name LIKE ? ESCAPE '!'", "%"+pattern+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var tokens []Token
	if err := query.Order("id DESC").Offset(offset).Limit(limit).Find(&tokens).Error; err != nil {
		return nil, 0, err
	}
	items := make([]AdminTokenItem, 0, len(tokens))
	for _, token := range tokens {
		items = append(items, AdminTokenItem{ID: token.Id, Name: token.Name, Key: token.GetMaskedKey(), Status: token.Status, HostingSeatID: token.HostingSeatID, CreatedTime: token.CreatedTime, AccessedTime: token.AccessedTime, ExpiredTime: token.ExpiredTime})
	}
	return items, total, nil
}

// AdminUserTokenOperation 在消费一次性证明后执行；事务中重新锁定会话、权限和所属密钥。
// status=0 只读取，1/2 只更新状态，绝不把旧快照的绑定、余额或最后使用时间覆盖回数据库。
func AdminUserTokenOperation(identity AuthSessionIdentity, userID, tokenID, status int) (string, error) {
	var key string
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, pat := ParseAccessTokenSessionID(identity.SessionID); pat {
			return ErrAdminTokenForbidden
		}
		if err := ValidateAuthSessionWithTx(tx, identity); err != nil {
			return err
		}
		if err := authorizeAdminTokens(tx, identity.UserID, userID, true); err != nil {
			return err
		}
		var token Token
		if err := lockForUpdate(tx).Where("id = ? AND user_id = ?", tokenID, userID).First(&token).Error; err != nil {
			return err
		}
		if status == 0 {
			key = token.GetFullKey()
			return nil
		}
		if status != common.TokenStatusEnabled && status != common.TokenStatusDisabled {
			return ErrAdminTokenForbidden
		}
		if status == common.TokenStatusEnabled && ((token.ExpiredTime != -1 && token.ExpiredTime <= common.GetTimestamp()) || (!token.UnlimitedQuota && token.RemainQuota <= 0)) {
			return ErrAdminTokenUnusable
		}
		if err := invalidateTokenCacheForMutation(token.Key); err != nil {
			return err
		}
		return tx.Model(&token).Update("status", status).Error
	})
	return key, err
}

// CheckAdminUserToken 在消耗验证证明前验证目标，错误信息不暴露其他用户的密钥是否存在。
func CheckAdminUserToken(actorID, userID, tokenID int) error {
	if err := authorizeAdminTokens(DB, actorID, userID, false); err != nil {
		return err
	}
	var count int64
	if err := DB.Model(&Token{}).Where("id = ? AND user_id = ?", tokenID, userID).Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrAdminTokenForbidden
	}
	return nil
}
