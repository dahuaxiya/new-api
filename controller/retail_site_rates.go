package controller

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/gin-gonic/gin"
)

// PutRetailSiteRates 读取本站管理员指定的同源公开接口。暂停只改变轮询开关，不会把失败报价恢复为有效。
func PutRetailSiteRates(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var in struct {
		Site    int    `json:"site_id"`
		Version int64  `json:"expected_version"`
		URL     string `json:"url"`
		Follow  *bool  `json:"follow"`
	}
	if !common.IsRetailSite() || c.ShouldBindJSON(&in) != nil || in.Site <= 0 {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	book, version, err := model.GetRetailPriceBook()
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	if version != in.Version {
		retailPricingManagementError(c, model.ErrRetailPriceConflict)
		return
	}
	prior := book.SiteFeeds[in.Site]
	next := prior
	channels, err := model.RetailRateSiteChannels(model.DB)
	if err != nil || !model.CheckRetailRateEndpoint(channels, in.Site, in.URL) {
		retailPricingManagementError(c, retailprice.ErrAccess)
		return
	}
	// 初次迁移星行时沿用原开关与快照。只有原编辑器批准过的站点可以接手这份报价。
	if prior.Snapshot == nil && in.URL == retailPublicRatesURL && book.PublicRateSync.Snapshot != nil {
		legacy, err := model.RetailBindingChannels()
		if err != nil {
			retailPricingManagementError(c, err)
			return
		}
		if model.CheckRetailRateEndpoint(legacy, in.Site, in.URL) {
			next = retailprice.SiteRateFeed{URL: in.URL, Follow: book.PublicRateSync.Enabled, Snapshot: book.PublicRateSync.Snapshot, LastError: book.PublicRateSync.LastError}
		}
	}
	if in.Follow != nil {
		next.Follow = *in.Follow
	}
	// 普通读取保留轮询开关。暂停无需网络；若上次抓取失败，保留故障状态，直到管理员成功读取。
	if !(in.Follow != nil && !*in.Follow && next.URL == in.URL && next.Snapshot != nil) {
		snapshot, err := fetchRetailSiteRates(c.Request.Context(), in.URL)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		next.URL, next.Snapshot, next.LastError = in.URL, snapshot, ""
	}
	version, err = model.SaveRetailSiteRates(c.Request.Context(), version, in.Site, prior, next, c.GetInt("id"))
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"version": version}})
}

// retailSiteRatesHandler 为站点独立轮询。与旧版星行任务使用不同租约，两者可兼容运行。
type retailSiteRatesHandler struct{}

// Type 返回站点倍率任务的租约名。
func (retailSiteRatesHandler) Type() string { return "retail_site_rates" }

// Interval 返回连续两轮抓取之间的间隔。
func (retailSiteRatesHandler) Interval() time.Duration { return time.Minute }

// NewPayload 只从服务端持久化配置确定站点和地址。
func (retailSiteRatesHandler) NewPayload() any { return nil }

// Enabled 仅在零售站已有管理员开启跟随时启动轮询。
func (retailSiteRatesHandler) Enabled() bool {
	if !common.IsRetailSite() {
		return false
	}
	book, _, err := model.GetRetailPriceBook()
	if err != nil {
		return false
	}
	for _, f := range book.SiteFeeds {
		if f.Follow {
			return true
		}
	}
	return false
}

// Run 并行抓取不同站点，再逐站保存；单个快照故障只暂停引用这个来源的计价。
func (retailSiteRatesHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	err := model.UpdateSystemTaskState(task.TaskID, runnerID, nil)
	if err == nil && common.IsRetailSite() {
		err = syncRetailSiteRates(ctx)
	}
	status := model.SystemTaskStatusSucceeded
	if err != nil {
		status = model.SystemTaskStatusFailed
	}
	finishSystemTaskHandler(task, runnerID, status, nil, err)
}

// syncRetailSiteRates 在网络调用外锁价表，提交前重新核对源状态，避免覆盖管理员刚暂停的跟随。
func syncRetailSiteRates(ctx context.Context) error {
	book, _, err := model.GetRetailPriceBook()
	if err != nil {
		return err
	}
	channels, err := model.RetailRateSiteChannels(model.DB)
	if err != nil {
		return err
	}
	type result struct {
		site        int
		prior, next retailprice.SiteRateFeed
		err         error
	}
	results := make(chan result, len(book.SiteFeeds))
	limit := make(chan struct{}, 8)
	var workers sync.WaitGroup
	for site, prior := range book.SiteFeeds {
		if !prior.Follow {
			continue
		}
		workers.Go(func() {
			select {
			case limit <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-limit }()
			next := prior
			var readErr error
			if model.CheckRetailRateEndpoint(channels, site, prior.URL) {
				var snapshot *retailprice.PublicRateSnapshot
				snapshot, readErr = fetchRetailSiteRates(ctx, prior.URL)
				if readErr == nil {
					next.Snapshot = snapshot
					next.LastError = ""
				}
			} else {
				readErr = retailprice.ErrAccess
			}
			if readErr != nil {
				next.LastError = "Unable to read upstream public multipliers"
			}
			results <- result{site, prior, next, readErr}
		})
	}
	go func() { workers.Wait(); close(results) }()
	var combined error
	for r := range results {
		if ctx.Err() != nil {
			continue
		}
		_, writeErr := model.SaveRetailSiteRates(ctx, 0, r.site, r.prior, r.next, 0)
		combined = errors.Join(combined, r.err, writeErr)
	}
	return errors.Join(ctx.Err(), combined)
}
