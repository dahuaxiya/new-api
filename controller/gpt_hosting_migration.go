package controller

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/gin-gonic/gin"
)

// hostingMigrationCheck is the safe preview returned to the administrator.
// It contains only display names and route coordinates; account hashes and
// upstream keys stay inside the server and vm2api instances.
type hostingMigrationCheck struct {
	SourceInstanceID string `json:"source_instance_id"`
	SourceSlotID     string `json:"source_slot_id"`
	TargetInstanceID string `json:"target_instance_id"`
	TargetSlotID     string `json:"target_slot_id"`
	TargetName       string `json:"target_name"`
	TargetPlatform   string `json:"target_platform"`
	InventoryID      string `json:"inventory_id"`
}

// hostingMigrationInput is deliberately smaller than the binding model so a
// browser cannot choose a source slot, channel, account identity, or key.
type hostingMigrationInput struct {
	UserID           int    `json:"user_id"`
	SeatID           string `json:"seat_id"`
	TargetInstanceID string `json:"target_instance_id"`
	TargetSlotID     string `json:"target_slot_id"`
}

type hostingMigrationFinishInput struct {
	UserID      int    `json:"user_id"`
	SeatID      string `json:"seat_id"`
	SessionID   string `json:"session_id"`
	CallbackURL string `json:"callback_url"`
}

// migrationInventory finds an enabled, administrator-registered target. The
// remote slot is checked separately because inventory is only a local allowlist.
func migrationInventory(instanceID, slotID string) (*model.GPTHostingInventory, error) {
	rows, err := model.ListHostingInventory()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Enabled && rows[i].InstanceID == instanceID && rows[i].SlotID == slotID {
			return &rows[i], nil
		}
	}
	return nil, model.ErrHostingConflict
}

// checkHostingMigration verifies both sides immediately before a write. A
// target must be a managed inventory slot in the same platform, while the
// source must still carry the binding that New API currently routes to.
func checkHostingMigration(ctx context.Context, userID int, input hostingMigrationInput) (*hostingMigrationCheck, error) {
	if input.UserID != userID || input.SeatID == "" || input.TargetInstanceID == "" || input.TargetSlotID == "" {
		return nil, model.ErrHostingConflict
	}
	grant, err := model.GetHostingGrant(userID, input.SeatID)
	if err != nil {
		return nil, err
	}
	if !grant.ValidAt(nowUnix()) {
		return nil, model.ErrHostingUnavailable
	}
	binding, err := model.GetHostingBinding(userID, input.SeatID)
	if err != nil {
		return nil, err
	}
	if binding.State != "active" || binding.InventoryID == nil || binding.ChannelID == 0 || binding.AccountHash == nil || binding.SubjectHash == nil || binding.MigrationState != "" {
		return nil, model.ErrHostingConflict
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return nil, err
	}
	source, err := registry.Client(binding.InstanceID)
	if err != nil {
		return nil, err
	}
	sourceSlots, err := source.HostedSlots(ctx)
	if err != nil {
		return nil, err
	}
	var sourceSlot *vm2api.HostedSlot
	for i := range sourceSlots {
		if sourceSlots[i].ID == binding.SlotID {
			sourceSlot = &sourceSlots[i]
			break
		}
	}
	if sourceSlot == nil || sourceSlot.Hosting == nil || sourceSlot.Hosting.BindingID != binding.BindingID || sourceSlot.Hosting.State != "active" {
		return nil, model.ErrHostingConflict
	}
	if input.TargetInstanceID == binding.InstanceID && input.TargetSlotID == binding.SlotID {
		return nil, model.ErrHostingConflict
	}
	targetInventory, err := migrationInventory(input.TargetInstanceID, input.TargetSlotID)
	if err != nil {
		return nil, err
	}
	target, err := registry.Client(input.TargetInstanceID)
	if err != nil {
		return nil, err
	}
	targetSlots, err := target.HostedSlots(ctx)
	if err != nil {
		return nil, err
	}
	for i := range targetSlots {
		slot := &targetSlots[i]
		if slot.ID != input.TargetSlotID {
			continue
		}
		platform := slot.Platform
		if platform == "" {
			platform = "codex"
		}
		if platform != binding.Platform || !slot.Empty || !slot.ExitReady || !slot.Managed || slot.Hosting == nil || slot.Hosting.State != "inventory" {
			return nil, &vm2api.Error{Code: "slot_unavailable", HTTPStatus: http.StatusConflict}
		}
		return &hostingMigrationCheck{
			SourceInstanceID: binding.InstanceID,
			SourceSlotID:     binding.SlotID,
			TargetInstanceID: input.TargetInstanceID,
			TargetSlotID:     slot.ID,
			TargetName:       slot.Name,
			TargetPlatform:   platform,
			InventoryID:      targetInventory.ID,
		}, nil
	}
	return nil, &vm2api.Error{Code: "slot_unavailable", HTTPStatus: http.StatusConflict}
}

// CheckGPTHostingMigration exposes a fresh target preview without reserving a
// slot. The final start endpoint repeats every check under a database lease.
func CheckGPTHostingMigration(c *gin.Context) {
	actor, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input hostingMigrationInput
	if !onboardingBody(c, &input) {
		return
	}
	result, err := checkHostingMigration(c.Request.Context(), actor, input)
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// StartGPTHostingMigration reserves the target and starts official OAuth. It
// leaves the source route active until FinishGPTHostingMigration succeeds.
func StartGPTHostingMigration(c *gin.Context) {
	actor, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input hostingMigrationInput
	if !onboardingBody(c, &input) {
		return
	}
	raw, _ := common.Marshal(service.AdminUserContext{UserID: input.UserID})
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAdminUserUpdate, Context: raw}) == nil {
		return
	}
	seat := input.SeatID
	binding, lease, err := model.ClaimHostingOperation(input.UserID, seat)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(input.UserID, lease, seat)
	if binding.MigrationState == "switched" || (binding.MigrationState != "" && (binding.MigrationTargetInstanceID != input.TargetInstanceID || binding.MigrationTargetSlotID != input.TargetSlotID)) {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	var preview *hostingMigrationCheck
	if binding.MigrationState == "" {
		preview, err = checkHostingMigration(c.Request.Context(), input.UserID, input)
		if err != nil {
			hostingError(c, err)
			return
		}
		binding, err = model.BeginHostingMigration(input.UserID, lease, preview.InventoryID, input.TargetInstanceID, input.TargetSlotID, seat)
		if err != nil {
			hostingError(c, err)
			return
		}
	} else {
		preview, err = migrationPreviewFromBinding(binding)
		if err != nil {
			hostingError(c, err)
			return
		}
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	target, err := registry.Client(binding.MigrationTargetInstanceID)
	if err != nil {
		hostingError(c, err)
		return
	}
	if _, err = target.HostingAction(c.Request.Context(), binding.MigrationTargetSlotID, "reserve", gin.H{"binding_id": binding.BindingID, "generation": binding.Generation}); err != nil {
		hostingError(c, err)
		return
	}
	if err = model.UpdateHostingMigrationState(input.UserID, lease, "reserved", seat); err != nil {
		hostingError(c, err)
		return
	}
	auth, err := registry.StartHostedOAuth(c.Request.Context(), actor, c.GetString("session_id"), binding.MigrationTargetInstanceID, binding.MigrationTargetSlotID, binding.BindingID, binding.Generation, binding.Platform)
	if err != nil {
		hostingError(c, err)
		return
	}
	if err = model.UpdateHostingMigrationState(input.UserID, lease, "authorizing", seat); err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"auth_url": auth.AuthURL, "session_id": auth.SessionID, "expires_at": auth.ExpiresAt,
		"source_instance_id": preview.SourceInstanceID, "source_slot_id": preview.SourceSlotID,
		"target_instance_id": preview.TargetInstanceID, "target_slot_id": preview.TargetSlotID,
		"target_name": preview.TargetName,
	}})
}

// migrationPreviewFromBinding reconstructs the safe target preview for a
// retry after the initial response was lost; it never re-reads private fields.
func migrationPreviewFromBinding(binding *model.GPTHostingBinding) (*hostingMigrationCheck, error) {
	if binding == nil || binding.MigrationState == "" || binding.MigrationTargetInstanceID == "" || binding.MigrationTargetSlotID == "" {
		return nil, model.ErrHostingConflict
	}
	return &hostingMigrationCheck{
		SourceInstanceID: binding.MigrationSourceInstanceID,
		SourceSlotID:     binding.MigrationSourceSlotID,
		TargetInstanceID: binding.MigrationTargetInstanceID,
		TargetSlotID:     binding.MigrationTargetSlotID,
		InventoryID:      binding.MigrationTargetInventoryID,
	}, nil
}

// FinishGPTHostingMigration validates the same official account on the target,
// activates it, switches the New API channel atomically, then releases source.
// If source cleanup is unknown, routing stays on target and the operation is
// left in switched state for a safe retry instead of reporting false success.
func FinishGPTHostingMigration(c *gin.Context) {
	actor, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input hostingMigrationFinishInput
	if !onboardingBody(c, &input) {
		return
	}
	raw, _ := common.Marshal(service.AdminUserContext{UserID: input.UserID})
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAdminUserUpdate, Context: raw}) == nil {
		return
	}
	binding, lease, err := model.ClaimHostingOperation(input.UserID, input.SeatID)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(input.UserID, lease, binding.SeatID)
	if binding.MigrationState == "switched" {
		finishHostingMigrationCleanup(c, binding, lease)
		return
	}
	if binding.MigrationState != "authorizing" && binding.MigrationState != "prepared" && binding.MigrationState != "reserved" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	target, err := registry.Client(binding.MigrationTargetInstanceID)
	if err != nil {
		hostingError(c, err)
		return
	}
	var prepared vm2api.HostedState
	if strings.TrimSpace(input.CallbackURL) != "" {
		if binding.MigrationState != "authorizing" || input.SessionID == "" {
			hostingError(c, model.ErrHostingConflict)
			return
		}
		prepared, err = registry.PrepareHostedOAuth(c.Request.Context(), actor, c.GetString("session_id"), binding.MigrationTargetInstanceID, binding.MigrationTargetSlotID, binding.BindingID, input.SessionID, input.CallbackURL, binding.Generation)
	} else {
		prepared, err = migrationRemoteState(c.Request.Context(), target, binding.MigrationTargetSlotID, binding.BindingID)
		if err == nil && prepared.State == "reserved" {
			err = &vm2api.Error{Code: "oauth_session_expired", HTTPStatus: http.StatusConflict}
		}
	}
	if err != nil {
		hostingError(c, err)
		return
	}
	if !validPreparedMigrationIdentity(binding, prepared) {
		if releaseErr := releaseMigrationTarget(c.Request.Context(), target, binding); releaseErr == nil {
			_ = model.AbortHostingMigration(input.UserID, lease, binding.SeatID)
		} else {
			_ = model.UpdateHostingMigrationState(input.UserID, lease, "prepared", binding.SeatID)
		}
		hostingError(c, model.ErrHostingConflict)
		return
	}
	if err = model.UpdateHostingMigrationState(input.UserID, lease, "prepared", binding.SeatID); err != nil {
		hostingError(c, err)
		return
	}
	grant, err := model.GetHostingGrant(input.UserID, binding.SeatID)
	if err != nil {
		hostingError(c, err)
		return
	}
	active := prepared
	if prepared.State != "active" {
		active, err = target.HostingAction(c.Request.Context(), binding.MigrationTargetSlotID, "activate", gin.H{
			"binding_id": binding.BindingID, "generation": binding.Generation,
			"account_hash": prepared.AccountHash, "subject_hash": prepared.SubjectHash,
			"starts_at": grant.StartsAt, "ends_at": grant.EndsAt, "enabled": grant.Enabled,
		})
		if err != nil {
			hostingError(c, err)
			return
		}
	}
	if active.BindingID != binding.BindingID || active.AccountHash != prepared.AccountHash || active.SubjectHash != prepared.SubjectHash || active.Key == "" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	updated, err := model.CompleteHostingMigration(input.UserID, lease, target.Config().BaseURL, strings.Join(target.HostingModels(binding.Platform), ","), active.Key, active.KeyID, active.PoolID, binding.SeatID)
	if err != nil {
		hostingError(c, err)
		return
	}
	model.InitChannelCache()
	finishHostingMigrationCleanup(c, updated, lease)
}

// validPreparedMigrationIdentity is the last server-side identity gate before
// activation; a valid OAuth callback alone is insufficient when the target
// slot returns another official account or another binding.
func validPreparedMigrationIdentity(binding *model.GPTHostingBinding, prepared vm2api.HostedState) bool {
	return binding != nil && prepared.IdentitySource == "official" && prepared.BindingID == binding.BindingID && prepared.VMID == binding.MigrationTargetSlotID && prepared.AccountHash == valueOrEmpty(binding.AccountHash) && prepared.SubjectHash == valueOrEmpty(binding.SubjectHash) && (prepared.State == "prepared" || prepared.State == "active")
}

// finishHostingMigrationCleanup releases the source after routing switched;
// it returns 202 when the remote result is unknown, preserving retry state.
func finishHostingMigrationCleanup(c *gin.Context, binding *model.GPTHostingBinding, lease string) {
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	source, err := registry.Client(binding.MigrationSourceInstanceID)
	if err != nil {
		hostingError(c, err)
		return
	}
	state, err := source.HostingAction(c.Request.Context(), binding.MigrationSourceSlotID, "release", gin.H{"binding_id": binding.BindingID, "generation": binding.Generation})
	if err != nil {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "cleanup_pending": true, "data": gin.H{"state": "switched"}})
		return
	}
	if state.State != "inventory" || state.BindingID != "" {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "cleanup_pending": true, "data": gin.H{"state": "switched"}})
		return
	}
	if err = model.FinalizeHostingMigration(binding.UserID, lease, binding.SeatID); err != nil {
		hostingError(c, err)
		return
	}
	recordHostingUserAudit(c, binding.UserID, "hosting.migrate", map[string]any{"seat_id": binding.SeatID, "source_instance_id": binding.MigrationSourceInstanceID, "source_slot_id": binding.MigrationSourceSlotID, "target_instance_id": binding.InstanceID, "target_slot_id": binding.SlotID})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"state": "active"}})
}

// CancelGPTHostingMigration is an administrator recovery path for an OAuth
// attempt that has not switched routing. It never clears the source account.
func CancelGPTHostingMigration(c *gin.Context) {
	_, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input hostingMigrationInput
	if !onboardingBody(c, &input) {
		return
	}
	raw, _ := common.Marshal(service.AdminUserContext{UserID: input.UserID})
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAdminUserUpdate, Context: raw}) == nil {
		return
	}
	binding, lease, err := model.ClaimHostingOperation(input.UserID, input.SeatID)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(input.UserID, lease, binding.SeatID)
	if binding.MigrationState == "" || binding.MigrationState == "switched" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	registry, err := onboardingRegistry()
	if err == nil {
		if target, e := registry.Client(binding.MigrationTargetInstanceID); e == nil {
			err = releaseMigrationTarget(c.Request.Context(), target, binding)
		}
	}
	if err != nil {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "cleanup_pending": true, "data": gin.H{"state": binding.MigrationState}})
		return
	}
	if err = model.AbortHostingMigration(input.UserID, lease, binding.SeatID); err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"state": "active"}})
}

func releaseMigrationTarget(ctx context.Context, target *vm2api.Client, binding *model.GPTHostingBinding) error {
	state, err := target.HostingAction(ctx, binding.MigrationTargetSlotID, "release", gin.H{"binding_id": binding.BindingID, "generation": binding.Generation})
	if err != nil {
		return err
	}
	if state.State != "inventory" || state.BindingID != "" {
		return model.ErrHostingConflict
	}
	return nil
}

func migrationRemoteState(ctx context.Context, target *vm2api.Client, slotID, bindingID string) (vm2api.HostedState, error) {
	slots, err := target.HostedSlots(ctx)
	if err != nil {
		return vm2api.HostedState{}, err
	}
	for _, slot := range slots {
		if slot.ID == slotID && slot.Hosting != nil && slot.Hosting.BindingID == bindingID {
			return *slot.Hosting, nil
		}
	}
	return vm2api.HostedState{}, model.ErrHostingConflict
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func nowUnix() int64 { return time.Now().Unix() }
