package controller

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/QuantumNous/new-api/service/onboarding"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var onboardingRegistry = sync.OnceValues(func() (*vm2api.Registry, error) {
	return vm2api.Load(os.Getenv("VM2API_INSTANCES_CONFIG"))
})

// configuredQuota preserves old static bindings while new accounts use the database.
// A configured but unreadable file fails closed: its occupied slots must not be reused.
var configuredQuota = sync.OnceValues(func() (gptquota.Config, error) {
	var cfg gptquota.Config
	path := os.Getenv("GPT_QUOTA_CONFIG")
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	err = common.Unmarshal(raw, &cfg)
	return cfg, err
})

var onboardingUsername = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)
var onboardingIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type onboardingSlot struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	LoggedIn  bool   `json:"logged_in"`
	Assigned  bool   `json:"assigned"`
	Reason    string `json:"reason,omitempty"`
}

type onboardingInstance struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Available bool             `json:"available"`
	Error     string           `json:"error,omitempty"`
	Slots     []onboardingSlot `json:"slots"`
	Exits     []vm2api.Exit    `json:"exits"`
}

type onboardingRequest struct {
	RequestID   string `json:"request_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Quota       int    `json:"quota"`
	InstanceID  string `json:"instance_id"`
	SlotID      string `json:"slot_id"`
}

// onboardingActor adds a local guard to the route's live RootAuth check. Browser
// hints cannot impersonate another actor, and responses are never cacheable.
func onboardingActor(c *gin.Context) (int, bool) {
	c.Header("Cache-Control", "no-store, private")
	id := c.GetInt("id")
	if id <= 0 || (c.GetHeader("New-Api-User") != "" && c.GetHeader("New-Api-User") != strconv.Itoa(id)) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required"})
		return 0, false
	}
	if c.GetInt("role") != common.RoleRootUser {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "Access denied"})
		return 0, false
	}
	return id, true
}

// onboardingError reports only locally defined errors. SQL and upstream bodies
// may contain keys or passwords and must never enter a browser error response.
func onboardingError(c *gin.Context, err error) {
	status, message := http.StatusServiceUnavailable, "Onboarding request failed"
	var upstream *vm2api.Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, message = http.StatusNotFound, "Not found"
	case errors.Is(err, model.ErrVM2APIEnrollmentConflict):
		status, message = http.StatusConflict, "Onboarding request conflicts with an existing account"
	case errors.Is(err, model.ErrVM2APIEnrollmentBusy), errors.Is(err, model.ErrVM2APIEnrollmentLease):
		status, message = http.StatusConflict, "Onboarding operation is still running. Retry later."
	case errors.As(err, &upstream):
		// Only stable, locally defined codes become user guidance. The original
		// upstream response is discarded by the client before it reaches here.
		switch upstream.Code {
		case "oauth_session_expired", "oauth_session_mismatch", "oauth_state_mismatch", "instance_changed":
			status, message = http.StatusConflict, "Authorization expired or does not match. Start official sign-in again."
		case "invalid_oauth_callback":
			status, message = http.StatusBadRequest, "Paste the complete official callback URL."
		case "oauth_in_progress":
			status, message = http.StatusConflict, "Onboarding operation is still running. Retry later."
		case "exit_unavailable":
			status, message = http.StatusConflict, "The selected outbound connection is unavailable."
		case "pool_scope_conflict", "key_scope_conflict", "slot_identity_conflict":
			status, message = http.StatusConflict, "The slot or account pool conflicts with an existing assignment."
		case "slot_already_authenticated", "slot_not_ready":
			status, message = http.StatusConflict, "Refresh the slot status before continuing."
		case "invalid_slot", "invalid_slot_request", "instance_not_found", "unsupported_slot":
			status, message = http.StatusBadRequest, "Invalid onboarding request"
		}
	}
	c.JSON(status, gin.H{"success": false, "message": message})
}

// validOnboardingID accepts only canonical UUIDs so a retry always addresses the
// same durable record and the derived remote name cannot contain path syntax.
func validOnboardingID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

// onboardingBody bounds password/callback bodies before JSON decoding. The
// Origin guard and JSON content type jointly exclude cross-origin HTML forms.
func onboardingBody(c *gin.Context, target any) bool {
	if c.ContentType() != "application/json" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid onboarding request"})
		return false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if err := common.DecodeJson(c.Request.Body, target); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid onboarding request"})
		return false
	}
	return true
}

// staticSlotAssigned resolves historical quota bindings by fixed backend URL as
// well as ID. Renaming a deployment ID must not make an existing account reusable.
func staticSlotAssigned(instanceID, slotID string, client *vm2api.Client) (bool, error) {
	cfg, err := configuredQuota()
	if err != nil {
		return false, err
	}
	panelURL := strings.TrimRight(client.Config().BaseURL, "/") + "/api/panel"
	for _, binding := range cfg.Users {
		if binding.Slot != slotID {
			continue
		}
		bindingID := binding.InstanceID
		if bindingID == "" {
			bindingID = gptquota.DefaultInstanceID
		}
		url := cfg.Instances[bindingID].PanelURL
		if bindingID == gptquota.DefaultInstanceID && cfg.PanelURL != "" {
			url = cfg.PanelURL
		}
		if bindingID == instanceID || strings.TrimRight(url, "/") == panelURL {
			return true, nil
		}
	}
	return false, nil
}

// onboardingSlotView combines persisted reservations with legacy bindings; a
// failed/pending account still owns its slot until the same operation resumes.
func onboardingSlotView(actorID int, instanceID string, slot vm2api.Slot, client *vm2api.Client) (onboardingSlot, error) {
	view := onboardingSlot{ID: slot.ID, Name: slot.Name, LoggedIn: slot.HasAuth}
	assigned, err := model.IsVM2APISlotAssigned(instanceID, slot.ID)
	if err != nil {
		return view, err
	}
	legacy, err := staticSlotAssigned(instanceID, slot.ID, client)
	if err != nil {
		return view, err
	}
	allowed, err := model.CanUseVM2APISlotDraft(instanceID, slot.ID, actorID)
	if err != nil {
		return view, err
	}
	view.Assigned = assigned || legacy || !allowed
	view.Available = slot.Ready && !view.Assigned
	if view.Assigned {
		view.Reason = "Slot is already assigned"
	} else if !slot.Ready {
		view.Reason = "Slot is not ready"
	}
	return view, nil
}

// GetVM2APIOnboardingOptions exposes fixed instance choices and safe slot state.
// One unavailable VPS does not prevent opening an account on another VPS.
func GetVM2APIOnboardingOptions(c *gin.Context) {
	actor, ok := onboardingActor(c)
	if !ok {
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Onboarding service is not configured"})
		return
	}
	instances := registry.Instances()
	views := make([]onboardingInstance, len(instances))
	models := make([]string, 0)
	var wg sync.WaitGroup
	for i, instance := range instances {
		models = append(models, instance.Models...)
		wg.Go(func() {
			view := onboardingInstance{ID: instance.ID, Name: instance.Name, Slots: []onboardingSlot{}, Exits: []vm2api.Exit{}}
			defer func() { views[i] = view }()
			ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
			defer cancel()
			client, clientErr := registry.Client(instance.ID)
			if clientErr != nil {
				view.Error = "Onboarding request failed"
				return
			}
			slots, slotErr := client.ListSlots(ctx)
			if slotErr != nil {
				view.Error = "Onboarding request failed"
				return
			}
			for _, slot := range slots {
				safe, err := onboardingSlotView(actor, instance.ID, slot, client)
				if err != nil {
					view.Error = "Onboarding request failed"
					return
				}
				view.Slots = append(view.Slots, safe)
			}
			exits, exitErr := client.ListExits(ctx)
			if exitErr != nil {
				view.Error = "Onboarding request failed"
				return
			}
			view.Exits, view.Available = exits, true
		})
	}
	wg.Wait()
	slices.Sort(models)
	models = slices.Compact(models)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"instances": views, "models": models, "api_base_url": strings.TrimRight(system_setting.ServerAddress, "/") + "/v1"}})
}

// CreateVM2APIAccount reserves intent before any remote mutation. A lost HTTP
// response can be recovered with the request ID without recreating a user.
func CreateVM2APIAccount(c *gin.Context) {
	actor, ok := onboardingActor(c)
	if !ok {
		return
	}
	var input onboardingRequest
	if !onboardingBody(c, &input) {
		return
	}
	input.Username, input.DisplayName = strings.TrimSpace(input.Username), strings.TrimSpace(input.DisplayName)
	if !validOnboardingID(input.RequestID) || !onboardingUsername.MatchString(input.Username) || !utf8.ValidString(input.DisplayName) || utf8.RuneCountInString(input.DisplayName) > 20 || !onboardingIdentifier.MatchString(input.InstanceID) || len(input.InstanceID) > 64 || !onboardingIdentifier.MatchString(input.SlotID) || input.Quota < 0 || input.Quota > 2_000_000_000 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid onboarding request"})
		return
	}
	// Check locally before reserving a slot or sending an upstream request. The
	// blocklist never receives customer data, and the exact password is hashed.
	if err := onboarding.ValidatePassword(input.Password); err != nil {
		message := "Invalid onboarding request"
		if errors.Is(err, onboarding.ErrCommonPassword) {
			message = "This password is too common. Choose a different password."
		}
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": message})
		return
	}
	if input.DisplayName == "" {
		input.DisplayName = input.Username
	}
	job, err := model.GetVM2APIEnrollment(input.RequestID, actor)
	if err == nil {
		// Never replace the persisted password on a replay. Completed operations
		// contain no password hash and may be read by their original root actor.
		if job.Username != input.Username || job.DisplayName != input.DisplayName || job.InstanceID != input.InstanceID || job.SlotID != input.SlotID || job.Quota != input.Quota || (job.Status != "completed" && !common.ValidatePasswordAndHash(input.Password, job.PasswordHash)) {
			onboardingError(c, model.ErrVM2APIEnrollmentConflict)
			return
		}
		runVM2APIEnrollment(c, job)
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		onboardingError(c, err)
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		onboardingError(c, err)
		return
	}
	client, err := registry.Client(input.InstanceID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	slot, err := client.GetSlot(c.Request.Context(), input.SlotID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	view, err := onboardingSlotView(actor, input.InstanceID, slot, client)
	if err != nil {
		onboardingError(c, err)
		return
	}
	if !view.Available {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": view.Reason})
		return
	}
	hash, err := common.HashAccountPassword(input.Password)
	if err != nil {
		onboardingError(c, err)
		return
	}
	job, err = model.ReserveVM2APIEnrollment(&model.VM2APIEnrollment{RequestID: input.RequestID, ActorID: actor, Username: input.Username, DisplayName: input.DisplayName, PasswordHash: hash, InstanceID: input.InstanceID, SlotID: input.SlotID, Quota: input.Quota})
	if err != nil {
		onboardingError(c, err)
		return
	}
	// Another identical request may have reserved the row while hashing. Verify
	// the saved hash as well, rather than reporting success for a different password.
	if job.Status != "completed" && !common.ValidatePasswordAndHash(input.Password, job.PasswordHash) {
		onboardingError(c, model.ErrVM2APIEnrollmentConflict)
		return
	}
	input.Password = ""
	runVM2APIEnrollment(c, job)
}

// runVM2APIEnrollment serializes retries with a database lease. Remote keys are
// deterministic and reusable after timeouts; the user/channel binding commits
// in one local transaction and the browser never receives the upstream key.
func runVM2APIEnrollment(c *gin.Context, job *model.VM2APIEnrollment) {
	if job.Status == "completed" {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": job})
		return
	}
	job, lease, err := model.ClaimVM2APIEnrollment(job.RequestID, job.ActorID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	if lease == "" {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": job})
		return
	}
	// The context expires before the lease, so a new worker cannot race a still
	// active remote request. A process crash leaves a retryable expired lease.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	finished := false
	requestID := job.RequestID
	defer func() {
		if !finished {
			_ = model.FailVM2APIEnrollment(requestID, lease, "Onboarding request failed")
		}
	}()
	registry, err := onboardingRegistry()
	if err != nil {
		onboardingError(c, err)
		return
	}
	client, err := registry.Client(job.InstanceID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	legacy, err := staticSlotAssigned(job.InstanceID, job.SlotID, client)
	if err != nil || legacy {
		onboardingError(c, model.ErrVM2APIEnrollmentConflict)
		return
	}
	slot, err := client.GetSlot(ctx, job.SlotID)
	if err != nil || !slot.Ready {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Slot is not ready"})
		return
	}
	key, err := client.EnsurePoolKey(ctx, job.SlotID, "na-"+job.RequestID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	cfg := client.Config()
	job, err = model.CompleteVM2APIEnrollment(job.RequestID, lease, cfg.BaseURL, strings.Join(cfg.Models, ","), key.Key, key.PoolID, key.KeyID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	finished = true
	model.InitChannelCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": job})
}

// GetVM2APIAccount restores only the current root actor's operation; possession
// of a UUID alone is insufficient to inspect another administrator's workflow.
func GetVM2APIAccount(c *gin.Context) {
	actor, ok := onboardingActor(c)
	if !ok {
		return
	}
	job, err := model.GetVM2APIEnrollment(c.Param("request_id"), actor)
	if err != nil {
		onboardingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": job})
}

// RetryVM2APIAccount resumes durable intent without sending or storing the
// customer's plaintext password a second time.
func RetryVM2APIAccount(c *gin.Context) {
	actor, ok := onboardingActor(c)
	if !ok {
		return
	}
	job, err := model.GetVM2APIEnrollment(c.Param("request_id"), actor)
	if err != nil {
		onboardingError(c, err)
		return
	}
	runVM2APIEnrollment(c, job)
}

// CreateVM2APIOnboardingSlot records a stable remote ID before creating a Docker
// container. Retrying after a lost reply verifies the same slot instead of adding one.
func CreateVM2APIOnboardingSlot(c *gin.Context) {
	actor, ok := onboardingActor(c)
	if !ok {
		return
	}
	var input struct {
		RequestID  string `json:"request_id"`
		InstanceID string `json:"instance_id"`
		Name       string `json:"name"`
		ExitID     string `json:"exit_id"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validOnboardingID(input.RequestID) || !onboardingIdentifier.MatchString(input.InstanceID) || len(input.InstanceID) > 64 || !onboardingIdentifier.MatchString(input.ExitID) || input.Name == "" || !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) > 60 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid onboarding request"})
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		onboardingError(c, err)
		return
	}
	client, err := registry.Client(input.InstanceID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	draft, err := model.ReserveVM2APISlotDraft(&model.VM2APISlotDraft{RequestID: input.RequestID, ActorID: actor, InstanceID: input.InstanceID, Name: input.Name, ExitID: input.ExitID})
	if err != nil {
		onboardingError(c, err)
		return
	}
	draft, lease, err := model.ClaimVM2APISlotDraft(draft.RequestID, actor)
	if err != nil {
		onboardingError(c, err)
		return
	}
	defer func() { _ = model.ReleaseVM2APISlotDraft(draft.RequestID, lease) }()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	slot, err := client.CreateSlot(ctx, vm2api.CreateSlotRequest{ID: draft.SlotID, Name: draft.Name, ExitID: draft.ExitID})
	if err != nil {
		onboardingError(c, err)
		return
	}
	view, err := onboardingSlotView(actor, draft.InstanceID, slot, client)
	if err != nil {
		onboardingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"request_id": draft.RequestID, "slot": view}})
}

// VM2APIOnboardingOAuth starts or finishes official authorization. The wrapper
// checks slot ownership on both steps and only returns a safe slot view.
func VM2APIOnboardingOAuth(c *gin.Context) {
	actor, ok := onboardingActor(c)
	if !ok {
		return
	}
	identity, ok := middleware.GetStepUpIdentity(c)
	if !ok || identity.SessionID == "" {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Authentication required"})
		return
	}
	var input struct {
		InstanceID  string `json:"instance_id"`
		SlotID      string `json:"slot_id"`
		SessionID   string `json:"session_id"`
		CallbackURL string `json:"callback_url"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	if !onboardingIdentifier.MatchString(input.InstanceID) || len(input.InstanceID) > 64 || !onboardingIdentifier.MatchString(input.SlotID) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid onboarding request"})
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		onboardingError(c, err)
		return
	}
	client, err := registry.Client(input.InstanceID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	slot, err := client.GetSlot(c.Request.Context(), input.SlotID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	view, err := onboardingSlotView(actor, input.InstanceID, slot, client)
	if err != nil {
		onboardingError(c, err)
		return
	}
	if view.Assigned || view.LoggedIn {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Slot is already assigned"})
		return
	}
	if strings.HasSuffix(c.FullPath(), "/start") {
		session, err := registry.StartOAuth(c.Request.Context(), actor, input.InstanceID, input.SlotID, identity.SessionID)
		if err != nil {
			onboardingError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": session})
		return
	}
	slot, err = registry.FinishOAuth(c.Request.Context(), actor, input.InstanceID, input.SlotID, input.SessionID, input.CallbackURL, identity.SessionID)
	if err != nil {
		onboardingError(c, err)
		return
	}
	view, err = onboardingSlotView(actor, input.InstanceID, slot, client)
	if err != nil {
		onboardingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"slot": view}})
}
