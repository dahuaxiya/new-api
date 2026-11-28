package model

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
			// A sibling binding sharing the same credential is removable until its own product references it.
			extra := b
			extra.LocalGroup = "existing"
			extra.FollowModels = false
			feed := book.PublicRateSync.Snapshot
			v, err = SaveRetailGroupBindings(ctx, v, []retailprice.GroupBinding{b, extra}, true, feed, 1)
			require.NoError(t, err)
			_, err = SaveRetailGroupBindings(ctx, v, []retailprice.GroupBinding{extra}, true, feed, 1)
			require.ErrorIs(t, err, retailprice.ErrAccess)
			v, err = SaveRetailGroupBindings(ctx, v, []retailprice.GroupBinding{b}, true, feed, 1)
			require.NoError(t, err)
			// Two complete observations pause the missing model. Switching to manual then selecting it must restore quotation.
			for range 2 {
				_, err = ApplyRetailModelList(ctx, v, ch.Id, src, []string{"other"})
				require.NoError(t, err)
				book, v, err = GetRetailPriceBook()
				require.NoError(t, err)
			}
			require.Contains(t, book.ModelSync[b.SyncID()].Paused, next.ID)
			_, err = book.MakeQuote(v, "default", policy, next.ID, 1, []int{ch.Id}, now)
			require.ErrorIs(t, err, retailprice.ErrExpired)
			b.FollowModels = false
			v, err = SaveRetailSupplyConfiguration(ctx, v, []retailprice.GroupBinding{b}, true, feed, 1, nil, []RetailSupplyEdit{{ID: ch.Id, SiteID: ch.Id, Name: ch.Name, Models: next.ID}})
			require.NoError(t, err)
			book, v, err = GetRetailPriceBook()
			require.NoError(t, err)
			targets, err := RetailDisplayTargets()
			require.NoError(t, err)
			_, err = book.MakeQuote(v, "default", policy, next.ID, 1, targets.Eligible(book.Products[1], next.ID), now)
			require.NoError(t, err)
			require.NoError(t, db.Model(&ch).Update("key", "replacement-test-key").Error)
			_, err = ApplyRetailModelList(ctx, v, ch.Id, src, []string{"stale"})
			require.ErrorIs(t, err, ErrRetailPriceConflict)
		})
	}
}

// TestRetailInventoryGroupLaunch checks that launch and later model following use the actual Kiro
// inventory Key and protocol. Changing consent must not change its tariff, mappings or sibling groups.
func TestRetailInventoryGroupLaunch(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			t.Setenv("CRYPTO_SECRET", "inventory-route-test")
			ctx := context.Background()
			endpoint := "https://aixingxing.pro"
			root := Channel{Name: "Kiro owner", Key: "kiro-secret", BaseURL: &endpoint, Type: 14, Models: "old-unavailable", Group: "retail", Status: 1}
			require.NoError(t, root.Insert())
			cfg := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": root.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfg, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			now := time.Now().Unix()
			feed := &retailprice.PublicRateSnapshot{Source: endpoint + "/api/public/rates", UpdatedAt: now, FetchedAt: now, Groups: []retailprice.PublicRate{{Group: "kiro", Multiplier: "0.45"}, {Group: "cursor", Multiplier: "0.35"}}}
			version, err := SaveRetailGroupBindings(ctx, 0, []retailprice.GroupBinding{{LocalGroup: "default", ChannelID: root.Id, UpstreamGroup: "kiro"}}, true, feed, 1)
			require.NoError(t, err)
			price := retailprice.OfficialModels()[0]
			price.ID = "claude-opus-5-5"
			price.OfficialModel = price.ID
			price.VerifiedAt = now - 30
			price.ValidUntil = now + 3600
			old := retailprice.Product{ID: "existing", Name: "legacy", UserGroups: []string{"default"}, Routes: []retailprice.Route{{ChannelID: root.Id, Models: map[string]string{price.ID: price.ID}}}}
			version, err = PublishRetailPriceBook(version, retailprice.Book{Enabled: true, WalletCNYPerUnit: "1", ReceiptRatio: "1", Models: []retailprice.Model{price}, Products: []retailprice.Product{old}}, nil, 1)
			require.NoError(t, err)
			view, err := ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			group := "kiro"
			id, err := EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "create", Stamp: view.Stamp, Name: "Kiro", Key: root.Key, OwnerSiteID: root.Id, UpstreamGroup: &group}, 1)
			require.NoError(t, err)
			margin := 2000
			input := RetailGroupLaunch{Version: version, Group: "claude-normal", LibraryID: id, Margin: &margin}
			discover := func(_ context.Context, channelType int, base, secret string) ([]string, error) {
				require.Equal(t, constant.ChannelTypeAnthropic, channelType)
				require.Equal(t, endpoint, base)
				require.Equal(t, "kiro-secret", secret)
				return []string{price.ID, "unknown-model"}, nil
			}
			conflict := input
			conflict.Upstream = "cursor"
			_, err = LaunchRetailGroupKeys(ctx, conflict, 1, discover)
			require.ErrorIs(t, err, retailprice.ErrConfig)
			review, err := LaunchRetailGroupKeys(ctx, input, 1, discover)
			require.NoError(t, err)
			require.NotEmpty(t, review.Review)
			require.Equal(t, "0.5625", review.SaleMultiplier)
			var unchanged Channel
			require.NoError(t, db.First(&unchanged, root.Id).Error)
			require.Zero(t, unchanged.RetailKeyID)
			require.Equal(t, root.Models, unchanged.Models)
			input.Review = review.Review
			applied, err := LaunchRetailGroupKeys(ctx, input, 1, discover)
			require.NoError(t, err)
			require.True(t, applied.Applied)
			book, _, err := GetQuotableRetailPriceBook()
			require.NoError(t, err)
			product := book.Products[1]
			factors, err := RetailGroupSaleMultipliers(book)
			require.NoError(t, err)
			require.Equal(t, map[string]string{product.ID: review.SaleMultiplier}, factors, "legacy products must not borrow an anchored price")
			require.Equal(t, id, product.RateSourceKeyID)
			require.Equal(t, id, product.Routes[0].LibraryID)
			require.Equal(t, map[string]string{price.ID: price.ID}, product.Routes[0].Models)
			require.Equal(t, old, book.Products[0])
			require.NoError(t, db.First(&unchanged, root.Id).Error)
			require.Empty(t, unchanged.Key)
			require.Equal(t, id, unchanged.RetailKeyID)
			book, version, err = GetRetailPriceBook()
			require.NoError(t, err)
			optIn := book.Binding("claude-normal", root.Id)
			require.NotNil(t, optIn)
			require.False(t, optIn.FollowModels)
			optIn.FollowModels = true
			version, err = SaveRetailSupplyConfiguration(ctx, version, book.GroupBindings, true, feed, 1, nil, nil)
			require.NoError(t, err)
			src, err := RetailModelListSource(db, root.Id)
			require.NoError(t, err)
			require.Equal(t, id, src.RetailKeyID)
			require.Equal(t, constant.ChannelTypeAnthropic, src.Type)
			require.Equal(t, "kiro-secret", src.Key)
			_, err = ApplyRetailModelList(ctx, version, root.Id, src, []string{price.ID, "unpriced-claude"})
			require.NoError(t, err)
			book, version, err = GetRetailPriceBook()
			require.NoError(t, err)
			require.True(t, book.Binding("claude-normal", root.Id).FollowModels)
			require.False(t, book.Binding("default", root.Id).FollowModels)
			require.Equal(t, []string{"unpriced-claude"}, book.ModelSync[optIn.SyncID()].Pending)
			// A protocol or owner change while the list is in flight cannot publish the old catalog.
			stale := *src
			stale.Type = constant.ChannelTypeOpenAI
			_, err = ApplyRetailModelList(ctx, version, root.Id, &stale, []string{"incorrect-model"})
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			_, err = ApplyRetailModelList(ctx, version, root.Id, src, nil)
			require.NoError(t, err)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			require.True(t, book.ModelSync[optIn.SyncID()].Failed)
			require.Empty(t, book.ModelSync[optIn.SyncID()].Paused)
			require.Equal(t, product.Routes, book.Products[1].Routes)
			// 用同一把 Key 切换采购组，预先存在的报价不得被改写，缺价时两边都必须回滚。
			editGroup := func(group string, confirm string) error {
				view, err := ReadRetailKeyLibrary(ctx)
				require.NoError(t, err)
				_, err = EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "edit", ID: id, Name: "Kiro", UpstreamGroup: &group, Stamp: view.Stamp, ConfirmGroups: map[int]string{root.Id: confirm}}, 1)
				return err
			}
			require.NoError(t, editGroup("cursor", "kiro"))
			require.Error(t, editGroup("not-listed", "cursor"))
			book, _, err = GetQuotableRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, "cursor", book.Binding("claude-normal", root.Id).UpstreamGroup)
			rate, err := book.BoundRate(*book.Binding("claude-normal", root.Id))
			require.NoError(t, err)
			require.Equal(t, "0.35", rate.Multiplier)
			factors, err = RetailGroupSaleMultipliers(book)
			require.NoError(t, err)
			require.Equal(t, "0.4375", factors[product.ID], "editing the Key binding changes the displayed sale factor")
			view, err = ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			require.Equal(t, "cursor", view.Keys[0].UpstreamGroup)
			unlisted := "provider-pilot"
			_, err = EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "create", OwnerSiteID: root.Id, Stamp: view.Stamp, Name: "Spare", Key: "spare-key", UpstreamGroup: &unlisted}, 1)
			require.NoError(t, err, "an unused Key can be filed before its tariff appears in the feed")
			// 移除库存引用后，即使调用方持有上一次的可选渠道集合，运行时报价也必须拒绝。
			require.NoError(t, db.Table("channels").Where("id = ?", root.Id).Update("retail_key_id", 0).Error)
			book, _, err = GetQuotableRetailPriceBook()
			require.NoError(t, err)
			require.True(t, book.UnavailableRateChannels[root.Id])
			factors, err = RetailGroupSaleMultipliers(book)
			require.NoError(t, err)
			require.NotContains(t, factors, product.ID, "revoked sources must never keep showing their cached rate")
			_, err = book.MakeQuote(applied.Version, "default", retailprice.KeyPolicy{ProductID: product.ID, GroupCurrent: true}, price.ID, 1, []int{root.Id}, now)
			require.Error(t, err)
			// 旧版把同一个凭据复制到了另一协议站点。只有同目的地、同凭据且人工停用的子线路允许接管。
			second := Channel{Name: "legacy owner", BaseURL: &endpoint, Type: 1, Group: "retail", Status: 1}
			require.NoError(t, second.Insert())
			raw, err = common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": root.Id, "kind": "reseller"}, {"channel_id": second.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfg, raw, 0600))
			child := Channel{Name: "old copy", RetailSiteID: second.Id, Group: "retail", Type: 1, BaseURL: &endpoint, Key: "some-other-secret", Status: common.ChannelStatusEnabled}
			require.NoError(t, child.Insert())
			view, err = ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			spareID := view.Keys[len(view.Keys)-1].ID
			adopt := func(destination string) error {
				state, err := ReadRetailKeyLibrary(ctx)
				require.NoError(t, err)
				_, err = EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "adopt_inline", ID: spareID, Channel: child.Id, Destination: destination, Stamp: state.Stamp}, 1)
				return err
			}
			require.Error(t, adopt(endpoint))
			require.NoError(t, db.Model(&child).Update("status", common.ChannelStatusManuallyDisabled).Error)
			require.Error(t, adopt(endpoint))
			require.NoError(t, db.Model(&child).Update("key", "spare-key").Error)
			require.Error(t, adopt("https://another.example"))
			require.NoError(t, adopt(endpoint))
			require.NoError(t, db.First(&child, child.Id).Error)
			require.Equal(t, root.Id, child.RetailSiteID)
			require.Equal(t, spareID, child.RetailKeyID)
			require.Equal(t, common.ChannelStatusManuallyDisabled, child.Status)
			require.Empty(t, child.Key)
		})
	}
}

// TestRetailAnthropicSiteConsent exercises an enrolled external reseller and a separate child Key.
// Turning on its saved binding cannot duplicate it, use the parent Key or revive paused supply.
func TestRetailAnthropicSiteConsent(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			ctx := t.Context()
			endpoint := "https://anthropic-supplier.example/api/v1"
			parent := Channel{Name: "Claude site", Key: "site-test-key", BaseURL: &endpoint, Type: constant.ChannelTypeAnthropic, Models: "claude-test", Group: "retail", Status: common.ChannelStatusEnabled}
			require.NoError(t, parent.Insert())
			cfg := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": parent.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfg, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			child, err := CreateRetailSupplyGroup(ctx, parent.Id, "Kiro", "child-test-key", "claude-test")
			require.NoError(t, err)
			now := time.Now().Unix()
			url := "https://anthropic-supplier.example/api/public/rates"
			binding := retailprice.GroupBinding{LocalGroup: "default", ChannelID: child, UpstreamGroup: "kiro"}
			siteFeed := retailprice.SiteRateFeed{URL: url, Snapshot: &retailprice.PublicRateSnapshot{Source: url, UpdatedAt: now, FetchedAt: now, Groups: []retailprice.PublicRate{{Group: "kiro", Multiplier: "0.45"}}}}
			v, err := SaveRetailSiteRates(ctx, 0, parent.Id, retailprice.SiteRateFeed{}, siteFeed, 1)
			require.NoError(t, err)
			// The group is already bound via site-specific pricing; the legacy editor cannot create such a route.
			v, err = mutateRetailPriceBook(ctx, v, 1, false, func(book *retailprice.Book) error {
				book.GroupBindings = []retailprice.GroupBinding{binding}
				return nil
			})
			require.NoError(t, err)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", child).Update("status", common.ChannelStatusManuallyDisabled).Error)
			binding.FollowModels = true
			v, err = SaveRetailSupplyConfiguration(ctx, v, []retailprice.GroupBinding{binding}, false, nil, 1, nil, nil)
			require.NoError(t, err)
			book, _, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, []retailprice.GroupBinding{binding}, book.GroupBindings)
			require.Equal(t, siteFeed, book.SiteFeeds[parent.Id])
			_, err = RetailModelListSource(db, child)
			require.ErrorIs(t, err, ErrRetailModelSourcePaused)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", child).Update("status", common.ChannelStatusEnabled).Error)
			src, err := RetailModelListSource(db, child)
			require.NoError(t, err)
			require.Equal(t, "child-test-key", src.Key)
			require.Equal(t, endpoint, src.GetBaseURL())
			binding.FollowModels = false
			v, err = SaveRetailSupplyConfiguration(ctx, v, []retailprice.GroupBinding{binding}, false, nil, 1, nil, nil)
			require.NoError(t, err)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", child).Update("header_override", `{"x-api-key":"other-test-key"}`).Error)
			_, err = RetailModelListSource(db, child)
			require.ErrorIs(t, err, retailprice.ErrAccess)
			binding.FollowModels = true
			_, err = SaveRetailSupplyConfiguration(ctx, v, []retailprice.GroupBinding{binding}, false, nil, 1, nil, nil)
			require.ErrorIs(t, err, retailprice.ErrAccess)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", child).Update("header_override", nil).Error)
			require.NoError(t, db.Create(&GPTHostingBinding{SeatID: "model-source-seat", BindingID: "model-source-seat", ChannelID: parent.Id}).Error)
			_, err = RetailModelListSource(db, child)
			require.ErrorIs(t, err, retailprice.ErrAccess)
			rows, err := RetailRateSiteChannels(db)
			require.NoError(t, err)
			require.False(t, slices.ContainsFunc(rows, func(row RetailBindingChannel) bool { return row.ID == child && row.ModelSyncSupported }))
		})
	}
}

func TestRetailGroupLaunchDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			endpoint := "https://aixingxing.pro"
			site := Channel{Name: "Anthropic", Key: "site-key", BaseURL: &endpoint, Type: 14, Models: "claude-current", Group: "retail", Status: 1}
			require.NoError(t, site.Insert())
			poolEndpoint := "https://pool.example.com"
			pool := Channel{Name: "Own pool", Key: "pool-key", BaseURL: &poolEndpoint, Type: 1, Models: "claude-current", Group: "retail", Status: 1}
			require.NoError(t, pool.Insert())
			path := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": site.Id, "kind": "reseller"}, {"channel_id": pool.Id, "kind": "subscription"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", path)
			now := time.Now().Unix()
			ctx := context.Background()
			feed := &retailprice.PublicRateSnapshot{Source: "https://aixingxing.pro/api/public/rates", UpdatedAt: now, FetchedAt: now, Groups: []retailprice.PublicRate{{Group: "a", Multiplier: "0.5"}, {Group: "b", Multiplier: "0.5"}}}
			binding := retailprice.GroupBinding{LocalGroup: "default", ChannelID: site.Id, UpstreamGroup: "a"}
			v, err := SaveRetailGroupBindings(ctx, 0, []retailprice.GroupBinding{binding}, true, feed, 1)
			require.NoError(t, err)
			price := retailprice.OfficialModels()[0]
			price.ID = "claude-current"
			price.OfficialModel = price.ID
			price.VerifiedAt = now - 30
			price.ValidUntil = now + 3600
			legacy := retailprice.Product{ID: "legacy", Name: "legacy", UserGroups: []string{"default"}, Routes: []retailprice.Route{{ChannelID: site.Id, Models: map[string]string{price.ID: price.ID}}}}
			v, err = PublishRetailPriceBook(v, retailprice.Book{Enabled: true, WalletCNYPerUnit: "1", ReceiptRatio: "1", Models: []retailprice.Model{price}, Products: []retailprice.Product{legacy}, Rates: []retailprice.Rate{
				{ID: "pool-standard", Multiplier: "0.55", VerifiedAt: now - 30, ValidUntil: now + 3600},
				{ID: "pool-cheap", Multiplier: "0.15", VerifiedAt: now - 30, ValidUntil: now + 3600},
			}}, nil, 1)
			require.NoError(t, err)
			margin := 2000
			in := RetailGroupLaunch{Version: v, Site: site.Id, Group: "claude-normal", Upstream: "b", Models: []string{price.ID, "claude-unknown"}, Key: "new-claude-key", Margin: &margin, Routes: []RetailLaunchRoute{
				{ChannelID: pool.Id, UpstreamGroup: "accounts", RateID: "pool-standard"},
			}}
			preview, err := LaunchRetailGroup(ctx, in, 1)
			require.NoError(t, err)
			require.NotEmpty(t, preview.Review)
			require.False(t, preview.Applied)
			require.Len(t, preview.Rows, 2)
			require.Equal(t, "1.25", preview.Rows[0].Tiers[0].Prices.Input)
			require.Equal(t, "0.625", preview.SaleMultiplier)
			require.ElementsMatch(t, []int{0, pool.Id}, preview.Rows[0].Channels)
			require.Equal(t, "official_price_missing_or_expired", preview.Rows[1].Reason)
			var n int64
			require.NoError(t, db.Model(&Channel{}).Where("retail_site_id = ?", site.Id).Count(&n).Error)
			require.Zero(t, n)
			in.Review = preview.Review
			margin = 3000
			_, err = LaunchRetailGroup(ctx, in, 1)
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			margin = 2000
			// Direct publish uses the locked quote and cannot duplicate a credential on retry.
			in.Review, in.Apply = "", true
			result, err := LaunchRetailGroup(ctx, in, 1)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.Equal(t, v+1, result.Version)
			_, err = LaunchRetailGroup(ctx, in, 1)
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			final, version, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, result.Version, version)
			require.Equal(t, legacy, final.Products[0])
			require.Zero(t, final.MarginBPS)
			require.Len(t, final.Products, 2)
			product := final.Products[1]
			require.True(t, product.Public)
			require.Equal(t, in.Group, product.BindingGroup)
			require.Len(t, product.Routes[0].Models, 1)
			targets, err := RetailDisplayTargets()
			require.NoError(t, err)
			quote, err := final.MakeQuote(version, "default", retailprice.KeyPolicy{GroupCurrent: true, ProductID: product.ID}, price.ID, 1, targets.Eligible(product, price.ID), now)
			require.NoError(t, err)
			require.Equal(t, preview.Rows[0].Tiers, quote.Sale)
			require.ElementsMatch(t, []int{product.RateSourceChannelID, pool.Id}, quote.Channels)
			poolQuote, err := final.MakeQuote(version, "default", retailprice.KeyPolicy{GroupCurrent: true, ProductID: product.ID}, price.ID, 1, []int{pool.Id}, now)
			require.NoError(t, err)
			require.Equal(t, quote.Sale, poolQuote.Sale)

			// 更新上游定价后，过贵的号池退出候选，分组仍沿用同一售价来源。
			feed.Groups[1].Multiplier = "0.4"
			version, err = SyncRetailGroupRates(ctx, version, feed, false)
			require.NoError(t, err)
			final, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			repriced, err := final.MakeQuote(version, "default", retailprice.KeyPolicy{GroupCurrent: true, ProductID: product.ID}, price.ID, 1, quote.Channels, now)
			require.NoError(t, err)
			require.Equal(t, "1", repriced.Sale[0].Prices.Input)
			require.Equal(t, []int{product.RateSourceChannelID}, repriced.Channels)

			// 定价来源可单独保留，编辑线路后已有 Key 继续引用原商品 ID。
			feed.Groups[1].Multiplier = "0.5"
			version, err = SyncRetailGroupRates(ctx, version, feed, false)
			require.NoError(t, err)
			edit := RetailGroupLaunch{Version: version, ProductID: product.ID, Site: site.Id,
				Channel: product.RateSourceChannelID, Group: product.BindingGroup, Upstream: "b",
				Margin: &margin, PricingOnly: true, Routes: in.Routes}
			review, err := LaunchRetailGroup(ctx, edit, 1)
			require.NoError(t, err)
			require.Equal(t, []int{pool.Id}, review.Rows[0].Channels)
			edit.Review = review.Review
			_, err = LaunchRetailGroup(ctx, edit, 1)
			require.NoError(t, err)
			final, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, product.ID, final.Products[1].ID)
			require.Equal(t, product.RateSourceChannelID, final.Products[1].RateSourceChannelID)
			require.Len(t, final.Products[1].Routes, 1)
			require.Equal(t, pool.Id, final.Products[1].Routes[0].ChannelID)
			// 同一采购分组由两个本站组复用，第二个直接继承已核验采购费率；显式写入低价必须失败。
			final, version, err = GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, "pool-standard", final.SiteGroupRates[pool.Id]["accounts"])
			other := edit
			other.Version, other.Review, other.ProductID, other.Group = version, "", "", "another-group"
			other.Routes = []RetailLaunchRoute{{ChannelID: pool.Id, UpstreamGroup: "accounts", RateID: "pool-cheap"}}
			_, err = LaunchRetailGroup(ctx, other, 1)
			require.ErrorIs(t, err, ErrRetailGroupRateConflict)
			other.Routes[0].RateID = ""
			reused, err := LaunchRetailGroup(ctx, other, 1)
			require.NoError(t, err)
			require.NotEmpty(t, reused.Review)
			// 旧控制台也不能通过整表发布给同一供货分组换一套采购价。
			final.Products[1].Routes[0].RateID = "pool-cheap"
			_, err = PublishRetailPriceBook(version, final, nil, 1)
			require.ErrorIs(t, err, ErrRetailGroupRateConflict)
			// 上游来源停用后仍可预览只通过号池供货的设置。
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", edit.Channel).Update("status", common.ChannelStatusManuallyDisabled).Error)
			edit.Review, edit.Version = "", version
			reused, err = LaunchRetailGroup(ctx, edit, 1)
			require.NoError(t, err)
			require.NotEmpty(t, reused.Review)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", edit.Channel).Update("status", common.ChannelStatusEnabled).Error)
			// 如果只有来源线路能卖，预览反馈具体失效线路并拒绝生成可发布的确认码。
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", pool.Id).Update("models", "unknown-pool-model").Error)
			other.PricingOnly = false
			reused, err = LaunchRetailGroup(ctx, other, 1)
			require.NoError(t, err)
			require.Empty(t, reused.Review)
			require.Equal(t, []int{pool.Id}, reused.UnavailableChannels)
			other.Apply = true
			refused, err := LaunchRetailGroup(ctx, other, 1)
			require.NoError(t, err)
			require.False(t, refused.Applied)
			current, currentVersion, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, version, currentVersion)
			// Add an independent catalog tariff, then verify that the worker only patches
			// this model, preserving accounting, route ownership and manual entries.
			tracked := retailprice.OfficialModels()[0]
			tracked.VerifiedAt, tracked.ValidUntil = now-30, now+3600
			current.Models = append(current.Models, tracked)
			currentVersion, err = PublishRetailPriceBook(currentVersion, current, nil, 1)
			require.NoError(t, err)
			prices := map[string]map[string][]retailprice.Tier{retailprice.OpenAIPriceURL: {tracked.ID: tracked.Tiers}}
			report, err := SyncOfficialRetailPrices(ctx, currentVersion, prices, now)
			require.NoError(t, err)
			require.Equal(t, []string{tracked.ID}, report["verified"])
			saved, nextVersion, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, currentVersion+1, nextVersion)
			require.Equal(t, "official", saved.Models[len(saved.Models)-1].PriceMode)
			saved.Models[len(saved.Models)-1] = tracked
			require.Equal(t, current, saved)
			_, err = SyncOfficialRetailPrices(ctx, nextVersion, nil, now+1)
			require.NoError(t, err)
			_, sameVersion, err := GetRetailPriceBook()
			require.NoError(t, err)
			require.Equal(t, nextVersion, sameVersion)

			// 视频没有 Token 阶梯，仍须通过真实上架事务确认每个分辨率的售价并保存供货线路。
			video := retailprice.Model{ID: "grok-imagine-video", OfficialModel: "grok-imagine-video",
				Kind: "video", PriceMode: "manual", Currency: "USD", Source: "https://aixingxing.pro/api/pricing",
				VerifiedAt: now - 30, ValidUntil: now + 3600,
				Video: &retailprice.VideoTariff{MaxDuration: 15, Prices: map[string]string{"480p": "0.10", "720p": "0.12"}}}
			expiredVideo := video
			expiredVideo.ID, expiredVideo.OfficialModel = "expired-video", "expired-video"
			expiredVideo.VerifiedAt, expiredVideo.ValidUntil = now-3600, now-1
			saved.Models = append(saved.Models, video, expiredVideo)
			nextVersion, err = PublishRetailPriceBook(nextVersion, saved, nil, 1)
			require.NoError(t, err)
			// 使用管理 API 实际调用的库存流程。供应商目录只返回图片时，已登记视频仍可上架，
			// 过期视频、缺报价模型以及已从目录消失的普通模型均不能被顺带保留。
			t.Setenv("CRYPTO_SECRET", "video-inventory-test")
			library, err := ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			upstream := "b"
			videoKey, err := EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "create", Stamp: library.Stamp,
				OwnerSiteID: site.Id, Name: "Grok Ultra", Key: "new-grok-key", UpstreamGroup: &upstream}, 1)
			require.NoError(t, err)
			library, err = ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			_, err = EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "add_route", Stamp: library.Stamp,
				ID: videoKey, Channel: site.Id, Destination: endpoint, Name: "Grok Ultra",
				Models: strings.Join([]string{video.ID, expiredVideo.ID, tracked.ID, "unpriced-video"}, ",")}, 1)
			require.NoError(t, err)
			_, nextVersion, err = GetRetailPriceBook()
			require.NoError(t, err)
			videoLaunch := RetailGroupLaunch{Version: nextVersion, Group: "grok-ultra",
				LibraryID: videoKey, Margin: &margin}
			discoverVideos := func(_ context.Context, _ int, base, secret string) ([]string, error) {
				require.Equal(t, endpoint, base)
				require.Equal(t, "new-grok-key", secret)
				return []string{"provider-image-only"}, nil
			}
			videoPreview, err := LaunchRetailGroupKeys(ctx, videoLaunch, 1, discoverVideos)
			require.NoError(t, err)
			require.NotEmpty(t, videoPreview.Review)
			require.Len(t, videoPreview.Rows, 2)
			require.Equal(t, video.ID, videoPreview.Rows[0].Model)
			require.Empty(t, videoPreview.Rows[0].Reason)
			require.Empty(t, videoPreview.Rows[0].Tiers)
			require.Equal(t, &retailprice.VideoTariff{MaxDuration: 15,
				Prices: map[string]string{"480p": "0.0625", "720p": "0.075"}}, videoPreview.Rows[0].Video)
			videoLaunch.Review = videoPreview.Review
			videoResult, err := LaunchRetailGroupKeys(ctx, videoLaunch, 1, discoverVideos)
			require.NoError(t, err)
			require.True(t, videoResult.Applied)
			saved, nextVersion, err = GetRetailPriceBook()
			require.NoError(t, err)
			videoProduct := saved.Products[len(saved.Products)-1]
			var videoChannel Channel
			require.NoError(t, db.First(&videoChannel, videoProduct.Routes[0].ChannelID).Error)
			require.Equal(t, "grok-imagine-video,provider-image-only", videoChannel.Models)
			targets, err = RetailDisplayTargets()
			require.NoError(t, err)
			videoQuote, err := saved.MakeVideoQuote(nextVersion, videoProduct.BindingGroup,
				retailprice.KeyPolicy{GroupCurrent: true, ProductID: videoProduct.ID}, video.ID,
				retailprice.VideoRequest{Duration: 2, Resolution: "720p", AspectRatio: "16:9"},
				targets.Eligible(videoProduct, video.ID), now)
			require.NoError(t, err)
			require.Equal(t, "0.15", videoQuote.ReservedCNY)
		})
	}
}

// TestRetailSiteRatesDatabases 验证同名分组隔离、手工冻结、过期的并发写、续期版本与故障恢复。
func TestRetailSiteRatesDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			ctx := context.Background()
			one := "https://aixingxing.pro"
			two := "https://pool.example"
			first := Channel{Name: "First", Key: "first", BaseURL: &one, Type: 1, Models: "demo", Group: "retail", Status: 1}
			second := Channel{Name: "Second", Key: "second", BaseURL: &two, Type: 1, Models: "demo", Group: "retail", Status: 1}
			require.NoError(t, first.Insert())
			require.NoError(t, second.Insert())
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": first.Id, "kind": "reseller"}, {"channel_id": second.Id, "kind": "subscription"}}})
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "retail.json")
			require.NoError(t, os.WriteFile(path, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", path)
			now := time.Now().Unix()
			mk := func(origin, rate string, follow bool) retailprice.SiteRateFeed {
				url := origin + "/api/public/rates"
				return retailprice.SiteRateFeed{URL: url, Follow: follow, Snapshot: &retailprice.PublicRateSnapshot{Source: url, UpdatedAt: now - 5, FetchedAt: now - 5, Groups: []retailprice.PublicRate{{Group: "shared", Name: "shared", Multiplier: rate}}}}
			}
			a, b := mk(one, "0.4", true), mk(two, "0.2", false)
			empty := retailprice.SiteRateFeed{}
			_, err = SaveRetailSiteRates(ctx, 0, first.Id, empty, b, 1)
			require.ErrorIs(t, err, retailprice.ErrAccess)
			v, err := SaveRetailSiteRates(ctx, 0, first.Id, empty, a, 1)
			require.NoError(t, err)
			v, err = SaveRetailSiteRates(ctx, v, second.Id, empty, b, 1)
			require.NoError(t, err)
			price := retailprice.OfficialModels()[0]
			price.ID, price.OfficialModel = "demo", "demo"
			price.VerifiedAt, price.ValidUntil = now-30, now+3600
			bootstrap := retailprice.Product{ID: "boot", Name: "boot", UserGroups: []string{"default"}, Routes: []retailprice.Route{{ChannelID: first.Id, RateID: "boot", Models: map[string]string{"demo": "demo"}}}}
			v, err = PublishRetailPriceBook(v, retailprice.Book{Enabled: true, WalletCNYPerUnit: "1", ReceiptRatio: "1", Models: []retailprice.Model{price}, Products: []retailprice.Product{bootstrap}, Rates: []retailprice.Rate{{ID: "boot", Multiplier: "0.4", VerifiedAt: now - 30, ValidUntil: now + 3600}}}, nil, 1)
			require.NoError(t, err)
			input := RetailGroupLaunch{Site: second.Id, Channel: second.Id, Group: "pool", Upstream: "shared", Version: v}
			preview, err := LaunchRetailGroup(ctx, input, 1)
			require.NoError(t, err)
			require.Equal(t, "0.2", preview.SaleMultiplier)
			require.NotEmpty(t, preview.Review)
			input.Review = preview.Review
			out, err := LaunchRetailGroup(ctx, input, 1)
			require.NoError(t, err)
			v = out.Version
			renewed := mk(one, "0.4", true)
			renewed.Snapshot.FetchedAt = now
			same, err := SaveRetailSiteRates(ctx, v, first.Id, a, renewed, 0)
			require.NoError(t, err)
			require.Equal(t, v, same)
			failed := renewed
			failed.LastError = "failure"
			v, err = SaveRetailSiteRates(ctx, v, first.Id, renewed, failed, 0)
			require.NoError(t, err)
			book, _, err := GetRetailPriceBook()
			require.NoError(t, err)
			_, err = book.BoundRate(retailprice.GroupBinding{ChannelID: first.Id, UpstreamGroup: "shared"})
			require.ErrorIs(t, err, retailprice.ErrExpired)
			rate, err := book.BoundRate(retailprice.GroupBinding{ChannelID: second.Id, UpstreamGroup: "shared"})
			require.NoError(t, err)
			require.Equal(t, "0.2", rate.Multiplier)
			recovered := mk(one, "0.6", true)
			recovered.Snapshot.FetchedAt = now
			v, err = SaveRetailSiteRates(ctx, v, first.Id, failed, recovered, 0)
			require.NoError(t, err)
			frozen := recovered
			frozen.Follow = false
			v, err = SaveRetailSiteRates(ctx, v, first.Id, recovered, frozen, 1)
			require.NoError(t, err)
			_, err = SaveRetailSiteRates(ctx, v, first.Id, recovered, failed, 0)
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			rate, err = book.BoundRate(retailprice.GroupBinding{ChannelID: first.Id, UpstreamGroup: "shared"})
			require.NoError(t, err)
			require.Equal(t, "0.6", rate.Multiplier)
			// 手动冻结的采购价只能属于原域名。换供应商后，须重新读取新站点的报价。
			t.Setenv("SITE_MODE", "retail")
			changed := "https://replacement.example"
			patch := Channel{Id: first.Id, BaseURL: &changed}
			require.NoError(t, patch.Update())
			book, _, err = GetQuotableRetailPriceBook()
			require.NoError(t, err)
			_, err = book.BoundRate(retailprice.GroupBinding{ChannelID: first.Id, UpstreamGroup: "shared"})
			require.ErrorIs(t, err, retailprice.ErrExpired)

			// 将冻结站点移出部署授权后，即使只引用其价格而通过其他渠道供货，来源也必须失效。
			onlyOne, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": first.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, onlyOne, 0600))
			book, _, err = GetQuotableRetailPriceBook()
			require.NoError(t, err)
			_, err = book.BoundRate(retailprice.GroupBinding{ChannelID: second.Id, UpstreamGroup: "shared"})
			require.ErrorIs(t, err, retailprice.ErrExpired)

		})
	}
}

// TestRetailCredentialCenterDatabases exercises the first write, concurrent edits and stable routing identity on each supported engine.
func TestRetailCredentialCenterDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			endpoint := "https://supplier.example"
			parent := Channel{Name: "Site", Key: "parent-secret", BaseURL: &endpoint, Type: 1, Models: "model-a", Group: "retail", Status: 1}
			require.NoError(t, parent.Insert())
			cfg := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"channel_id": parent.Id, "kind": "reseller"}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfg, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			t.Setenv("SITE_MODE", "retail")
			ctx := context.Background()
			inventory := func() RetailCredentialInventory {
				t.Helper()
				out, err := ReadRetailCredentials(ctx)
				require.NoError(t, err)
				encoded, err := common.Marshal(out)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "secret")
				return out
			}
			initial := inventory()
			create := RetailCredentialEdit{Version: initial.Version, Stamp: initial.Stamp, Action: "create", Site: parent.Id, Name: "Fast", Group: "fast", Key: "child-secret", Models: "model-a"}
			id, err := EditRetailCredential(ctx, create, 1)
			require.NoError(t, err)
			require.Positive(t, id)
			current := inventory()
			require.Equal(t, "fast", current.Groups[id])
			require.Empty(t, current.Groups[parent.Id])
			require.Equal(t, "model-a", current.Targets["model-a"][id])
			_, err = EditRetailCredential(ctx, create, 1)
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			create.Version, create.Stamp, create.Name = current.Version, current.Stamp, "Another"
			_, err = EditRetailCredential(ctx, create, 1)
			require.ErrorIs(t, err, ErrRetailSupplyGroupKey)
			rotate := RetailCredentialEdit{Version: current.Version, Stamp: current.Stamp, Site: parent.Id, ID: id, Action: "rotate", Key: "new-secret", Group: "fast"}
			_, err = EditRetailCredential(ctx, rotate, 1)
			require.ErrorIs(t, err, retailprice.ErrConfig)
			rotate.ConfirmGroup = "fast"
			// Channel maintenance outside pricing can advance the channel stamp alone; never replay that old preview.
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", id).Update("key", "outside-secret").Error)
			_, err = EditRetailCredential(ctx, rotate, 1)
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			rotate.Stamp = inventory().Stamp
			rotated, err := EditRetailCredential(ctx, rotate, 1)
			require.NoError(t, err)
			require.Equal(t, id, rotated)
			require.Error(t, (&Channel{Id: id, Key: "stale-secret"}).Update())
			var stored Channel
			require.NoError(t, db.First(&stored, id).Error)
			require.Equal(t, "new-secret", stored.Key)
			var state RetailPriceState
			require.NoError(t, db.First(&state, 1).Error)
			published := state.Payload
			for _, action := range []string{"disable", "enable"} {
				latest := inventory()
				change := RetailCredentialEdit{Action: action, Site: parent.Id, ID: id, Version: latest.Version, Stamp: latest.Stamp}
				_, err = EditRetailCredential(ctx, change, 1)
				require.NoError(t, err)
				_, enabled := inventory().Targets["model-a"][id]
				require.Equal(t, action == "enable", enabled)
				require.NoError(t, db.First(&state, 1).Error)
				require.Equal(t, published, state.Payload)
			}
			require.NoError(t, db.Create(&GPTHostingBinding{ChannelID: parent.Id, UserID: 991}).Error)
			hidden := inventory()
			require.Empty(t, hidden.Channels)
			rotate.Version, rotate.Stamp = hidden.Version, hidden.Stamp
			_, err = EditRetailCredential(ctx, rotate, 1)
			require.ErrorIs(t, err, retailprice.ErrAccess)
		})
	}
}

// TestRetailKeyLibraryDatabases verifies independent creation, site isolation, destination pinning,
// safe adoption of inline credentials and reference-aware deletion against each supported database.
func TestRetailKeyLibraryDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			t.Setenv("CRYPTO_SECRET", "test-only-library-master")
			t.Setenv("RETAIL_RELAY_CONFIG", "")
			ctx := context.Background()
			write := func(in RetailLibraryEdit) (int, error) {
				t.Helper()
				view, err := ReadRetailKeyLibrary(ctx)
				require.NoError(t, err)
				in.Stamp = view.Stamp
				return EditRetailKeyLibrary(ctx, in, 1)
			}
			_, err := write(RetailLibraryEdit{Action: "create", Key: "test", Name: "missing site"})
			require.Error(t, err)
			var sites []Channel
			var supplies []map[string]any
			for _, endpoint := range []string{"https://one.example", "https://two.example"} {
				site := Channel{Name: endpoint, BaseURL: &endpoint, Type: 1, Group: "retail", Status: 1, Models: "model-a"}
				require.NoError(t, site.Insert())
				sites = append(sites, site)
				supplies = append(supplies, map[string]any{"kind": "reseller", "channel_id": site.Id})
			}
			cfg := filepath.Join(t.TempDir(), "retail.json")
			raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": supplies})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(cfg, raw, 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			id, err := write(RetailLibraryEdit{Action: "create", Name: "Spare", OwnerSiteID: sites[0].Id, Key: "test-secret-1"})
			require.NoError(t, err)
			var entry RetailUpstreamKey
			require.NoError(t, db.First(&entry, id).Error)
			require.NotContains(t, entry.Sealed, "test-secret")
			require.Equal(t, sites[0].Id, entry.OwnerSiteID)
			first, foreign := &sites[0], &sites[1]
			// Equal destinations must not allow a key from another root. This also covers the create-child path.
			require.NoError(t, db.Model(foreign).Update("base_url", first.GetBaseURL()).Error)
			foreign.BaseURL = first.BaseURL
			for _, action := range []string{"bind", "add_route"} {
				_, err = write(RetailLibraryEdit{Action: action, ID: id, Channel: foreign.Id, Destination: foreign.GetBaseURL(), Name: "foreign", Models: "model-a"})
				require.ErrorIs(t, err, retailprice.ErrAccess)
			}
			_, err = write(RetailLibraryEdit{Action: "bind", ID: id, Channel: first.Id, Destination: first.GetBaseURL()})
			require.NoError(t, err)
			// 库存 Key 的归属变更保留加密凭据，并成为对应线路的唯一分组来源。
			classified := "kiro"
			_, err = write(RetailLibraryEdit{Action: "edit", ID: id, Name: "Spare", UpstreamGroup: &classified})
			require.NoError(t, err)
			owned, err := ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			require.Equal(t, classified, owned.Keys[0].UpstreamGroup)
			require.Equal(t, classified, owned.Groups[first.Id])
			// Seed a reference that the old version permitted; the new API cannot create it.
			require.NoError(t, db.Table("channels").Where("id = ?", foreign.Id).Updates(map[string]any{"retail_key_id": id, "retail_key_target": foreign.supplyTarget()}).Error)
			require.NoError(t, db.First(foreign, foreign.Id).Error)
			require.NoError(t, db.First(first, first.Id).Error)
			_, err = foreign.SupplySecret(db)
			require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
			eligible, err := RetailSupplyIDs("retail")
			require.NoError(t, err)
			require.Contains(t, eligible, first.Id)
			require.NotContains(t, eligible, foreign.Id)
			// A changed destination must be removed from both catalog previews and retail selection.
			require.NoError(t, db.Table("channels").Where("id = ?", first.Id).Update("retail_key_target", "stale").Error)
			eligible, err = RetailSupplyIDs("retail")
			require.NoError(t, err)
			require.NotContains(t, eligible, first.Id)
			targets, err := RetailCatalogTargets("retail", []int{first.Id, foreign.Id})
			require.NoError(t, err)
			require.Empty(t, targets)
			require.NoError(t, db.Table("channels").Where("id = ?", first.Id).Update("retail_key_target", first.supplyTarget()).Error)

			secret, err := first.SupplySecret(db)
			require.NoError(t, err)
			require.Equal(t, "test-secret-1", secret)
			view, err := ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			require.Len(t, view.Uses[id], 2)
			// Upgrade a database that already has a shared key. Its two consumers cannot tell us who issued it.
			require.NoError(t, db.Migrator().DropColumn(&RetailUpstreamKey{}, "OwnerSiteID"))
			require.NoError(t, db.Migrator().DropColumn(&RetailUpstreamKey{}, "UpstreamGroup"))
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			view, err = ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			require.Zero(t, view.Keys[0].OwnerSiteID)
			require.Empty(t, view.Keys[0].UpstreamGroup)
			_, err = first.SupplySecret(db)
			require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
			// A sibling may use the same secret while this old reference is blocked. Filing must not activate both.
			sibling := Channel{RetailSiteID: first.Id, Key: "test-secret-1", Type: first.Type, BaseURL: first.BaseURL, Name: "occupied", Group: "retail"}
			require.NoError(t, db.Create(&sibling).Error)
			_, err = write(RetailLibraryEdit{Action: "assign_owner", ID: id, OwnerSiteID: first.Id})
			require.ErrorIs(t, err, ErrRetailSupplyGroupKey)
			require.NoError(t, db.Delete(&sibling).Error)
			oldStamp := view.Stamp
			var before, after RetailPriceState
			require.NoError(t, db.First(&before, 1).Error)
			_, err = write(RetailLibraryEdit{Action: "assign_owner", ID: id, OwnerSiteID: first.Id})
			require.NoError(t, err)
			_, err = EditRetailKeyLibrary(ctx, RetailLibraryEdit{Stamp: oldStamp, Action: "assign_owner", ID: id, OwnerSiteID: foreign.Id}, 1)
			require.ErrorIs(t, err, ErrRetailLibraryConflict)
			_, err = write(RetailLibraryEdit{Action: "assign_owner", ID: id, OwnerSiteID: foreign.Id})
			require.Error(t, err)
			view, err = ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			require.Equal(t, first.Id, view.Keys[0].OwnerSiteID)
			require.Len(t, view.Uses[id], 2)
			var filed RetailUpstreamKey
			require.NoError(t, db.First(&filed, id).Error)
			require.Equal(t, entry.Sealed, filed.Sealed)
			require.NoError(t, db.First(&after, 1).Error)
			require.Equal(t, before.Version, after.Version)
			public, err := common.Marshal(view)
			require.NoError(t, err)
			require.NotContains(t, string(public), "test-secret")
			require.NotContains(t, string(public), entry.Sealed)
			_, err = write(RetailLibraryEdit{Action: "delete", ID: id})
			require.ErrorIs(t, err, ErrRetailLibraryUsed)
			// Renaming a bound Key preserves its ciphertext, reference IDs and the price revision.
			_, err = write(RetailLibraryEdit{Action: "edit", ID: id, Name: "Claude"})
			require.NoError(t, err)
			require.NoError(t, db.First(&filed, id).Error)
			require.Equal(t, "Claude", filed.Name)
			require.Equal(t, entry.Sealed, filed.Sealed)
			require.NoError(t, db.First(&after, 1).Error)
			require.Equal(t, before.Version, after.Version)
			_, err = write(RetailLibraryEdit{Action: "edit", ID: id, Name: "Discard", Key: "invalid secret"})
			require.Error(t, err)
			require.NoError(t, db.First(&filed, id).Error)
			require.Equal(t, "Claude", filed.Name)
			require.Equal(t, entry.Sealed, filed.Sealed)
			_, err = write(RetailLibraryEdit{Action: "edit", ID: id, Name: "Claude Pro", Key: "edited-secret", ConfirmGroups: map[int]string{first.Id: classified}})
			require.NoError(t, err)
			require.NoError(t, db.First(&filed, id).Error)
			require.Equal(t, "Claude Pro", filed.Name)
			secret, err = first.SupplySecret(db)
			require.NoError(t, err)
			require.Equal(t, "edited-secret", secret)
			// A stale external reference must neither receive the new key nor prevent its owner from rotating it.
			_, err = write(RetailLibraryEdit{Action: "rotate", ID: id, Key: "test-secret-2", ConfirmGroups: map[int]string{first.Id: classified}})
			require.NoError(t, err)
			_, err = foreign.SupplySecret(db)
			require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
			_, err = write(RetailLibraryEdit{Action: "detach", Channel: foreign.Id, Destination: foreign.GetBaseURL()})
			require.NoError(t, err)
			_, err = EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "rotate", ID: id, Stamp: view.Stamp, Key: "stale"}, 1)
			require.ErrorIs(t, err, ErrRetailLibraryConflict)
			{
				ch := *first
				key, err := ch.SupplySecret(db)
				require.NoError(t, err)
				require.Equal(t, "test-secret-2", key)
				wrong := ch
				target := "https://wrong.example"
				wrong.BaseURL = &target
				_, err = wrong.SupplySecret(db)
				require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
				_, err = write(RetailLibraryEdit{Action: "detach", Channel: ch.Id, Destination: ch.GetBaseURL()})
				require.NoError(t, err)
				_, err = ch.SupplySecret(db)
				require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
			}
			_, err = write(RetailLibraryEdit{Action: "add_route", Channel: first.Id, Destination: first.GetBaseURL(), ID: id, Name: "unassigned", Models: "model-a"})
			require.NoError(t, err)
			view, err = ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			require.Len(t, view.Uses[id], 1)
			child := view.Uses[id][0]
			require.Empty(t, view.Groups[child])
			_, err = write(RetailLibraryEdit{Action: "create", Name: "child is not a site", Key: "spare", OwnerSiteID: child})
			require.Error(t, err)
			var unassigned Channel
			require.NoError(t, db.First(&unassigned, child).Error)
			activeKey, _, apiErr := unassigned.GetNextEnabledKey()
			require.Nil(t, apiErr)
			require.Equal(t, "test-secret-2", activeKey)
			_, err = write(RetailLibraryEdit{Action: "detach", Channel: child, Destination: first.GetBaseURL()})
			require.NoError(t, err)
			_, err = write(RetailLibraryEdit{Action: "delete", ID: id})
			require.NoError(t, err)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", child).Update("key", "old-secret").Error)
			_, err = write(RetailLibraryEdit{Action: "import", Name: "untrusted owner", Channel: child, Destination: first.GetBaseURL(), OwnerSiteID: sites[1].Id})
			require.Error(t, err)
			other, err := write(RetailLibraryEdit{Action: "import", Name: "Old", Channel: child, Destination: first.GetBaseURL()})
			require.NoError(t, err)
			require.Positive(t, other)
			var imported RetailUpstreamKey
			require.NoError(t, db.First(&imported, other).Error)
			require.Equal(t, first.Id, imported.OwnerSiteID)
			var saved Channel
			require.NoError(t, db.First(&saved, child).Error)
			require.Empty(t, saved.Key)
			key, err := saved.SupplySecret(db)
			require.NoError(t, err)
			require.Equal(t, "old-secret", key)
			transfer := RetailLibraryEdit{Action: "transfer_owner", ID: other, FromSiteID: first.Id, OwnerSiteID: foreign.Id}
			_, err = write(transfer)
			require.ErrorIs(t, err, ErrRetailLibraryTransferUsed)
			_, err = write(RetailLibraryEdit{Action: "detach", Channel: child, Destination: first.GetBaseURL()})
			require.NoError(t, err)
			view, err = ReadRetailKeyLibrary(ctx)
			require.NoError(t, err)
			transfer.Stamp = view.Stamp
			_, err = EditRetailKeyLibrary(ctx, transfer, 1)
			require.NoError(t, err)
			_, err = EditRetailKeyLibrary(ctx, transfer, 1)
			require.ErrorIs(t, err, ErrRetailLibraryConflict)
			var moved RetailUpstreamKey
			require.NoError(t, db.First(&moved, other).Error)
			require.Equal(t, foreign.Id, moved.OwnerSiteID)
			require.Equal(t, imported.Sealed, moved.Sealed)
			require.Equal(t, imported.Version+1, moved.Version)
			// The cached source route cannot dispatch after transfer, even when the two roots share a URL.
			_, err = saved.SupplySecret(db)
			require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
			_, err = write(RetailLibraryEdit{Action: "bind", ID: other, Channel: first.Id, Destination: first.GetBaseURL()})
			require.ErrorIs(t, err, retailprice.ErrAccess)
			_, err = write(RetailLibraryEdit{Action: "bind", ID: other, Channel: foreign.Id, Destination: foreign.GetBaseURL()})
			require.NoError(t, err)
			require.NoError(t, db.First(foreign, foreign.Id).Error)
			key, err = foreign.SupplySecret(db)
			require.NoError(t, err)
			require.Equal(t, "old-secret", key)
		})
	}
}

// TestRetailKeyLibraryRevocation exercises the authorization change between loading a channel into cache
// and dispatching a request. A root must still be able to detach an excluded channel without sending its Key.
func TestRetailKeyLibraryRevocation(t *testing.T) {
	db := useVM2APIEnrollmentDatabase(t, "sqlite")
	require.NoError(t, migrateDB())
	t.Setenv("CRYPTO_SECRET", "test-only-master")
	endpoint := "https://one.example"
	channel := Channel{Key: "test-legacy", Type: 1, Name: "site", Group: "retail", BaseURL: &endpoint, Models: "m", Status: 1}
	require.NoError(t, channel.Insert())
	cfg := filepath.Join(t.TempDir(), "retail.json")
	raw, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"kind": "reseller", "channel_id": channel.Id}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, raw, 0600))
	t.Setenv("RETAIL_RELAY_CONFIG", cfg)
	ctx := context.Background()
	view, err := ReadRetailKeyLibrary(ctx)
	require.NoError(t, err)
	id, err := EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "import", Channel: channel.Id, Destination: endpoint, Stamp: view.Stamp, Name: "saved"}, 1)
	require.NoError(t, err)
	require.NoError(t, db.First(&channel, channel.Id).Error)
	t.Setenv("RETAIL_RELAY_CONFIG", "")
	_, err = channel.SupplySecret(db)
	require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
	view, err = ReadRetailKeyLibrary(ctx)
	require.NoError(t, err)
	require.NoError(t, db.Model(&channel).Updates(map[string]any{"key": "replacement-owner", "status": 1}).Error)
	require.Empty(t, view.Channels)
	require.Equal(t, []int{channel.Id}, view.Uses[id])
	_, err = EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "detach", Channel: channel.Id, Stamp: view.Stamp}, 1)
	require.NoError(t, err)
	var retained Channel
	require.NoError(t, db.First(&retained, channel.Id).Error)
	require.Equal(t, "replacement-owner", retained.Key)
	require.Equal(t, 1, retained.Status)
	require.Zero(t, retained.RetailKeyID)
}

// TestRetailKeyLibraryInstallation checks restart decryption without environment configuration and
// refuses to silently replace the installation key when restoring a database without its sidecar.
func TestRetailKeyLibraryInstallation(t *testing.T) {
	db := useVM2APIEnrollmentDatabase(t, "sqlite")
	require.NoError(t, migrateDB())
	oldPath := common.SQLitePath
	common.SQLitePath = filepath.Join(t.TempDir(), "retail.db") + "?_txlock=immediate"
	t.Cleanup(func() { common.SQLitePath = oldPath })
	t.Setenv("SESSION_SECRET", "")
	t.Setenv("CRYPTO_SECRET", "")
	endpoint := "https://install.example"
	site := Channel{Type: 1, Name: "install", BaseURL: &endpoint, Group: "retail", Status: 1}
	require.NoError(t, site.Insert())
	cfg := filepath.Join(t.TempDir(), "install.json")
	data, err := common.Marshal(map[string]any{"enabled": true, "group": "retail", "supplies": []map[string]any{{"kind": "reseller", "channel_id": site.Id}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfg, data, 0600))
	t.Setenv("RETAIL_RELAY_CONFIG", cfg)
	ctx := context.Background()
	view, err := ReadRetailKeyLibrary(ctx)
	require.NoError(t, err)
	id, err := EditRetailKeyLibrary(ctx, RetailLibraryEdit{Action: "create", OwnerSiteID: site.Id, Stamp: view.Stamp, Name: "test", Key: "test-secret"}, 1)
	require.NoError(t, err)
	var key RetailUpstreamKey
	require.NoError(t, db.First(&key, id).Error)
	got, err := key.secret()
	require.NoError(t, err)
	require.Equal(t, "test-secret", got)
	require.NoError(t, os.Remove(filepath.Join(filepath.Dir(common.SQLitePath), "retail.db.retail-key")))
	_, err = key.secret()
	require.ErrorIs(t, err, ErrRetailLibraryUnavailable)
}
