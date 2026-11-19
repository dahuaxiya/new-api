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
			// The legacy NULL-parent site is visible, but its child cannot consume a page or tag.
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", parent.Id).Update("tag", "site-tag").Error)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", id).Update("tag", "child-tag").Error)
			var listed int64
			require.NoError(t, ChannelSiteList(db.Model(&Channel{})).Count(&listed).Error)
			require.EqualValues(t, 1, listed)
			matching, err := SearchChannels(endpoint, "retail", "model-a", true)
			require.NoError(t, err)
			require.Len(t, matching, 1)
			require.Equal(t, parent.Id, matching[0].Id)
			tags, err := SearchTags(endpoint, "retail", "model-a", true)
			require.NoError(t, err)
			require.Len(t, tags, 1)
			require.Equal(t, "site-tag", *tags[0])
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

// TestRetailSupplyConfigurationDatabases verifies that credentials, model abilities, margin and
// multiplier bindings either commit together or roll back together on all supported engines.
func TestRetailSupplyConfigurationDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			endpoint := "https://aixingxing.pro"
			parent := Channel{Name: "Site", Key: "site-key", BaseURL: &endpoint, Type: 1, Models: "old-model", Group: "retail,default", Status: 1}
			require.NoError(t, parent.Insert())
			cfgPath := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": parent.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfgPath, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfgPath)
			ctx := context.Background()
			now := time.Now().Unix()
			feed := &retailprice.PublicRateSnapshot{Source: "https://aixingxing.pro/api/public/rates", UpdatedAt: now, FetchedAt: now, Groups: []retailprice.PublicRate{{Group: "pro", Name: "Pro", Multiplier: "0.3"}}}
			bindings := []retailprice.GroupBinding{{LocalGroup: "default", ChannelID: -1, UpstreamGroup: "pro"}}
			edits := []RetailSupplyEdit{{ID: -1, SiteID: parent.Id, Name: "Pro", Key: "pro-key", Models: "model-a\nmodel-b"}}
			policy := &retailprice.SalePolicy{ReceiptRatio: "1", MarginBPS: 2500}
			version, err := SaveRetailSupplyConfiguration(ctx, 0, bindings, true, feed, 1, policy, edits)
			require.NoError(t, err)
			book, _, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, 2500, book.MarginBPS)
			id := book.GroupBindings[0].ChannelID
			require.Positive(t, id)
			require.Equal(t, -1, bindings[0].ChannelID, "the browser draft must remain retryable")
			var ch Channel
			require.NoError(t, db.First(&ch, id).Error)
			require.Equal(t, "pro-key", ch.Key)
			require.Equal(t, "model-a,model-b", ch.Models)
			require.Equal(t, parent.Id, ch.RetailSiteID)
			bindings = book.GroupBindings
			// A channel-only edit still invalidates other editors; blank Key preserves the credential.
			version2, err := SaveRetailSupplyConfiguration(ctx, version, bindings, true, feed, 1, policy, []RetailSupplyEdit{{ID: id, SiteID: parent.Id, Name: "Pro renamed", Models: "model-c"}})
			require.NoError(t, err)
			require.Greater(t, version2, version)
			require.NoError(t, db.First(&ch, id).Error)
			require.Equal(t, "pro-key", ch.Key)
			require.Equal(t, "model-c", ch.Models)
			_, err = SaveRetailSupplyConfiguration(ctx, version, bindings, true, feed, 1, policy, []RetailSupplyEdit{{ID: id, SiteID: parent.Id, Name: "Stale", Key: "stale-key", Models: "stale-model"}})
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			// Invalid upstream mapping after a credential update rolls the whole transaction back.
			bad := []retailprice.GroupBinding{{LocalGroup: "default", ChannelID: id, UpstreamGroup: "missing"}}
			_, err = SaveRetailSupplyConfiguration(ctx, version2, bad, true, feed, 1, policy, []RetailSupplyEdit{{ID: id, SiteID: parent.Id, Name: "Bad", Key: "changed-key", Models: "bad-model"}})
			require.Error(t, err)
			require.NoError(t, db.First(&ch, id).Error)
			require.Equal(t, "Pro renamed", ch.Name)
			require.Equal(t, "pro-key", ch.Key)
			var abilities []Ability
			require.NoError(t, db.Where("channel_id = ?", id).Find(&abilities).Error)
			require.Len(t, abilities, 1)
			require.Equal(t, "model-c", abilities[0].Model)
			_, err = SaveRetailSupplyConfiguration(ctx, version2, bindings, true, feed, 1, policy, []RetailSupplyEdit{{ID: id, SiteID: parent.Id, Name: "Pro", Key: parent.Key, Models: "model-c"}})
			require.ErrorIs(t, err, ErrRetailSupplyGroupKey)
			// The root site's legacy comma-separated group list is valid and editable.
			rootBinding := append(append([]retailprice.GroupBinding{}, bindings...), retailprice.GroupBinding{LocalGroup: "existing", ChannelID: parent.Id, UpstreamGroup: "pro"})
			version3, err := SaveRetailSupplyConfiguration(ctx, version2, rootBinding, true, feed, 1, policy, []RetailSupplyEdit{{ID: parent.Id, SiteID: parent.Id, Name: "Site renamed", Models: "old-model"}})
			require.NoError(t, err)
			// Simulate the payload from before credential ownership was recorded. Removing all
			// customer routes must retain both upstream identities when the editor reopens.
			legacy, _, err := GetRetailPriceBook()
			require.NoError(t, err)
			legacy.CredentialGroups = nil
			payload, err := common.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, db.Model(&RetailPriceState{}).Where("id = ?", 1).Update("payload", string(payload)).Error)
			version4, err := SaveRetailGroupBindings(ctx, version3, nil, false, feed, 1)
			require.NoError(t, err)
			reopened, _, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, map[int]string{id: "pro", parent.Id: "pro"}, reopened.RegisteredGroups())
			feed.Groups = append(feed.Groups, retailprice.PublicRate{Group: "cheap", Multiplier: "0.01"})
			wrong := []retailprice.GroupBinding{{ChannelID: id, LocalGroup: "default", UpstreamGroup: "cheap"}}
			_, err = SaveRetailSupplyConfiguration(ctx, version4, wrong, true, feed, 1, nil, []RetailSupplyEdit{{ID: id, SiteID: parent.Id, Name: "Must roll back", Models: "model-x"}})
			require.Error(t, err)
			require.NoError(t, db.First(&ch, id).Error)
			require.Equal(t, "Pro renamed", ch.Name)
			// An unbound saved Key can be paused. Its sibling and abilities stay independent.
			off, on := false, true
			edit := RetailSupplyEdit{ID: id, SiteID: parent.Id, Name: ch.Name, Models: ch.Models, Enabled: &off}
			version5, err := SaveRetailSupplyConfiguration(ctx, version4, nil, false, feed, 1, nil, []RetailSupplyEdit{edit})
			require.NoError(t, err)
			require.NoError(t, db.First(&ch, id).Error)
			require.Equal(t, common.ChannelStatusManuallyDisabled, ch.Status)
			var sibling Channel
			require.NoError(t, db.First(&sibling, parent.Id).Error)
			require.Equal(t, common.ChannelStatusEnabled, sibling.Status)
			var available int64
			require.NoError(t, db.Model(&Ability{}).Where("channel_id = ? AND enabled = ?", id, true).Count(&available).Error)
			require.Zero(t, available)
			edit.Enabled, edit.Models = &on, ""
			_, err = SaveRetailSupplyConfiguration(ctx, version5, bindings, true, feed, 1, nil, []RetailSupplyEdit{edit})
			require.ErrorIs(t, err, ErrRetailSupplyGroupIncomplete)
			edit.Models = ch.Models
			_, err = SaveRetailSupplyConfiguration(ctx, version5, bindings, true, feed, 1, nil, []RetailSupplyEdit{edit})
			require.NoError(t, err)
			require.NoError(t, db.Model(&Ability{}).Where("channel_id = ? AND enabled = ?", id, true).Count(&available).Error)
			require.Positive(t, available)
		})
	}
}

// TestRetailModelSyncAtomicPublication exercises the DB boundary shared by the scheduler and admission.
// A stale editor or rotated credential must not install an ability even if the model list looked valid.
func TestRetailModelSyncAtomicPublication(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			endpoint := "https://aixingxing.pro"
			ch := Channel{Name: "Test site", Key: "test-only", BaseURL: &endpoint, Type: 1, Models: "already-listed", Group: "retail,default", Status: 1}
			require.NoError(t, ch.Insert())
			path := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": ch.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", path)
			now := time.Now().Unix()
			b := retailprice.GroupBinding{LocalGroup: "default", ChannelID: ch.Id, UpstreamGroup: "up", FollowModels: true}
			ctx := context.Background()
			v, err := SaveRetailGroupBindings(ctx, 0, []retailprice.GroupBinding{b}, true, &retailprice.PublicRateSnapshot{Source: "https://aixingxing.pro/api/public/rates", FetchedAt: now, UpdatedAt: now, Groups: []retailprice.PublicRate{{Group: "up", Multiplier: "0.12"}}}, 1)
			require.NoError(t, err)
			// Seed two verified, synthetic tariffs rather than depending on the expiry of a real catalog.
			price := retailprice.OfficialModels()[0]
			price.ID = "already-listed"
			price.OfficialModel = price.ID
			price.VerifiedAt = now - 30
			price.ValidUntil = now + 3600
			next := price
			next.ID = "new-model"
			next.OfficialModel = next.ID
			book := retailprice.Book{Enabled: true, WalletCNYPerUnit: "1", ReceiptRatio: "1", Models: []retailprice.Model{price, next}, Products: []retailprice.Product{{ID: "manual", Name: "manual", UserGroups: []string{"default"}, Routes: []retailprice.Route{{ChannelID: ch.Id, Models: map[string]string{price.ID: price.ID}}}}}}
			v, err = PublishRetailPriceBook(v, book, nil, 1)
			require.NoError(t, err)
			src, err := RetailModelListSource(db, ch.Id)
			require.NoError(t, err)
			_, err = ApplyRetailModelList(ctx, v-1, ch.Id, src, []string{next.ID})
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			var n int64
			abilities := db.Model(&Ability{}).Where("channel_id = ? AND model = ?", ch.Id, next.ID)
			require.NoError(t, abilities.Count(&n).Error)
			require.Zero(t, n)
			_, err = ApplyRetailModelList(ctx, v, ch.Id, src, []string{next.ID})
			require.NoError(t, err)
			book, v, err = GetRetailPriceBook()
			require.NoError(t, err)
			require.Len(t, book.Products, 2)
			require.NoError(t, abilities.Count(&n).Error)
			require.Positive(t, n)
			policy := retailprice.KeyPolicy{ProductID: book.Products[1].ID, GroupCurrent: true}
			q, err := book.MakeQuote(v, "default", policy, next.ID, 1, []int{ch.Id}, now)
			require.NoError(t, err)
			require.Equal(t, []int{ch.Id}, q.Channels)
			_, err = ApplyRetailModelList(ctx, v, ch.Id, src, []string{next.ID})
			require.NoError(t, err)
			_, again, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, v, again, "unchanged models must not invalidate active quotes")
			require.NoError(t, db.Model(&ch).Update("key", "replacement-test-key").Error)
			_, err = ApplyRetailModelList(ctx, v, ch.Id, src, []string{"stale"})
			require.ErrorIs(t, err, ErrRetailPriceConflict)
		})
	}
}
