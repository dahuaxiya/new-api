package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/stretchr/testify/require"
)

// TestRetailSupplyGroupsDatabases covers fresh/upgrade schema, independent credentials,
// address inheritance and fail-closed enrollment on real supported database engines.
func TestRetailSupplyGroupsDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			endpoint := "https://aixingxing.pro"
			parent := Channel{Name: "Site", Key: "parent-secret", BaseURL: &endpoint, Type: 1, Models: "model-a", Group: "retail", Status: 1}
			require.NoError(t, parent.Insert())
			// The last release has no site relationship column. Preserve its channel and abilities.
			require.NoError(t, db.Migrator().DropColumn(&Channel{}, "retail_site_id"))
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			require.True(t, db.Migrator().HasIndex(&Channel{}, "idx_channels_retail_site_id"))
			var legacy Channel
			require.NoError(t, db.First(&legacy, parent.Id).Error)
			require.Equal(t, parent.Key, legacy.Key)
			require.Zero(t, legacy.RetailSiteID)
			cfgPath := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": parent.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfgPath, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfgPath)
			ctx := context.Background()
			id, err := CreateRetailSupplyGroup(ctx, parent.Id, "Normal", "group-secret", "model-a\n model-b")
			require.NoError(t, err)
			var child Channel
			require.NoError(t, db.First(&child, id).Error)
			require.Equal(t, parent.Id, child.RetailSiteID)
			require.Equal(t, "group-secret", child.Key)
			require.Equal(t, "model-a,model-b", child.Models)
			require.Equal(t, endpoint, child.GetBaseURL())
			ids, err := RetailSupplyIDs("retail")
			require.NoError(t, err)
			require.ElementsMatch(t, []int{parent.Id, id}, ids)
			candidates, err := RetailBindingChannels()
			require.NoError(t, err)
			require.Len(t, candidates, 2, "a group is usable in pricing without editing deployment files")
			now := time.Now().Unix()
			feed := &retailprice.PublicRateSnapshot{Source: "https://aixingxing.pro/api/public/rates", UpdatedAt: now, FetchedAt: now, Groups: []retailprice.PublicRate{
				{Group: "normal", Name: "Normal", Multiplier: "0.12"},
				{Group: "pro", Name: "Pro", Multiplier: "0.30"},
			}}
			bindings := []retailprice.GroupBinding{
				{LocalGroup: "default", ChannelID: parent.Id, UpstreamGroup: "normal"},
				{LocalGroup: "existing", ChannelID: id, UpstreamGroup: "pro"},
			}
			_, err = SaveRetailGroupBindings(ctx, 0, bindings, true, feed, 1, retailprice.SalePolicy{ReceiptRatio: "1", MarginBPS: 2000})
			require.NoError(t, err, "separate site credentials can follow different upstream group rates")
			book, _, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, bindings, book.GroupBindings)
			require.Equal(t, 2000, book.MarginBPS)
			attemptID, err := BeginRetailAttempt("group-attempt", id)
			require.NoError(t, err)
			var attempt RetailAttempt
			require.NoError(t, db.First(&attempt, attemptID).Error)
			require.Equal(t, id, attempt.ChannelID, "procurement records retain the selected group's identity")
			var abilities int64
			require.NoError(t, db.Model(&Ability{}).Where("channel_id = ?", id).Count(&abilities).Error)
			require.EqualValues(t, 2, abilities)
			_, err = CreateRetailSupplyGroup(ctx, parent.Id, "Normal", "other-key", "model-a")
			require.Error(t, err, "duplicate group names cannot create ambiguous credentials")
			_, err = CreateRetailSupplyGroup(ctx, parent.Id, "Same Key", parent.Key, "model-a")
			require.Error(t, err)
			_, err = CreateRetailSupplyGroup(ctx, id, "Nested", "nested-key", "model-a")
			require.Error(t, err, "groups cannot authorize further descendants")
			draftID, err := CreateRetailSupplyGroup(ctx, parent.Id, "Draft", "", "model-a")
			require.NoError(t, err)
			var draft Channel
			require.NoError(t, db.First(&draft, draftID).Error)
			require.Empty(t, draft.Key, "missing credentials must not fall back to the site Key")
			require.Equal(t, common.ChannelStatusManuallyDisabled, draft.Status)
			// A child credential cannot be redirected independently through the generic channel editor.
			evil := "https://other.example"
			require.Error(t, (&Channel{Id: id, BaseURL: &evil}).Update())
			require.NoError(t, db.First(&child, id).Error)
			require.Equal(t, endpoint, child.GetBaseURL())
			changed := endpoint + "/v1"
			require.NoError(t, (&Channel{Id: parent.Id, BaseURL: &changed}).Update())
			require.NoError(t, db.First(&child, id).Error)
			require.Equal(t, changed, child.GetBaseURL())
			require.Equal(t, "group-secret", child.Key, "site edits never rotate a group's credential")
			require.Error(t, (&Channel{Id: id, Key: parent.Key}).Update(), "rotation must not reuse another group's credential")
			// Even a direct database edit cannot borrow enrollment from a mismatched site.
			require.NoError(t, db.Model(&child).Update("base_url", evil).Error)
			ids, err = RetailSupplyIDs("retail")
			require.NoError(t, err)
			require.NotContains(t, ids, id)
			reserved, err := RetailReservedSupplyIDs()
			require.NoError(t, err)
			require.Contains(t, reserved, id, "mismatched groups must remain isolated from non-retail Keys")
			require.NoError(t, db.Model(&child).Update("base_url", changed).Error)
			// Occupying a parent for hosting revokes all derived public supply groups.
			require.NoError(t, db.Create(&GPTHostingBinding{ChannelID: parent.Id, UserID: 991}).Error)
			ids, err = RetailSupplyIDs("retail")
			require.NoError(t, err)
			require.Empty(t, ids)
			_, err = CreateRetailSupplyGroup(ctx, parent.Id, "Private", "private-key", "model-a")
			require.Error(t, err)
			require.NoError(t, db.Where("channel_id = ?", parent.Id).Delete(&GPTHostingBinding{}).Error)
			// An ability-write failure must roll back both the new channel and shared-address edits.
			require.NoError(t, db.Migrator().DropTable(&Ability{}))
			_, err = CreateRetailSupplyGroup(ctx, parent.Id, "Rollback", "rollback-key", "model-a")
			require.Error(t, err)
			var rolledBack int64
			require.NoError(t, db.Model(&Channel{}).Where("name = ?", "Rollback").Count(&rolledBack).Error)
			require.Zero(t, rolledBack)
			require.Error(t, (&Channel{Id: parent.Id, BaseURL: &endpoint}).Update())
			require.NoError(t, db.First(&child, id).Error)
			require.Equal(t, changed, child.GetBaseURL())
		})
	}
}
