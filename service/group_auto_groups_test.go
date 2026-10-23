package service

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configureRequestAutoGroupsTest(t *testing.T) {
	t.Helper()
	originalMax := setting.GetMaxTokenAutoGroups()
	originalAutoGroups := setting.AutoGroups2JsonString()
	originalUsableGroups := setting.UserUsableGroups2JSONString()
	originalRatios := ratio_setting.GroupRatio2JSONString()
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("2"))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip","default","svip"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","vip":"VIP","svip":"SVIP"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1,"svip":1}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateMaxTokenAutoGroups(fmt.Sprintf("%d", originalMax)))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(originalAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(originalUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(originalRatios))
	})
}

func newRequestAutoGroupsContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func TestGetRequestAutoGroupsInheritedListIsNotLimited(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Equal(t, []string{"vip", "default", "svip"}, groups)
}

func TestGetRequestAutoGroupsFiltersBeforeApplyingCurrentLimit(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"revoked", "vip", "default", "svip"})
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Equal(t, []string{"vip", "default"}, groups)
	require.NoError(t, setting.UpdateMaxTokenAutoGroups("1"))
	assert.Equal(t, []string{"vip"}, GetRequestAutoGroups(ctx, "default"))
}

func TestGetRequestAutoGroupsDoesNotFallBackAfterPermissionChange(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	ctx := newRequestAutoGroupsContext()
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip"})
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))

	groups := GetRequestAutoGroups(ctx, "default")

	assert.Empty(t, groups)
}

// TestVM2APIDedicatedGroupRejectsSharedRoutes 验证专属账号不能通过公开组、特殊加组或旧 Auto 快照走共享渠道。
func TestVM2APIDedicatedGroupRejectsSharedRoutes(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	const own = "acct_0123456789abcdef0123456789abcdef"
	const other = "acct_abcdef0123456789abcdef0123456789"
	special := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
	previousSpecial := special.ReadAll()
	t.Cleanup(func() {
		special.Clear()
		special.AddAll(previousSpecial)
	})
	special.Set(own, map[string]string{"+:vip": "VIP", "+:future-public": "Future", "auto": "Auto"})
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(fmt.Sprintf(`{"default":1,"vip":1,"future-public":1,%q:1,%q:1}`, own, other)))

	for _, publicGroups := range []string{
		`{"default":"Default","vip":"VIP","auto":"Auto"}`,
		fmt.Sprintf(`{"default":"Default","vip":"VIP","auto":"Auto","future-public":"Future",%q:"Other account"}`, other),
	} {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(publicGroups))
		assert.Equal(t, map[string]string{own: "User group"}, GetUserUsableGroups(own))
		// TokenAuth 直接检查该权限集合；playground 和 Auto 候选过滤复用相同规则。
		for _, target := range []string{"default", "vip", "future-public", "auto", other} {
			assert.False(t, GroupInUserUsableGroups(own, target), target)
			assert.False(t, IsUserSelectableGroup(own, target), target)
		}
		assert.True(t, IsUserSelectableGroup(own, own))
	}

	require.NoError(t, setting.UpdateAutoGroupsByJsonString(fmt.Sprintf(`["vip","future-public",%q]`, other)))
	ctx := newRequestAutoGroupsContext()
	assert.Empty(t, GetRequestAutoGroups(ctx, own))
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, own)
	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", "future-public", other})
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)
	assert.Empty(t, GetRequestAutoGroups(ctx, own))
	// 即使内部调用直接要求 Auto，也必须在查找渠道前拒绝已失去权限的候选，不能回退到全局列表。
	retry := 0
	channel, _, err := CacheGetRandomSatisfiedChannel(&RetryParam{Ctx: ctx, TokenGroup: "auto", ModelName: "gpt-test", Retry: &retry})
	require.Error(t, err)
	assert.Nil(t, channel)

	common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"vip", own, other})
	assert.Equal(t, []string{own}, GetRequestAutoGroups(ctx, own))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(fmt.Sprintf(`["vip",%q,"future-public"]`, own)))
	assert.Equal(t, []string{own}, GetUserAutoGroup(own))
}

// TestVM2APIDedicatedGroupPreservesOrdinaryPermissions 验证只收紧完整保留格式，普通组仍应用原来的公开和特殊权限。
func TestVM2APIDedicatedGroupPreservesOrdinaryPermissions(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	special := ratio_setting.GetGroupRatioSetting().GroupSpecialUsableGroup
	previousSpecial := special.ReadAll()
	t.Cleanup(func() {
		special.Clear()
		special.AddAll(previousSpecial)
	})
	special.Set("legacy-customer", map[string]string{"-:vip": "", "+:custom": "Custom"})
	assert.Equal(t, map[string]string{
		"default": "Default", "svip": "SVIP", "custom": "Custom", "legacy-customer": "User group",
	}, GetUserUsableGroups("legacy-customer"))
	assert.Equal(t, []string{"vip", "default", "svip"}, GetUserAutoGroup("default"))

	for _, ordinary := range []string{
		"", "default", "acct_team", "acct_0123456789abcdef0123456789abcde",
		"acct_0123456789abcdef0123456789abcdef0", "acct_0123456789ABCDEF0123456789ABCDEF",
		"prefix_acct_0123456789abcdef0123456789abcdef", "acct_0123456789abcdef0123456789abcdef_suffix",
	} {
		assert.True(t, GroupInUserUsableGroups(ordinary, "vip"), ordinary)
	}
}
