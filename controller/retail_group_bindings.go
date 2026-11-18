package controller

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// GetRetailGroupBindings supplies safe selector options and the committed sync state to root sessions.
func GetRetailGroupBindings(c *gin.Context) {
	book, version, err := model.GetRetailPriceBook()
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	channels, err := model.RetailBindingChannels()
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	groups := []string{}
	for group := range ratio_setting.GetGroupRatioCopy() {
		groups = append(groups, group)
	}
	slices.Sort(groups)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"version": version, "bindings": book.GroupBindings, "model_sync": book.ModelSync, "sync": book.PublicRateSync,
		"local_groups": groups, "channels": channels, "server_time": time.Now().Unix(),
		"sale_policy":   retailprice.SalePolicy{ReceiptRatio: book.ReceiptRatio, MarginBPS: book.MarginBPS, FeeBPS: book.FeeBPS, BufferBPS: book.BufferBPS},
		"pricing_ready": len(book.Models) > 0 && len(book.Products) > 0, "pricing_enabled": book.Enabled,
	}})
}

// PutRetailGroupBindings re-fetches source values rather than trusting browser-supplied multipliers.
// Disabling sync can be saved during an outage using the last verified group names.
func PutRetailGroupBindings(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var input struct {
		ExpectedVersion int64                      `json:"expected_version"`
		Bindings        []retailprice.GroupBinding `json:"bindings"`
		Enabled         bool                       `json:"enabled"`
		SalePolicy      *retailprice.SalePolicy    `json:"sale_policy"`
		Supplies        []model.RetailSupplyEdit   `json:"supplies"`
	}
	if c.ShouldBindJSON(&input) != nil {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	book, version, err := model.GetRetailPriceBook()
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	if version != input.ExpectedVersion {
		retailPricingManagementError(c, model.ErrRetailPriceConflict)
		return
	}
	snapshot := book.PublicRateSync.Snapshot
	if input.Enabled || len(input.Bindings) > 0 {
		fresh, readErr := fetchRetailPublicRates(c.Request.Context())
		if readErr != nil && (input.Enabled || snapshot == nil) {
			common.ApiError(c, readErr)
			return
		}
		if readErr == nil {
			snapshot = fresh
		}
	}
	// The model commits credentials and pricing under one transaction; existing consoles
	// omit supplies and retain their previous bindings-only semantics.
	version, err = model.SaveRetailSupplyConfiguration(c.Request.Context(), input.ExpectedVersion, input.Bindings, input.Enabled, snapshot, c.GetInt("id"), input.SalePolicy, input.Supplies)
	if err != nil {
		if err == model.ErrRetailSupplyGroupName || err == model.ErrRetailSupplyGroupKey {
			common.ApiError(c, err)
		} else {
			retailPricingManagementError(c, err)
		}
		return
	}
	model.InitChannelCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"version": version}})
}

// SyncRetailGroupBindings provides an explicit refresh without altering mappings or consent.
func SyncRetailGroupBindings(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if c.ShouldBindJSON(&input) != nil {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	err := syncRetailGroupBindings(c.Request.Context(), input.ExpectedVersion)
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// syncRetailGroupBindings commits a failure state before returning errors, closing bound routes immediately.
func syncRetailGroupBindings(ctx context.Context, expected int64) error {
	snapshot, readErr := fetchRetailPublicRates(ctx)
	_, err := model.SyncRetailGroupRates(ctx, expected, snapshot, readErr != nil)
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	if min(snapshot.UpdatedAt, snapshot.FetchedAt)+retailprice.PublicRateMaxAge <= time.Now().Unix() {
		return retailprice.ErrExpired
	}
	return nil
}

// retailRatesHandler uses the existing DB lease so multiple gateway processes do not race to publish feeds.
type retailRatesHandler struct{}

// Type is the stable task/lease namespace for public price synchronization.
func (retailRatesHandler) Type() string { return "retail_group_rates" }

// Enabled requires explicit saved consent and never runs on the hosting site.
func (retailRatesHandler) Enabled() bool {
	if !common.IsRetailSite() {
		return false
	}
	book, _, err := model.GetRetailPriceBook()
	return err == nil && book.PublicRateSync.Enabled && len(book.GroupBindings) > 0
}

// Interval bounds polling; the independent five-minute quote expiry also covers worker downtime.
func (retailRatesHandler) Interval() time.Duration { return time.Minute }

// NewPayload deliberately accepts no URLs, credentials or operator-supplied rates.
func (retailRatesHandler) NewPayload() any { return nil }

// Run honors lease cancellation and uses optimistic publication to protect concurrent binding edits.
func (h retailRatesHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	book, version, err := model.GetRetailPriceBook()
	if err == nil && (!common.IsRetailSite() || !book.PublicRateSync.Enabled || len(book.GroupBindings) == 0) {
		err = retailprice.ErrAccess
	}
	if err == nil {
		err = model.UpdateSystemTaskState(task.TaskID, runnerID, nil)
	}
	if err == nil {
		err = syncRetailGroupBindings(ctx, version)
	}
	status := model.SystemTaskStatusSucceeded
	if err != nil {
		status = model.SystemTaskStatusFailed
	}
	finishSystemTaskHandler(task, runnerID, status, nil, err)
}
