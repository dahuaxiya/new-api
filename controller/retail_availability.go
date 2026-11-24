package controller

import (
	"context"
	"errors"
	"io"
	"math"
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

// 原始分子、分母只在服务端累加；API 只输出比值，不暴露流量和线路信息。
type retailAvailabilityCounts struct{ Success, Failure uint64 }
type retailAvailabilityWindow struct {
	Slices                     [retailAvailabilitySlices]retailAvailabilityCounts
	Input, Hit, FirstMS, Timed float64
}
type retailAvailabilitySeries struct{ Short, Day retailAvailabilityWindow }

// retailMetricWindow 的百分率使用 0–100；nil 表示无有效分母，0 表示实测为零。
type retailMetricWindow struct {
	SuccessPct      *float64                           `json:"success_pct"`
	CacheReadPct    *float64                           `json:"cache_read_pct"`
	MeanTTFTMS      *float64                           `json:"mean_ttft_ms"`
	SliceSuccessPct [retailAvailabilitySlices]*float64 `json:"slice_success_pct"`
}

// add 先合并原始统计再求比值，避免小样本模型和大样本模型具有相同权重。
func (w *retailAvailabilityWindow) add(v retailAvailabilityWindow) {
	for i, c := range v.Slices {
		w.Slices[i].Success += c.Success
		w.Slices[i].Failure += c.Failure
	}
	w.Input += v.Input
	w.Hit += v.Hit
	w.FirstMS += v.FirstMS
	w.Timed += v.Timed
}

// availabilityRatio 不将缺失分母解释为零；在公开边界统一舍入，内部保留原始权重。
func availabilityRatio(n, d, scale float64) *float64 {
	if d <= 0 {
		return nil
	}
	v := math.Round(n/d*scale*100) / 100
	return &v
}

// publicMetrics 从同一组样本生成状态和比例，缓存和延迟只属于整个窗口。
func (w retailAvailabilityWindow) publicMetrics(labels *[retailAvailabilitySlices]string) retailMetricWindow {
	m := retailMetricWindow{CacheReadPct: availabilityRatio(w.Hit, w.Input, 100), MeanTTFTMS: availabilityRatio(w.FirstMS, w.Timed, 1)}
	var success, failure uint64
	for i, c := range w.Slices {
		switch {
		case c.Success > 0 && c.Failure > 0:
			labels[i] = "mixed"
		case c.Success > 0:
			labels[i] = "success"
		case c.Failure > 0:
			labels[i] = "failure"
		default:
			labels[i] = "empty"
		}
		m.SliceSuccessPct[i] = availabilityRatio(float64(c.Success), float64(c.Success+c.Failure), 100)
		success += c.Success
		failure += c.Failure
	}
	m.SuccessPct = availabilityRatio(float64(success), float64(success+failure), 100)
	return m
}

// readAvailabilityPair 只接纳成对、非负的窗口累计量；缺失某个指标不会丢弃同一行可用的成功率。
func readAvailabilityPair(values []common.RawMessage, pos int, bounded bool) (float64, float64) {
	var a, b *uint64
	if common.Unmarshal(values[pos], &a) != nil || common.Unmarshal(values[pos+1], &b) != nil || a == nil || b == nil {
		return 0, 0
	}
	// 缓存读取量不能超过有效输入；没有计时样本时忽略时长，避免污染其他模型的均值。
	if (bounded && *b > *a) || (!bounded && *b == 0) {
		return 0, 0
	}
	return float64(*a), float64(*b)
}

// 源站分组与模型名只用于服务端映射。公开响应单独由 retailAvailabilityGroup 定义。
type retailAvailabilitySnapshot struct {
	updatedAt int64
	groups    map[string]map[string]retailAvailabilitySeries
}

type retailAvailabilityGroup struct {
	Metrics  map[string]retailMetricWindow    `json:"metrics"`
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
				window string
				stats  *retailAvailabilityWindow
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
					slot.stats.Slices[i] = retailAvailabilityCounts{Success: *pair[0], Failure: *pair[1]}
					sum.Success += *pair[0]
					sum.Failure += *pair[1]
				}
				// 上游第 4–7 项依次是输入 Token、缓存读取 Token、首字总毫秒、计时样本数。
				slot.stats.Input, slot.stats.Hit = readAvailabilityPair(values, 4, true)
				slot.stats.FirstMS, slot.stats.Timed = readAvailabilityPair(values, 6, false)
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
// 否则同名的其他上游分组会借用星行的统计。同一来源模型只累计一次。
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
		seen     map[[2]string]bool
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
			activity = &groupActivity{seen: make(map[[2]string]bool)}
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
				// 同一上游组和模型可被别名、产品或多把密钥重复绑定；每个本站分组内只能计数一次。
				key := [2]string{group, name}
				if activity.seen[key] {
					continue
				}
				activity.seen[key] = true
				activity.observed = true
				activity.series.Short.add(series.Short)
				activity.series.Day.add(series.Day)
			}
		}
	}
	out := make([]retailAvailabilityGroup, 0, len(groups))
	for group, activity := range groups {
		row := retailAvailabilityGroup{Group: group, Observed: activity.observed}
		row.Metrics = map[string]retailMetricWindow{"90m": activity.series.Short.publicMetrics(&row.Short), "24h": activity.series.Day.publicMetrics(&row.Day)}
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b retailAvailabilityGroup) int { return strings.Compare(a.Group, b.Group) })
	return out
}
