package model

import (
	"errors"
	"os"
	"strconv"

	"gorm.io/gorm"
)

// TableName 将多名额存入新表，保留旧表作为只读迁移来源，避免跨数据库修改旧主键。
func (GPTHostingGrant) TableName() string { return "account_hosting_grants" }

// TableName 以名额为主键保存账号状态；用户 ID 仅表示归属。
func (GPTHostingBinding) TableName() string { return "account_hosting_bindings" }

// defaultHostingSeat 为旧接口和历史记录保留稳定首名额，不随新增、排序或到期改变。
func defaultHostingSeat(userID int) string { return "legacy-" + strconv.Itoa(userID) }

// hostingSeatScope 每次读写同时限制用户和名额；未传名额只定位首名额，绝不能批量更新该用户。
func hostingSeatScope(db *gorm.DB, userID int, seats []string) *gorm.DB {
	id := defaultHostingSeat(userID)
	if len(seats) > 0 && seats[0] != "" {
		id = seats[0]
	}
	return db.Where("user_id = ? AND seat_id = ?", userID, id)
}

// BeforeCreate 为兼容旧调用补齐首名额 ID；新增名额必须由服务端显式生成 UUID。
func (g *GPTHostingGrant) BeforeCreate(tx *gorm.DB) error {
	if g.SeatID == "" {
		g.SeatID = defaultHostingSeat(g.UserID)
	}
	return nil
}

// BeforeCreate 保持旧绑定写入与首名额一致，平台缺省沿用历史 GPT 行为。
func (b *GPTHostingBinding) BeforeCreate(tx *gorm.DB) error {
	if b.SeatID == "" {
		b.SeatID = defaultHostingSeat(b.UserID)
	}
	if b.Platform == "" {
		b.Platform = "codex"
	}
	return nil
}

// MigrateHostingSeats 只追加旧记录，重复启动不覆盖后来修改的期限、Key 选择或绑定状态。
// 原表不再参与业务读写；迁移在启动提供流量前整体提交，任何冲突都会阻止启动。
func MigrateHostingSeats(db *gorm.DB) error {
	if !db.Migrator().HasTable("gpt_hosting_grants") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var grants []GPTHostingGrant
		if err := tx.Table("gpt_hosting_grants").Find(&grants).Error; err != nil {
			return err
		}
		for _, grant := range grants {
			grant.SeatID = defaultHostingSeat(grant.UserID)
			var existing GPTHostingGrant
			err := tx.First(&existing, "seat_id = ?", grant.SeatID).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var binding GPTHostingBinding
			if err := tx.Table("gpt_hosting_bindings").Take(&binding, "user_id = ?", grant.UserID).Error; err != nil {
				return err
			}
			binding.SeatID, binding.Platform = grant.SeatID, "codex"
			if err := tx.Create(&grant).Error; err != nil {
				return err
			}
			if err := tx.Create(&binding).Error; err != nil {
				return err
			}
			if err := tx.Model(&Token{}).Where("user_id = ? AND (hosting_seat_id = ? OR hosting_seat_id IS NULL)", grant.UserID, "").Update("hosting_seat_id", grant.SeatID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListHostingSeats 只列出指定用户的名额，顺序稳定；停用和到期名额保留供续期和审计。
func ListHostingSeats(userID int) ([]GPTHostingGrant, error) {
	rows := []GPTHostingGrant{}
	err := DB.Where("user_id = ?", userID).Order("created_at, seat_id").Find(&rows).Error
	return rows, err
}

// SelectTokenHostingSeat 在保存 Key 前验证归属。单名额旧客户端可省略，多名额必须明确选择。
func SelectTokenHostingSeat(userID int, seatID string) (string, error) {
	// 未启用托管的部署可能没有托管表；普通 Key 保持原行为，显式目标则不能静默忽略。
	if os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		if seatID != "" {
			return "", ErrHostingConflict
		}
		return "", nil
	}
	if seatID != "" {
		_, err := GetHostingGrant(userID, seatID)
		if err != nil {
			return "", ErrHostingConflict
		}
		return seatID, nil
	}
	seats, err := ListHostingSeats(userID)
	if err != nil {
		return "", err
	}
	if len(seats) == 0 {
		return "", nil
	}
	if len(seats) != 1 {
		return "", ErrHostingConflict
	}
	return seats[0].SeatID, nil
}

// GetTokenHostingRoute 每次从数据库读取 Key 指向，避免缓存或 WebSocket 长连接继续使用旧账号。
// 显式指向已不存在的名额时拒绝访问，不能回退到公共组或另一个账号。
func GetTokenHostingRoute(userID, tokenID int) (string, int, error) {
	if os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		return "", 0, nil
	}
	if tokenID <= 0 {
		return GetHostingRoute(userID)
	}
	var token Token
	if err := DB.Select("id", "user_id", "hosting_seat_id").First(&token, "id = ? AND user_id = ?", tokenID, userID).Error; err != nil {
		return "", 0, ErrHostingUnavailable
	}
	return GetHostingRoute(userID, token.HostingSeatID)
}

// reserveSeatPlatform 在仍未绑定时确定本次账号类型；已占槽的名额不能切换到另一平台。
func reserveSeatPlatform(tx *gorm.DB, userID int, seatID, platform string) error {
	if platform != "codex" && platform != "claude" {
		return ErrHostingConflict
	}
	var binding GPTHostingBinding
	if err := hostingSeatScope(lockForUpdate(tx), userID, []string{seatID}).First(&binding).Error; err != nil {
		return err
	}
	if binding.State != "unbound" && binding.Platform != platform {
		return ErrHostingConflict
	}
	return tx.Model(&binding).Update("platform", platform).Error
}
