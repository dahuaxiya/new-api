package model

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrHostingSoldOut = errors.New("hosting slots are sold out")

// HostingPlan 用人民币分保存价格；订单保存快照，改价不会改动已下单金额或期限。
type HostingPlan struct {
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	Days       int    `json:"days"`
	PriceCents int64  `json:"price_cents"`
	Enabled    bool   `json:"enabled"`
	UpdatedAt  int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// HostingOrder 只记录支付与权益交付证据，不保存网关签名、商户密钥和原始回调。
// InventoryID 是有时限的库存占用，已付款后由名额绑定继续独占该槽位。
type HostingOrder struct {
	ID            string  `json:"id" gorm:"type:varchar(40);primaryKey"`
	UserID        int     `json:"user_id" gorm:"index"`
	RequestID     string  `json:"-" gorm:"type:varchar(36);uniqueIndex:idx_hosting_order_request,priority:2"`
	RequestUser   int     `json:"-" gorm:"uniqueIndex:idx_hosting_order_request,priority:1"`
	Kind          string  `json:"kind" gorm:"type:varchar(12)"`
	SeatID        string  `json:"seat_id" gorm:"type:varchar(36);index"`
	PlanID        string  `json:"plan_id" gorm:"type:varchar(36)"`
	Days          int     `json:"days"`
	PriceCents    int64   `json:"price_cents"`
	Platform      string  `json:"platform" gorm:"type:varchar(16)"`
	Method        string  `json:"method" gorm:"type:varchar(12)"`
	Merchant      string  `json:"-" gorm:"type:varchar(128)"`
	ProviderTrade *string `json:"-" gorm:"type:varchar(128);uniqueIndex:idx_hosting_provider_trade"`
	InventoryID   string  `json:"-" gorm:"type:varchar(36);index"`
	Status        string  `json:"status" gorm:"type:varchar(24);index"`
	CreatedAt     int64   `json:"created_at" gorm:"autoCreateTime"`
	ExpiresAt     int64   `json:"expires_at"`
	PaidAt        int64   `json:"paid_at"`
	EndsAt        int64   `json:"ends_at"`
}

// MigrateHostingStore 仅初始化缺失套餐，重启和升级不覆盖管理员的价格或上下架状态。
func MigrateHostingStore(db *gorm.DB) error {
	for _, plan := range []HostingPlan{{ID: "monthly", Days: 30, PriceCents: 4000}, {ID: "quarterly", Days: 90, PriceCents: 9900}, {ID: "yearly", Days: 365, PriceCents: 35000}} {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&plan).Error; err != nil {
			return err
		}
	}
	return nil
}

// ListHostingPlans 返回三档套餐；下架项仍可显示价格，但后端不允许创建订单。
func ListHostingPlans() ([]HostingPlan, error) {
	rows := []HostingPlan{}
	err := DB.Order("days, id").Find(&rows).Error
	return rows, err
}

// SaveHostingPlan 仅允许修改已约定三档价格和开关，不让前端扩大期限或授予永久权益。
func SaveHostingPlan(plan HostingPlan) error {
	days := map[string]int{"monthly": 30, "quarterly": 90, "yearly": 365}
	if days[plan.ID] == 0 || plan.Days != days[plan.ID] || plan.PriceCents < 1 || plan.PriceCents > 10000000 {
		return ErrHostingConflict
	}
	r := DB.Model(&HostingPlan{}).Where("id = ?", plan.ID).Updates(map[string]any{"price_cents": plan.PriceCents, "enabled": plan.Enabled})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrHostingConflict
	}
	return nil
}

// checkHostingInventoryHold 必须在锁定库存行后调用；订单超时后不再占位，迟到到账进入人工核对。
func checkHostingInventoryHold(tx *gorm.DB, inventoryID, exceptOrder string, now int64) error {
	// 旧部署和部分不启用商城的独立测试可能尚未创建订单表，原分配路径保持兼容。
	if !tx.Migrator().HasTable(&HostingOrder{}) {
		return nil
	}
	var count int64
	err := tx.Model(&HostingOrder{}).Where("inventory_id = ? AND id <> ? AND status = ? AND expires_at > ?", inventoryID, exceptOrder, "pending", now).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 0 {
		return ErrHostingSoldOut
	}
	return nil
}

// hostingPurchaseInventory 在事务锁下确认库存仍可用；候选列表来自服务端实时远端检查。
func hostingPurchaseInventory(tx *gorm.DB, candidates []string, platform, exceptOrder string, now int64) (*GPTHostingInventory, error) {
	for _, id := range candidates {
		var inv GPTHostingInventory
		if err := lockForUpdate(tx).First(&inv, "id = ? AND enabled = ?", id, true).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if inv.Platform != platform && !(inv.Platform == "" && platform == "codex") {
			continue
		}
		var used int64
		if err := tx.Model(&GPTHostingBinding{}).Where("inventory_id = ?", id).Count(&used).Error; err != nil {
			return nil, err
		}
		if used != 0 {
			continue
		}
		if err := tx.Model(&VM2APIEnrollment{}).Where("instance_id = ? AND slot_id = ?", inv.InstanceID, inv.SlotID).Count(&used).Error; err != nil {
			return nil, err
		}
		if used != 0 {
			continue
		}
		if err := checkHostingInventoryHold(tx, id, exceptOrder, now); err != nil {
			if errors.Is(err, ErrHostingSoldOut) {
				continue
			}
			return nil, err
		}
		return &inv, nil
	}
	return nil, ErrHostingSoldOut
}

// CreateHostingOrder 锁用户后读取服务端套餐价格，按请求 UUID 幂等，并为新账号占位十五分钟。
// 每次只购买一个名额，续费必须属于本人；已有暂停、停用、清理中或永久名额不能误收续费款。
func CreateHostingOrder(userID int, requestID, planID, seatID, platform, method, merchant string, candidates []string) (*HostingOrder, error) {
	if _, err := uuid.Parse(requestID); err != nil {
		return nil, ErrHostingConflict
	}
	if userID <= 0 || (platform != "codex" && platform != "claude") || (method != "wxpay" && method != "alipay") || merchant == "" || len(merchant) > 128 {
		return nil, ErrHostingConflict
	}
	vm2apiEnrollmentCommitMutex.Lock()
	defer vm2apiEnrollmentCommitMutex.Unlock()
	var order HostingOrder
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		if user.Status != common.UserStatusEnabled || user.Role != common.RoleCommonUser {
			return ErrHostingConflict
		}
		err := tx.Where("request_user = ? AND request_id = ?", userID, requestID).First(&order).Error
		if err == nil {
			if order.PlanID != planID || order.Method != method || order.Merchant != merchant || order.Platform != platform || (seatID != "" && order.SeatID != seatID) || (seatID == "" && order.Kind != "new") {
				return ErrHostingConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().Unix()
		var pending int64
		if err := tx.Model(&HostingOrder{}).Where("user_id = ? AND status = ? AND expires_at > ?", userID, "pending", now).Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return ErrHostingConflict
		}
		var plan HostingPlan
		if err := tx.First(&plan, "id = ? AND enabled = ?", planID, true).Error; err != nil {
			return ErrHostingConflict
		}
		order = HostingOrder{ID: "HP" + strings.ReplaceAll(uuid.NewString(), "-", ""), UserID: userID, RequestUser: userID, RequestID: requestID, Kind: "new", SeatID: seatID, PlanID: plan.ID, Days: plan.Days, PriceCents: plan.PriceCents, Platform: platform, Method: method, Merchant: merchant, Status: "pending", CreatedAt: now, ExpiresAt: now + 900}
		needsSlot := true
		if seatID != "" {
			var g GPTHostingGrant
			var b GPTHostingBinding
			if err := hostingSeatScope(lockForUpdate(tx), userID, []string{seatID}).First(&g).Error; err != nil {
				return ErrHostingConflict
			}
			if err := hostingSeatScope(lockForUpdate(tx), userID, []string{seatID}).First(&b).Error; err != nil {
				return ErrHostingConflict
			}
			if !g.Enabled || g.EndsAt == HostingPermanentEnd || b.State == "cleaning" || b.State == "takeover" || b.Paused || b.OperationUntil > now {
				return ErrHostingConflict
			}
			if b.State != "unbound" && b.Platform != platform {
				return ErrHostingConflict
			}
			order.Kind = "renew"
			needsSlot = b.InventoryID == nil
		} else {
			var count int64
			if err := tx.Model(&GPTHostingGrant{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
				return err
			}
			if count >= 100 {
				return ErrHostingConflict
			}
			// 历史静态账号必须先由管理员接管，付款不能暗中覆盖原分组和 Key。
			if count == 0 {
				var legacy int64
				if err := tx.Model(&VM2APIEnrollment{}).Where("user_id = ?", userID).Count(&legacy).Error; err != nil {
					return err
				}
				if legacy != 0 || strings.HasPrefix(user.Group, "acct_") {
					return ErrHostingConflict
				}
			}
			order.SeatID = uuid.NewString()
		}
		if needsSlot {
			inv, err := hostingPurchaseInventory(tx, candidates, platform, "", now)
			if err != nil {
				return err
			}
			order.InventoryID = inv.ID
		}
		return tx.Create(&order).Error
	})
	return &order, err
}

// GetHostingOrder 读取本人订单，回调不使用此接口；不能仅凭订单号跨用户读取。
func GetHostingOrder(userID int, id string) (*HostingOrder, error) {
	var order HostingOrder
	err := DB.Where("id = ? AND user_id = ?", id, userID).First(&order).Error
	return &order, err
}

// ListHostingOrders 为用户或已鉴权管理员分页，超时状态由服务端时间推导，不依赖浏览器付款返回参数。
func ListHostingOrders(userID, page int) ([]HostingOrder, int64, error) {
	rows := []HostingOrder{}
	var total int64
	q := DB.Model(&HostingOrder{})
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("created_at desc, id desc").Offset((page - 1) * 20).Limit(20).Find(&rows).Error
	for i := range rows {
		if rows[i].Status == "pending" && rows[i].ExpiresAt <= time.Now().Unix() {
			rows[i].Status = "expired"
		}
	}
	return rows, total, err
}

// CompleteHostingOrder 只接收已验签通知；金额、商户、渠道、流水均需匹配，权益与 paid 状态原子提交。
// 超时或管理员已变更名额时记为 paid_review，保留到账证据而不覆盖管理操作，供人工退款核对。
func CompleteHostingOrder(id, merchant, method, trade string, cents int64) (*HostingOrder, error) {
	if trade == "" || len(trade) > 128 {
		return nil, ErrHostingConflict
	}
	vm2apiEnrollmentCommitMutex.Lock()
	defer vm2apiEnrollmentCommitMutex.Unlock()
	var order HostingOrder
	if err := DB.First(&order, "id = ?", id).Error; err != nil {
		return nil, err
	}
	var ratioJSON string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := lockForUpdate(tx).First(&user, "id = ?", order.UserID).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&order, "id = ?", id).Error; err != nil {
			return err
		}
		if order.Merchant != merchant || order.Method != method || order.PriceCents != cents || cents <= 0 {
			return ErrHostingConflict
		}
		if order.Status == "paid" || order.Status == "paid_review" {
			if order.ProviderTrade == nil || *order.ProviderTrade != trade {
				return ErrHostingConflict
			}
			return nil
		}
		now := time.Now().Unix()
		order.PaidAt = now
		order.ProviderTrade = &trade
		// 子事务确保交付失败时不残留名额、分组或 Key 改动，再单独记录需核对的真实付款。
		fulfillErr := tx.Transaction(func(inner *gorm.DB) error {
			if order.Status != "pending" || now >= order.ExpiresAt || user.Status != common.UserStatusEnabled || user.Role != common.RoleCommonUser {
				return ErrHostingConflict
			}
			grant := GPTHostingGrant{SeatID: order.SeatID, UserID: order.UserID, ActorID: order.UserID, StartsAt: now, EndsAt: now + int64(order.Days)*86400, Enabled: true, RetentionDays: 7, Note: "Paid hosting order " + order.ID}
			var binding GPTHostingBinding
			if order.Kind == "renew" {
				if err := hostingSeatScope(lockForUpdate(inner), order.UserID, []string{order.SeatID}).First(&grant).Error; err != nil {
					return ErrHostingConflict
				}
				if err := hostingSeatScope(lockForUpdate(inner), order.UserID, []string{order.SeatID}).First(&binding).Error; err != nil {
					return ErrHostingConflict
				}
				if !grant.Enabled || grant.EndsAt == HostingPermanentEnd || binding.Paused || binding.State == "cleaning" || binding.State == "takeover" || binding.OperationUntil > now {
					return ErrHostingConflict
				}
				// 下单后用户可能解绑并换了平台；原订单不能为另一个平台隐式续费。
				if binding.State != "unbound" && binding.Platform != order.Platform {
					return ErrHostingConflict
				}
				grant.EndsAt = max(now, grant.EndsAt) + int64(order.Days)*86400
				grant.ActorID = order.UserID
			}
			if grant.EndsAt > 4102444800 {
				return ErrHostingConflict
			}
			var inv *GPTHostingInventory
			if order.InventoryID != "" {
				if binding.InventoryID != nil {
					return ErrHostingConflict
				}
				var err error
				inv, err = hostingPurchaseInventory(inner, []string{order.InventoryID}, order.Platform, order.ID, now)
				if err != nil {
					return err
				}
			} else if binding.InventoryID == nil {
				return ErrHostingConflict
			}
			var saved GPTHostingGrant
			if err := persistHostingGrant(inner, grant, nil, &saved, &ratioJSON); err != nil {
				return err
			}
			if inv != nil {
				r := hostingSeatScope(inner.Model(&GPTHostingBinding{}), order.UserID, []string{order.SeatID}).Where("state = ? AND inventory_id IS NULL", "unbound").Updates(map[string]any{"inventory_id": inv.ID, "instance_id": inv.InstanceID, "slot_id": inv.SlotID, "state": "reserved", "platform": order.Platform, "generation": gorm.Expr("generation + 1")})
				if r.Error != nil {
					return r.Error
				}
				if r.RowsAffected != 1 {
					return ErrHostingConflict
				}
			}
			order.EndsAt = saved.EndsAt
			return nil
		})
		if fulfillErr != nil && !errors.Is(fulfillErr, ErrHostingConflict) && !errors.Is(fulfillErr, ErrHostingSoldOut) {
			return fulfillErr
		}
		order.Status = "paid"
		if fulfillErr != nil {
			order.Status = "paid_review"
			ratioJSON = ""
		}
		return tx.Save(&order).Error
	})
	if err != nil {
		return nil, err
	}
	if order.Status == "paid" {
		// 重复通知也刷新缓存，修复首次提交后进程退出或缓存短暂不可用的窗口。
		var option Option
		if err := DB.Where(Option{Key: "GroupRatio"}).First(&option).Error; err != nil {
			return nil, err
		}
		if err := updateOptionMap("GroupRatio", option.Value); err != nil {
			return nil, err
		}
		if err := RefreshUserGroupCache(order.UserID); err != nil {
			return nil, err
		}
	}
	return &order, nil
}
