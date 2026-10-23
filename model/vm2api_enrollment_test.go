package model

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useVM2APIEnrollmentDatabase 使用专门的隔离数据库验证真实方言，调用方不得传入生产数据库。
// 每个方言串行切换全局模型连接，SQLite 使用本地文件，远端 DSN 由显式测试环境变量提供。
func useVM2APIEnrollmentDatabase(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	oldDB, oldLogDB := DB, LOG_DB
	oldType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldPath := common.SQLitePath
	oldOptions, oldRatios := maps.Clone(common.OptionMap), ratio_setting.GroupRatio2JSONString()
	dsn := "local"
	if dialect == "sqlite" {
		common.SQLitePath = filepath.Join(t.TempDir(), "enrollment.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	} else {
		dsn = os.Getenv("VM2API_ENROLLMENT_TEST_" + strings.ToUpper(dialect) + "_DSN")
		if dsn == "" {
			t.Skip("dedicated enrollment test database DSN is not configured")
		}
		// 测试会清理隔离库，名字检查防止误把常规应用连接串当成测试库使用。
		require.Contains(t, dsn, "enrollment_test")
	}
	t.Setenv("VM2API_ENROLLMENT_TEST_DSN", dsn)
	db, dbType, err := chooseDB("VM2API_ENROLLMENT_TEST_DSN", false)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(6)
	var version string
	versionSQL := "SELECT VERSION()"
	if dialect == "sqlite" {
		versionSQL = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
	t.Logf("database=%s version=%s", dialect, version)
	DB, LOG_DB = db, db
	common.SetDatabaseTypes(dbType, dbType)
	common.OptionMap = make(map[string]string)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"existing":0.75}`))
	initCol()
	t.Cleanup(func() {
		_ = sqlDB.Close()
		DB, LOG_DB = oldDB, oldLogDB
		common.SetDatabaseTypes(oldType, oldLogType)
		common.SQLitePath = oldPath
		common.OptionMap = oldOptions
		_ = ratio_setting.UpdateGroupRatioByJSONString(oldRatios)
		initCol()
	})
	// 只在专用测试库内清理；所有表由本次测试创建，没有共享生产数据。
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	for _, table := range tables {
		if strings.HasPrefix(table, "sqlite_") {
			continue
		}
		require.NoError(t, db.Migrator().DropTable(table))
	}
	return db
}

// newVM2APIEnrollmentInput 创建拥有真实密码哈希的独立请求，避免测试依赖插入时的隐式哈希或赠额。
func newVM2APIEnrollmentInput(t *testing.T, name, instance, slot string) *VM2APIEnrollment {
	t.Helper()
	hash, err := common.HashAccountPassword("onboarding-test-password")
	require.NoError(t, err)
	return &VM2APIEnrollment{RequestID: uuid.NewString(), ActorID: 1, Username: name, DisplayName: name,
		PasswordHash: hash, InstanceID: instance, SlotID: slot, Quota: 12345}
}

// TestVM2APIEnrollmentDatabases 在真实三种数据库验证新安装、已有数据升级、幂等、租约及事务原子性。
func TestVM2APIEnrollmentDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			// 调用生产迁移入口证明新安装会实际注册两个表；随后重复迁移检查无数据损坏。
			require.NoError(t, migrateDB())
			require.True(t, db.Migrator().HasTable(&VM2APIEnrollment{}))
			require.True(t, db.Migrator().HasTable(&VM2APISlotDraft{}))
			require.NoError(t, migrateDB())

			// 代表升级前的 schema 与本分支基线 1647a177 一致，仅不存在新增开户表。
			require.NoError(t, db.Migrator().DropTable(&VM2APIEnrollment{}, &VM2APISlotDraft{}))
			legacy := User{Username: "legacy-owner", Password: "legacy-preserved-hash", AffCode: "legacy-aff", Quota: 4321, Group: "existing"}
			require.NoError(t, db.Create(&legacy).Error)
			oldChannel := Channel{Name: "legacy-channel", Type: 1, Key: "legacy-key", Group: "existing", Models: "legacy-model", Status: 1}
			require.NoError(t, db.Create(&oldChannel).Error)
			require.NoError(t, oldChannel.AddAbilities(db))
			require.NoError(t, db.Create(&Option{Key: "GroupRatio", Value: `{"default":1,"existing":0.75}`}).Error)
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			var preserved User
			require.NoError(t, db.First(&preserved, legacy.Id).Error)
			assert.Equal(t, legacy.Password, preserved.Password)
			assert.Equal(t, legacy.Quota, preserved.Quota)
			var existingChannel Channel
			require.NoError(t, db.First(&existingChannel, oldChannel.Id).Error)
			assert.Equal(t, oldChannel.Key, existingChannel.Key)
			assert.Equal(t, []string{"legacy-model"}, GetGroupEnabledModels("existing"))
			recorder := &migrationSQLRecorder{}
			require.NoError(t, db.Session(&gorm.Session{Logger: recorder}).AutoMigrate(&VM2APIEnrollment{}, &VM2APISlotDraft{}))
			assert.Empty(t, recorder.schemaMutations(), "repeated enrollment migration must not rewrite schema")

			t.Run("reservation_and_lease", testVM2APIReservationAndLease)
			t.Run("transaction_completion", testVM2APICompletion)
			t.Run("concurrent_ownership", testVM2APIConcurrentOwnership)
			t.Run("slot_draft", testVM2APISlotDraft)
		})
	}
}

// testVM2APIReservationAndLease 验证重放不改变数据，跨管理员与冲突绑定被拒绝，过期工作器不能提交。
func testVM2APIReservationAndLease(t *testing.T) {
	input := newVM2APIEnrollmentInput(t, "reserved-user", "cc", "slot-reserved")
	for _, invalidQuota := range []int{-1, common.MaxWalletQuota + 1} {
		invalid := *input
		invalid.Quota = invalidQuota
		_, err := ReserveVM2APIEnrollment(&invalid)
		require.ErrorIs(t, err, ErrVM2APIEnrollmentConflict)
	}
	job, err := ReserveVM2APIEnrollment(input)
	require.NoError(t, err)
	assert.Equal(t, "acct_"+strings.ReplaceAll(input.RequestID, "-", ""), job.Group)
	assert.Equal(t, VM2APIEnrollmentPending, job.Status)
	var repeated = *input
	repeated.PasswordHash, err = common.HashAccountPassword("onboarding-test-password")
	require.NoError(t, err)
	_, err = ReserveVM2APIEnrollment(&repeated)
	require.NoError(t, err)
	repeated.ActorID = 2
	_, err = ReserveVM2APIEnrollment(&repeated)
	require.ErrorIs(t, err, ErrVM2APIEnrollmentConflict)
	_, err = GetVM2APIEnrollment(input.RequestID, 2)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = GetVM2APIEnrollmentForUser(0)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	assigned, err := IsVM2APISlotAssigned("cc", input.SlotID)
	require.NoError(t, err)
	assert.True(t, assigned)
	_, lease, err := ClaimVM2APIEnrollment(input.RequestID, 1)
	require.NoError(t, err)
	require.NotEmpty(t, lease)
	_, _, err = ClaimVM2APIEnrollment(input.RequestID, 1)
	require.ErrorIs(t, err, ErrVM2APIEnrollmentBusy)
	require.ErrorIs(t, FailVM2APIEnrollment(input.RequestID, "wrong-token", "upstream secret"), ErrVM2APIEnrollmentLease)
	require.NoError(t, DB.Model(&VM2APIEnrollment{}).Where("request_id = ?", input.RequestID).Update("lease_until", time.Now().Unix()-1).Error)
	_, replacement, err := ClaimVM2APIEnrollment(input.RequestID, 1)
	require.NoError(t, err)
	assert.NotEqual(t, lease, replacement)
	require.ErrorIs(t, FailVM2APIEnrollment(input.RequestID, lease, "old worker"), ErrVM2APIEnrollmentLease)
	require.NoError(t, FailVM2APIEnrollment(input.RequestID, replacement, "secret-upstream-key"))
	failed, err := GetVM2APIEnrollment(input.RequestID, 1)
	require.NoError(t, err)
	assert.Equal(t, VM2APIEnrollmentFailed, failed.Status)
	assert.NotContains(t, failed.Message, "secret")
	serialized, err := common.Marshal(failed)
	require.NoError(t, err)
	assert.NotContains(t, string(serialized), input.PasswordHash)
	assert.NotContains(t, string(serialized), replacement)

	deleted := User{Username: "deleted-owner", Password: "old", AffCode: "deleted-owner-aff"}
	require.NoError(t, DB.Create(&deleted).Error)
	require.NoError(t, DB.Delete(&deleted).Error)
	_, err = ReserveVM2APIEnrollment(newVM2APIEnrollmentInput(t, deleted.Username, "cc", "unused"))
	require.ErrorIs(t, err, ErrVM2APIEnrollmentConflict)
}

// testVM2APICompletion 验证用户、渠道、能力、倍率和绑定一起提交；错误不会遗留半个账号或重复赠额。
func testVM2APICompletion(t *testing.T) {
	input := newVM2APIEnrollmentInput(t, "complete-user", "bwh", "slot-complete")
	input.Quota = 1 << 40 // 验证实际数据库保留 64 位钱包额度，不被 32 位列截断。
	// 模拟会展开参数的 Debug SQL logger，业务写入必须主动阻止密码哈希和上游 Key 进入它。
	previousDB := DB
	recorder := &migrationSQLRecorder{}
	DB = DB.Session(&gorm.Session{Logger: recorder})
	t.Cleanup(func() {
		DB = previousDB
		recorder.mu.Lock()
		logged := strings.Join(recorder.statements, "\n")
		recorder.mu.Unlock()
		assert.NotContains(t, logged, input.PasswordHash)
		assert.NotContains(t, logged, "private-upstream-key")
	})
	job, err := ReserveVM2APIEnrollment(input)
	require.NoError(t, err)
	_, lease, err := ClaimVM2APIEnrollment(job.RequestID, 1)
	require.NoError(t, err)
	// 用损坏倍率模拟事务后半段失败，验证前半段用户和渠道写入确实回滚。
	require.NoError(t, DB.Model(&Option{}).Where(Option{Key: "GroupRatio"}).Update("value", "malformed").Error)
	_, err = CompleteVM2APIEnrollment(job.RequestID, lease, "https://panel.example", "gpt-test,gpt-second", "private-upstream-key", "pool-id", "key-id")
	require.ErrorIs(t, err, ErrVM2APIEnrollmentConflict)
	var users, channels int64
	require.NoError(t, DB.Model(&User{}).Where("username = ?", input.Username).Count(&users).Error)
	require.NoError(t, DB.Model(&Channel{}).Where("name = ?", "vm2api-"+input.Username).Count(&channels).Error)
	assert.Zero(t, users)
	assert.Zero(t, channels)
	assert.False(t, ratio_setting.ContainsGroupRatio(job.Group))
	require.NoError(t, DB.Model(&Option{}).Where(Option{Key: "GroupRatio"}).Update("value", `{"default":1,"existing":0.75}`).Error)
	completed, err := CompleteVM2APIEnrollment(job.RequestID, lease, "https://panel.example", "gpt-test,gpt-second", "private-upstream-key", "pool-id", "key-id")
	require.NoError(t, err)
	assert.Equal(t, VM2APIEnrollmentCompleted, completed.Status)
	assert.Empty(t, completed.PasswordHash)
	assert.Empty(t, completed.LeaseToken)
	var user User
	require.NoError(t, DB.First(&user, completed.UserID).Error)
	assert.Equal(t, input.PasswordHash, user.Password)
	assert.True(t, common.ValidatePasswordAndHash("onboarding-test-password", user.Password))
	assert.Equal(t, input.Quota, user.Quota)
	assert.Equal(t, common.RoleCommonUser, user.Role)
	assert.Equal(t, completed.Group, user.Group)
	assert.NotEmpty(t, user.GetSetting().SidebarModules)
	var channel Channel
	require.NoError(t, DB.First(&channel, completed.ChannelID).Error)
	assert.Equal(t, completed.Group, channel.Group)
	assert.Equal(t, "private-upstream-key", channel.Key)
	assert.ElementsMatch(t, []string{"gpt-test", "gpt-second"}, GetGroupEnabledModels(user.Group))
	assert.Equal(t, 1.0, ratio_setting.GetGroupRatio(user.Group))
	assert.Equal(t, 0.75, ratio_setting.GetGroupRatio("existing"))
	bound, err := GetVM2APIEnrollmentForUser(user.Id)
	require.NoError(t, err)
	assert.Equal(t, "bwh", bound.InstanceID)
	assert.Equal(t, input.SlotID, bound.SlotID)
	replayed, token, err := ClaimVM2APIEnrollment(input.RequestID, 1)
	require.NoError(t, err)
	assert.Empty(t, token)
	assert.Equal(t, completed.UserID, replayed.UserID)
	require.ErrorIs(t, FailVM2APIEnrollment(job.RequestID, lease, "stale failure"), ErrVM2APIEnrollmentLease)
}

// testVM2APIConcurrentOwnership 验证并发预留只能有一个槽位所有者，并发租约只能发给一个工作器。
func testVM2APIConcurrentOwnership(t *testing.T) {
	first := newVM2APIEnrollmentInput(t, "race-first", "cc", "same-slot")
	second := newVM2APIEnrollmentInput(t, "race-second", "cc", "same-slot")
	inputs := []*VM2APIEnrollment{first, second}
	var wg sync.WaitGroup
	results := make([]*VM2APIEnrollment, len(inputs))
	errorsSeen := make([]error, len(inputs))
	start := make(chan struct{})
	for i := range inputs {
		wg.Go(func() { <-start; results[i], errorsSeen[i] = ReserveVM2APIEnrollment(inputs[i]) })
	}
	close(start)
	wg.Wait()
	var owner *VM2APIEnrollment
	successes := 0
	for i, err := range errorsSeen {
		if err == nil {
			successes++
			owner = results[i]
		} else {
			assert.ErrorIs(t, err, ErrVM2APIEnrollmentConflict)
		}
	}
	require.Equal(t, 1, successes)
	require.NotNil(t, owner)
	leaseErrors := make([]error, 2)
	start = make(chan struct{})
	for i := range leaseErrors {
		wg.Go(func() { <-start; _, _, leaseErrors[i] = ClaimVM2APIEnrollment(owner.RequestID, owner.ActorID) })
	}
	close(start)
	wg.Wait()
	successes = 0
	for _, err := range leaseErrors {
		if err == nil {
			successes++
		} else {
			assert.ErrorIs(t, err, ErrVM2APIEnrollmentBusy)
		}
	}
	assert.Equal(t, 1, successes)
	// 同名槽位在另一个实例中是不同资源，可以独立开户。
	other := newVM2APIEnrollmentInput(t, "race-other-node", "bwh", "same-slot")
	_, err := ReserveVM2APIEnrollment(other)
	require.NoError(t, err)
	// 不同实例也不能使用同一个门户用户名，唯一约束在数据库层生效。
	duplicateName := newVM2APIEnrollmentInput(t, owner.Username, "third", "different-slot")
	_, err = ReserveVM2APIEnrollment(duplicateName)
	require.ErrorIs(t, err, ErrVM2APIEnrollmentConflict)
}

// testVM2APISlotDraft 验证远端槽位名确定、跨管理员隔离，以及旧租约无法释放接管后的新租约。
func testVM2APISlotDraft(t *testing.T) {
	input := &VM2APISlotDraft{RequestID: uuid.NewString(), ActorID: 1, InstanceID: "bwh", Name: "New slot", ExitID: "egress-1"}
	draft, err := ReserveVM2APISlotDraft(input)
	require.NoError(t, err)
	assert.Equal(t, "na-"+input.RequestID, draft.SlotID)
	for _, check := range []struct {
		instance, slot string
		actor          int
		allowed        bool
	}{{"bwh", draft.SlotID, 1, true}, {"bwh", draft.SlotID, 2, false}, {"bwh", "existing-slot", 2, true}, {"other", draft.SlotID, 2, true}, {"bwh", draft.SlotID, 0, false}} {
		allowed, err := CanUseVM2APISlotDraft(check.instance, check.slot, check.actor)
		require.NoError(t, err)
		assert.Equal(t, check.allowed, allowed)
	}
	_, err = ReserveVM2APISlotDraft(input)
	require.NoError(t, err)
	changed := *input
	changed.ExitID = "other-egress"
	_, err = ReserveVM2APISlotDraft(&changed)
	require.ErrorIs(t, err, ErrVM2APIEnrollmentConflict)
	_, _, err = ClaimVM2APISlotDraft(input.RequestID, 2)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound))
	_, lease, err := ClaimVM2APISlotDraft(input.RequestID, 1)
	require.NoError(t, err)
	_, _, err = ClaimVM2APISlotDraft(input.RequestID, 1)
	require.ErrorIs(t, err, ErrVM2APIEnrollmentBusy)
	require.NoError(t, DB.Model(&VM2APISlotDraft{}).Where("request_id = ?", input.RequestID).Update("lease_until", time.Now().Unix()-1).Error)
	_, freshLease, err := ClaimVM2APISlotDraft(input.RequestID, 1)
	require.NoError(t, err)
	require.ErrorIs(t, ReleaseVM2APISlotDraft(input.RequestID, lease), ErrVM2APIEnrollmentLease)
	require.NoError(t, ReleaseVM2APISlotDraft(input.RequestID, freshLease))
	_, _, err = ClaimVM2APISlotDraft(input.RequestID, 1)
	require.NoError(t, err)
}
