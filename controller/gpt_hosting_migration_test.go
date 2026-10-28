package controller

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/stretchr/testify/require"
)

// TestValidPreparedMigrationIdentity 验证目标授权必须属于同一官方账号、名额和绑定编号。
func TestValidPreparedMigrationIdentity(t *testing.T) {
	accountHash, subjectHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	binding := &model.GPTHostingBinding{
		BindingID: "binding", MigrationTargetSlotID: "target-slot", AccountHash: &accountHash, SubjectHash: &subjectHash,
	}
	valid := vm2api.HostedState{IdentitySource: "official", BindingID: "binding", VMID: "target-slot", State: "prepared", AccountHash: accountHash, SubjectHash: subjectHash}
	require.True(t, validPreparedMigrationIdentity(binding, valid))
	for name, mutate := range map[string]func(*vm2api.HostedState){
		"identity source": func(state *vm2api.HostedState) { state.IdentitySource = "legacy_binding" },
		"binding":         func(state *vm2api.HostedState) { state.BindingID = "other" },
		"slot":            func(state *vm2api.HostedState) { state.VMID = "other" },
		"account":         func(state *vm2api.HostedState) { state.AccountHash = strings.Repeat("c", 64) },
		"subject":         func(state *vm2api.HostedState) { state.SubjectHash = strings.Repeat("d", 64) },
		"state":           func(state *vm2api.HostedState) { state.State = "reserved" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			require.False(t, validPreparedMigrationIdentity(binding, candidate))
		})
	}
	require.False(t, validPreparedMigrationIdentity(nil, valid))
}
