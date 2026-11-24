package controller

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/gin-gonic/gin"
)

const retailAvailabilityURL = "https://aixingxing.pro/api/channel-market"
const retailAvailabilityMaxAge = 10 * time.Minute

var errRetailAvailability = errors.New("invalid or unavailable upstream availability snapshot")

var retailAvailabilityCache struct {
	sync.Mutex
	until    time.Time
	snapshot *retailAvailabilitySnapshot
}

const retailAvailabilitySlices = 18

// retailAvailabilityCounts 只用于校验上游响应，不能进入公开响应。
type retailAvailabilityCounts struct {
	Success uint64
	Failure uint64
}

// 两个位分别表示出现过成功和失败。合并时只保留状态，不能从公开数据还原请求量。
type retailSliceStatus uint8

const (
	retailSliceSuccess retailSliceStatus = 1 << iota
	retailSliceFailure
)

var retailSliceLabels = [...]string{"empty", "success", "failure", "mixed"}

type retailAvailabilitySeries struct {
	Short [retailAvailabilitySlices]retailSliceStatus
	Day   [retailAvailabilitySlices]retailSliceStatus
}

// 源站分组与模型名只用于服务端映射。公开响应单独由 retailAvailabilityGroup 定义。
type retailAvailabilitySnapshot struct {
	updatedAt int64
	groups    map[string]map[string]retailAvailabilitySeries
}

type retailAvailabilityGroup struct {
	Group    string                           `json:"group"`
	Observed bool                             `json:"observed"`
	Short    [retailAvailabilitySlices]string `json:"90m"`
	Day      [retailAvailabilitySlices]string `json:"24h"`
}

// GetRetailAvailability 为萝卜已公开的调用分组返回按时间片汇总的状态。源站失败时短暂缓存失败，避免反复请求上游。
// c 由公开只读路由传入，不使用访问者的凭据或查询参数访问源站。
func GetRetailAvailability(c *gin.Context) {
	retailAvailabilityCache.Lock()
	if time.Now().After(retailAvailabilityCache.until) {
		// 刷新由所有访客共享，单个访客断线不能取消刷新。客户端有独立的总超时。
		snapshot, err := fetchRetailAvailability()
		ttl := time.Minute
		if err != nil {
			ttl = 15 * time.Second
		}
		retailAvailabilityCache.snapshot = snapshot
		retailAvailabilityCache.until = time.Now().Add(ttl)
	}
	snapshot := retailAvailabilityCache.snapshot
	retailAvailabilityCache.Unlock()

	if snapshot == nil || time.Since(time.Unix(snapshot.updatedAt, 0)) > retailAvailabilityMaxAge {
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Availability data is temporarily unavailable"))
		return
	}
	book, _, err := model.GetRetailPriceBook()
	if err != nil {
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Availability data is temporarily unavailable"))
		return
	}
	channels, err := model.RetailBindingChannels()
	if err != nil {
		common.ApiErrorStatus(c, http.StatusServiceUnavailable, common.NewMessage("Availability data is temporarily unavailable"))
		return
	}
	// 公开响应只包含本站分组和时间片状态，不能返回原始快照或线路映射。
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"updated_at": snapshot.updatedAt, "groups": projectRetailAvailability(book, channels, snapshot),
	}})
}

// fetchRetailAvailability 从固定公开地址匿名取样，沿用公开倍率客户端的超时和禁止跳转约束。
// 返回内部快照供分组映射，读取失败或内容过大时返回错误。
func fetchRetailAvailability() (*retailAvailabilitySnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, retailAvailabilityURL, nil)
	if err != nil {
		return nil, errRetailAvailability
	}
	req.Header.Set("Accept", "application/json")
	response, err := retailPublicRatesClient.Do(req)
	if err != nil {
		return nil, errRetailAvailability
	}
	defer response.Body.Close()
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || mediaType != "application/json" {
		return nil, errRetailAvailability
	}
	const maxBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		return nil, errRetailAvailability
	}
	return parseRetailAvailability(body, time.Now())
}

// parseRetailAvailability 读取源站每种时段的 18 个连续时间片，以快照发布时间为结束时间。
// body 是源站 JSON，now 用来判定过期和未来时间。格式不符时不会生成看似正常的时间片。
func parseRetailAvailability(body []byte, now time.Time) (*retailAvailabilitySnapshot, error) {
	var source struct {
		Updated int64 `json:"updated"`
		Groups  map[string]struct {
			Models  map[string]bool                           `json:"models"`
			Windows map[string]map[string][]common.RawMessage `json:"windows"`
		} `json:"groups"`
	}
	if common.Unmarshal(body, &source) != nil || source.Updated < now.Add(-retailAvailabilityMaxAge).Unix() || source.Updated > now.Add(30*time.Second).Unix() || len(source.Groups) == 0 || len(source.Groups) > 200 {
		return nil, errRetailAvailability
	}
	out := &retailAvailabilitySnapshot{updatedAt: source.Updated, groups: map[string]map[string]retailAvailabilitySeries{}}
	for group, entry := range source.Groups {
		if !retailprice.ValidBindingGroup(group) || len(entry.Models) > 500 || entry.Windows["90m"] == nil || entry.Windows["24h"] == nil {
			return nil, errRetailAvailability
		}
		out.groups[group] = map[string]retailAvailabilitySeries{}
		for name := range entry.Models {
			if !retailprice.ValidBindingGroup(name) {
				return nil, errRetailAvailability
			}
			series := retailAvailabilitySeries{}
			for _, slot := range []struct {
				window  string
				buckets *[retailAvailabilitySlices]retailSliceStatus
			}{{"90m", &series.Short}, {"24h", &series.Day}} {
				values, sampled := entry.Windows[slot.window][name]
				if !sampled {
					// 缺少某模型的记录表示没有样本，不能当作调用成功。
					continue
				}
				// 第 0、1 项是窗口总量，第 8 项是按时间排序的成功和失败数。
				// 核对总量，避免上游字段结构变更时仍画出貌似可信的历史状态。
				var totalSuccess, totalFailure *uint64
				var slicesIn [][]*uint64
				if len(values) < 9 || common.Unmarshal(values[0], &totalSuccess) != nil || common.Unmarshal(values[1], &totalFailure) != nil || totalSuccess == nil || totalFailure == nil || common.Unmarshal(values[8], &slicesIn) != nil || len(slicesIn) != retailAvailabilitySlices {
					return nil, errRetailAvailability
				}
				var sum retailAvailabilityCounts
				for i, pair := range slicesIn {
					if len(pair) != 2 || pair[0] == nil || pair[1] == nil || *pair[0] > 1_000_000_000 || *pair[1] > 1_000_000_000 {
						return nil, errRetailAvailability
					}
					if *pair[0] != 0 {
						(*slot.buckets)[i] |= retailSliceSuccess
					}
					if *pair[1] != 0 {
						(*slot.buckets)[i] |= retailSliceFailure
					}
					sum.Success += *pair[0]
					sum.Failure += *pair[1]
				}
				if sum.Success != *totalSuccess || sum.Failure != *totalFailure {
					return nil, errRetailAvailability
				}
			}
			out.groups[group][name] = series
		}
	}
	return out, nil
}

// projectRetailAvailability 只按已公开的本站调用分组输出时间片。channels 必须来自星行白名单，
// 否则同名的其他上游分组会借用星行的统计。重复绑定的状态按位合并，不会重复计数。
func projectRetailAvailability(book retailprice.Book, channels []model.RetailBindingChannel, snapshot *retailAvailabilitySnapshot) []retailAvailabilityGroup {
	registered := book.RegisteredGroups()
	trusted := map[int]string{}
	for _, ch := range channels {
		// 暂停的线路不会继续承接本站请求，不能用它的上游历史样本给本站分组判绿。
		if group := registered[ch.ID]; group != "" && ch.HasKey && ch.Status == common.ChannelStatusEnabled {
			trusted[ch.ID] = group
		}
	}
	type groupActivity struct {
		observed bool
		series   retailAvailabilitySeries
	}
	// 价表允许多个产品共用一个调用分组。聚合键用分组名，公开页始终只有一条时间轴。
	groups := map[string]*groupActivity{}
	for _, product := range book.Products {
		if !product.Public || !retailprice.ValidBindingGroup(product.BindingGroup) {
			continue
		}
		activity := groups[product.BindingGroup]
		if activity == nil {
			activity = &groupActivity{}
			groups[product.BindingGroup] = activity
		}
		for _, route := range product.Routes {
			group := trusted[route.ChannelID]
			if group == "" {
				continue
			}
			for _, name := range route.Models {
				series, present := snapshot.groups[group][name]
				if !present {
					continue
				}
				activity.observed = true
				for i := range retailAvailabilitySlices {
					activity.series.Short[i] |= series.Short[i]
					activity.series.Day[i] |= series.Day[i]
				}
			}
		}
	}
	out := make([]retailAvailabilityGroup, 0, len(groups))
	for group, activity := range groups {
		row := retailAvailabilityGroup{Group: group, Observed: activity.observed}
		for i := range retailAvailabilitySlices {
			row.Short[i] = retailSliceLabels[activity.series.Short[i]]
			row.Day[i] = retailSliceLabels[activity.series.Day[i]]
		}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b retailAvailabilityGroup) int { return strings.Compare(a.Group, b.Group) })
	return out
}
