package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/gin-gonic/gin"
)

var quotaHistoryOnce sync.Once

// quotaHistoryScope 将用户生命周期和当前绑定共同编码；用户名、槽位相同不能代替归属。
// 调用者只能传从会话和数据库取得的身份，不能接收浏览器的 user_id/slot。
func quotaHistoryScope(svc *gptquota.Service, user *model.User, seats ...string) (gptquota.Binding, string, error) {
	binding, epoch, err := resolveGPTQuotaBinding(svc, user.Id, user.Username, user.Group, seats...)
	if err != nil {
		return binding, "", err
	}
	if binding.Slot == "" || binding.Username != user.Username || binding.Group != user.Group {
		return binding, "", nil
	}
	raw, err := common.Marshal([]any{user.Id, user.CreatedAt, user.Username, user.Group, epoch, binding.InstanceID, binding.Slot})
	return binding, fmt.Sprintf("%x", sha256.Sum256(raw)), err
}

// collectGPTQuotaUser 仅读取 vm2api 已有官方快照，不生成模型请求或重复执行官方探测。
// 网络读取后重查归属，防止慢响应在解绑或改组后被记到新绑定；失败不伪造零值。
func collectGPTQuotaUser(ctx context.Context, svc *gptquota.Service, user *model.User, seats ...string) error {
	binding, scope, err := quotaHistoryScope(svc, user, seats...)
	if err != nil || scope == "" {
		return err
	}
	view, err := svc.ObserveFor(ctx, user.Id, user.Username, user.Group, binding)
	if err != nil || !view.Bound || view.Stale || view.Status != "ok" || view.Observed == nil {
		return err
	}
	if binding.AccountHash != "" && binding.AccountHash != view.AccountFingerprint {
		return gptquota.ErrUnbound
	}
	current, err := model.GetUserById(user.Id, false)
	if err != nil {
		return err
	}
	_, currentScope, err := quotaHistoryScope(svc, current, seats...)
	if err != nil || scope != currentScope || current.Status != common.UserStatusEnabled {
		return err
	}
	observed, err := time.Parse(time.RFC3339Nano, *view.Observed)
	if err != nil {
		return err
	}
	row := model.GPTQuotaHistory{UserID: user.Id, Scope: scope, ObservedAt: observed.UnixMilli(), Segment: fmt.Sprintf("%x", sha256.Sum256([]byte(scope+"\x00"+view.AccountFingerprint)))}
	for _, window := range view.Windows {
		var reset *int64
		remaining := window.Remaining
		if window.Reset != nil {
			if parsed, err := time.Parse(time.RFC3339Nano, *window.Reset); err == nil {
				value := parsed.UnixMilli()
				reset = &value
				// 快照本身已经越过窗口截止时间时，百分比属于旧窗口，不能当成新观测。
				if !parsed.After(observed) {
					remaining = nil
				}
			}
		}
		switch window.ID {
		case "5h":
			row.Remaining5h, row.Reset5h = remaining, reset
		case "7d":
			row.Remaining7d, row.Reset7d = remaining, reset
		}
	}
	return model.SaveGPTQuotaHistory(ctx, row, time.Now())
}

// collectGPTQuotaHistory 分页、串行读取已启用用户，单用户失败不阻止其他实例继续采集。
// 每轮有总预算，不启动重叠批次；数据库唯一约束使多副本采集也不会重复增长。
func collectGPTQuotaHistory(ctx context.Context, svc *gptquota.Service) error {
	lastID := 0
	for ctx.Err() == nil {
		var users []model.User
		if err := model.DB.WithContext(ctx).Select("id", "username", "group", "created_at", "status").
			Where("status = ? AND id > ?", common.UserStatusEnabled, lastID).Order("id ASC").Limit(100).Find(&users).Error; err != nil {
			return err
		}
		if len(users) == 0 {
			break
		}
		for i := range users {
			userCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			var seats []model.GPTHostingGrant
			var err error
			if os.Getenv("GPT_HOSTING_ENABLED") == "true" {
				seats, err = model.ListHostingSeats(users[i].Id)
			}
			if err == nil && len(seats) > 0 {
				for _, seat := range seats {
					if e := collectGPTQuotaUser(userCtx, svc, &users[i], seat.SeatID); e != nil && e != gptquota.ErrUnbound {
						err = e
					}
				}
			} else if err == nil {
				err = collectGPTQuotaUser(userCtx, svc, &users[i])
			}
			cancel()
			if err != nil && err != gptquota.ErrUnbound {
				// 不记录上游响应、用户身份或令牌；采集失败不影响推理和实时额度查询。
				common.SysError("GPT quota history observation unavailable")
			}
		}
		lastID = users[len(users)-1].Id
	}
	return model.PruneGPTQuotaHistory(ctx, time.Now())
}

// StartGPTQuotaHistory 在启动后和每分钟读取最新快照；五分钟桶限制持久化密度。
// 既有 vm2api 五分钟官方探测保持不变；未部署额度功能的安装不会启动此任务。
func StartGPTQuotaHistory() {
	if os.Getenv("GPT_QUOTA_CONFIG") == "" && os.Getenv("VM2API_INSTANCES_CONFIG") == "" {
		return
	}
	quotaHistoryOnce.Do(func() {
		go func() {
			svc, err := quotaService()
			if err != nil {
				common.SysError("GPT quota history configuration unavailable")
				return
			}
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
				err := collectGPTQuotaHistory(ctx, svc)
				cancel()
				if err != nil {
					common.SysError("GPT quota history collection unavailable")
				}
				<-ticker.C
			}
		}()
	})
}

// GetGPTQuotaHistorySelf 仅读取当前会话的绑定历史，离线也可查看已保存数据。
// 时间范围使用白名单，额外身份或槽位参数直接拒绝，不能扩大查询范围。
func GetGPTQuotaHistorySelf(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	id := c.GetInt("id")
	if hint := c.GetHeader("New-Api-User"); id <= 0 || (hint != "" && hint != strconv.Itoa(id)) {
		c.AbortWithStatus(401)
		return
	}
	params, err := urlHistoryHours(c)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Invalid parameters"})
		return
	}
	user, err := model.GetUserById(id, false)
	if err != nil || user.Status != common.UserStatusEnabled || user.Username != c.GetString("username") || user.Group != c.GetString("group") {
		c.AbortWithStatus(401)
		return
	}
	svc, err := quotaService()
	if err != nil {
		c.AbortWithStatus(503)
		return
	}
	_, scope, err := quotaHistoryScope(svc, user, c.Query("seat_id"))
	if err == gptquota.ErrUnbound {
		scope, err = "", nil
	}
	if err != nil {
		c.AbortWithStatus(503)
		return
	}
	rows := make([]model.GPTQuotaHistory, 0)
	if scope != "" {
		rows, err = model.GetGPTQuotaHistory(c.Request.Context(), id, scope, params, time.Now())
	}
	if err != nil {
		c.AbortWithStatus(503)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"points": rows, "retention_days": model.GPTQuotaHistoryDays, "sample_seconds": model.GPTQuotaHistoryBucketSeconds}})
}

// urlHistoryHours 严格解析唯一 hours 参数，重复值和未知参数都不能悄悄改变查询语义。
func urlHistoryHours(c *gin.Context) (int, error) {
	values, parseErr := url.ParseQuery(c.Request.URL.RawQuery)
	if parseErr != nil {
		return 0, parseErr
	}
	if selected, ok := values["seat_id"]; ok {
		if len(selected) != 1 || len(selected[0]) > 36 {
			return 0, fmt.Errorf("invalid seat")
		}
		values.Del("seat_id")
	}
	if len(values) == 0 {
		return 24, nil
	}
	if len(values) != 1 || len(values["hours"]) != 1 {
		return 0, fmt.Errorf("invalid history range")
	}
	hours, err := strconv.Atoi(values.Get("hours"))
	if err != nil || (hours != 24 && hours != 168 && hours != 720) {
		return 0, fmt.Errorf("invalid history range")
	}
	return hours, nil
}
