package model

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// HostingTakeover 保存 root 已确认的历史独占绑定及来源，不把历史标识当成新的官方登录证明。
type HostingTakeover struct {
	IdentitySource                                                                     string
	InstanceID, SlotID, Snapshot, AccountHash, SubjectHash, Label, Plan, KeyID, PoolID string
	ChannelID                                                                          int
}

// hostingChannelFingerprint 用完整渠道的哈希检测预检后的配置变化；原始 Key 不进入迁移记录或日志。
func hostingChannelFingerprint(ch Channel) string {
	// 测速、消耗累计和余额采集不改变路由，不能让正常后台采集永久卡住接管。
	ch.TestTime, ch.ResponseTime, ch.UsedQuota, ch.BalanceUpdatedTime = 0, 0, 0, 0
	ch.Balance = 0
	raw, _ := common.Marshal(ch)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// legacyHostingChannel 仅允许一个普通用户独占一个渠道和分组；共享或复杂路由必须先人工拆分。
func legacyHostingChannel(tx *gorm.DB, userID int) (*Channel, string, error) {
	var user User
	if err := lockForUpdate(tx).First(&user, userID).Error; err != nil {
		return nil, "", err
	}
	if user.Role != common.RoleCommonUser || user.Status != common.UserStatusEnabled || user.Group == "" || user.Group == "default" {
		return nil, "", ErrHostingConflict
	}
	var users int64
	if err := tx.Model(&User{}).Where(map[string]any{"group": user.Group}).Count(&users).Error; err != nil {
		return nil, "", err
	}
	if users != 1 {
		return nil, "", ErrHostingConflict
	}
	var channels []Channel
	if err := tx.Find(&channels).Error; err != nil {
		return nil, "", err
	}
	var target *Channel
	for i := range channels {
		ch := &channels[i]
		for group := range strings.SplitSeq(ch.Group, ",") {
			if group != user.Group {
				continue
			}
			if target != nil || ch.Group != user.Group || ch.Type != 1 || ch.Status != common.ChannelStatusEnabled || ch.Key == "" || strings.Contains(ch.Key, "\n") || ch.BaseURL == nil {
				return nil, "", ErrHostingConflict
			}
			target = ch
		}
	}
	if target == nil {
		return nil, "", ErrHostingConflict
	}
	var tokens []Token
	if err := tx.Where("user_id <> ?", userID).Find(&tokens).Error; err != nil {
		return nil, "", err
	}
	for _, token := range tokens {
		// 自动分组选项采用保守包含检查，宁可要求拆分，也不能撤销其他客户正在使用的路由。
		if token.Group == user.Group || strings.Contains(token.AutoGroups, user.Group) {
			return nil, "", ErrHostingConflict
		}
	}
	return target, hostingChannelFingerprint(*target), nil
}

// LegacyHostingChannel 只返回给受信控制器，禁止将包含渠道 Key 的结果序列化到浏览器。
func LegacyHostingChannel(userID int) (*Channel, string, error) {
	return legacyHostingChannel(DB.Session(&gorm.Session{Logger: logger.Discard}), userID)
}

// BeginHostingTakeover 在远端变更前持久化管理员确认的历史归属与配置快照；失败重试沿用同一绑定。
func BeginHostingTakeover(grant GPTHostingGrant, takeover HostingTakeover) (*GPTHostingGrant, error) {
	if takeover.IdentitySource != "legacy_binding" || !validVM2APIIdentifier(takeover.InstanceID, 64) || !validVM2APIIdentifier(takeover.SlotID, 80) || len(takeover.AccountHash) != 64 || len(takeover.SubjectHash) != 64 || len(takeover.Snapshot) != 64 || takeover.KeyID == "" {
		return nil, ErrHostingConflict
	}
	return putHostingGrant(grant, &takeover)
}

// HostingUserFilter 的旧配置 ID 仅由服务器生成，不接受浏览器传入任意归属。
type HostingUserFilter struct {
	Status        string
	LegacyUserIDs []int
}

// Apply 使用各数据库共有的子查询，先过滤后分页；不远程探测账号，也不加载全部用户。
func (f HostingUserFilter) Apply(query, tx *gorm.DB) (*gorm.DB, error) {
	grants := tx.Model(&GPTHostingGrant{}).Select("user_id")
	legacy := append([]int{0}, f.LegacyUserIDs...)
	jobs := tx.Model(&VM2APIEnrollment{}).Select("user_id").Where("status = ?", VM2APIEnrollmentCompleted)
	switch f.Status {
	case "undated":
		return query.Where("id NOT IN (?)", grants).Where("(id IN ? OR id IN (?))", legacy, jobs), nil
	case "unopened":
		return query.Where("role = ?", common.RoleCommonUser).Where("id NOT IN (?)", grants).Where("id NOT IN ? AND id NOT IN (?)", legacy, jobs), nil
	}
	now := time.Now().Unix()
	bound := tx.Table("account_hosting_grants AS g").Select("g.user_id").Joins("JOIN account_hosting_bindings AS b ON b.seat_id = g.seat_id AND b.user_id = g.user_id")
	if f.Status == "takeover" {
		return query.Where("id IN (?)", bound.Where("b.state = ?", "takeover")), nil
	}
	bound = bound.Where("b.state <> ?", "takeover")
	if f.Status == "disabled" {
		return query.Where("id IN (?)", bound.Where("g.enabled = ? OR b.paused = ?", false, true)), nil
	}
	bound = bound.Where("g.enabled = ? AND b.paused = ?", true, false)
	switch f.Status {
	case "expired":
		bound = bound.Where("g.ends_at <> ? AND g.ends_at <= ?", HostingPermanentEnd, now)
	case "scheduled":
		bound = bound.Where("(g.ends_at = ? OR g.ends_at > ?) AND g.starts_at > ?", HostingPermanentEnd, now, now)
	case "awaiting_login":
		bound = bound.Where("(g.ends_at = ? OR g.ends_at > ?) AND g.starts_at <= ? AND b.state <> ?", HostingPermanentEnd, now, now, "active")
	case "expiring":
		bound = bound.Where("g.ends_at > ? AND g.ends_at <= ? AND g.starts_at <= ? AND b.state = ?", now, now+7*86400, now, "active")
	case "active":
		bound = bound.Where("(g.ends_at = ? OR g.ends_at > ?) AND g.starts_at <= ? AND b.state = ?", HostingPermanentEnd, now+7*86400, now, "active")
	default:
		return nil, ErrHostingConflict
	}
	return query.Where("id IN (?)", bound), nil
}
