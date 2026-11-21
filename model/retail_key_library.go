package model

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// RetailUpstreamKey belongs to one root site. Legacy inventory with no owner cannot be dispatched.
type RetailUpstreamKey struct {
	OwnerSiteID int    `json:"owner_site_id" gorm:"not null;default:0"`
	ID          int    `json:"id"`
	Name        string `json:"name" gorm:"size:120;not null"`
	Sealed      string `json:"-" gorm:"type:text;not null"`
	Version     int64  `json:"version" gorm:"not null"`
}

// RetailLibraryState contains display metadata only. Some references may belong to a site no longer enrolled.
type RetailLibraryState struct {
	Keys     []RetailUpstreamKey    `json:"keys"`
	Channels []RetailBindingChannel `json:"channels"`
	Uses     map[int][]int          `json:"uses"`
	Groups   map[int]string         `json:"groups"`
	Stamp    string                 `json:"stamp"`
}

// RetailLibraryEdit carries the destination seen by the administrator. Only create and rotate accept a secret.
type RetailLibraryEdit struct {
	OwnerSiteID   int            `json:"owner_site_id"`
	ConfirmGroups map[int]string `json:"confirm_groups"`
	Action        string         `json:"action"`
	ID            int            `json:"id"`
	Channel       int            `json:"channel_id"`
	Destination   string         `json:"destination"`
	Models        string         `json:"models"`
	Name          string         `json:"name"`
	Key           string         `json:"key"`
	Stamp         string         `json:"stamp"`
}

var ErrRetailLibraryConflict = errors.New("upstream key inventory changed")
var ErrRetailLibraryUsed = common.NewMessage("Remove this Key from its sites before deleting it")
var ErrRetailLibraryUnavailable = common.NewMessage("Upstream Key is unavailable")

// sqliteLibraryRoot loads the installation secret beside a persistent SQLite database. Only a first
// inventory write may initialize it. Restoring a database without its secret must never mint a new one.
func sqliteLibraryRoot(initialize bool) (string, error) {
	path, _, _ := strings.Cut(common.SQLitePath, "?")
	path = strings.TrimPrefix(path, "file:")
	if !common.UsingMainDatabase(common.DatabaseTypeSQLite) || path == "" || path == ":memory:" || strings.Contains(common.SQLitePath, "mode=memory") {
		// A remote database can serve multiple nodes. They must share the documented installation secret.
		return "", ErrRetailLibraryUnavailable
	}
	path += ".retail-key"
	if _, err := os.Lstat(path); initialize && errors.Is(err, os.ErrNotExist) {
		// Publish a fully written private file without overwriting another process's winning key.
		tmp, err := os.CreateTemp(filepath.Dir(path), ".retail-key-*")
		if err != nil {
			return "", ErrRetailLibraryUnavailable
		}
		defer os.Remove(tmp.Name())
		var root [32]byte
		rand.Read(root[:])
		_, err = tmp.Write(root[:])
		if err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err != nil || closeErr != nil {
			return "", ErrRetailLibraryUnavailable
		}
		if err = os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
			return "", ErrRetailLibraryUnavailable
		}
		// The DB may commit as soon as this returns. Persist the directory entry before that happens.
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return "", ErrRetailLibraryUnavailable
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return "", ErrRetailLibraryUnavailable
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != 32 {
		return "", ErrRetailLibraryUnavailable
	}
	root, err := os.ReadFile(path)
	if err != nil || len(root) != 32 {
		return "", ErrRetailLibraryUnavailable
	}
	return string(root), nil
}

// libraryCipher derives an inventory-specific key from persistent deployment configuration.
// The in-memory default session secret cannot decrypt stored credentials after a restart.
func libraryCipher(initialize bool) (cipher.AEAD, error) {
	root := os.Getenv("CRYPTO_SECRET")
	if root == "" {
		root = os.Getenv("SESSION_SECRET")
	}
	if root == "" {
		var err error
		root, err = sqliteLibraryRoot(initialize)
		if err != nil {
			return nil, err
		}
	}
	mac := hmac.New(sha256.New, []byte(root))
	mac.Write([]byte("new-api/retail/upstream-key/v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

// secret authenticates the row identity to prevent ciphertext copied between credentials from resolving successfully.
func (k RetailUpstreamKey) secret() (string, error) {
	aead, err := libraryCipher(false)
	if err != nil {
		return "", err
	}
	data, err := base64.RawStdEncoding.DecodeString(k.Sealed)
	if err != nil || len(data) < aead.Overhead() {
		return "", ErrRetailLibraryUnavailable
	}
	plain, err := aead.Open(nil, nil, data, fmt.Appendf(nil, "v1/%d", k.ID))
	if err != nil {
		return "", ErrRetailLibraryUnavailable
	}
	return string(plain), nil
}

// supplyTarget pins the selected adapter and exact destination. An address change requires a fresh selection.
func (ch *Channel) supplyTarget() string { return fmt.Sprintf("%d/%s", ch.Type, ch.GetBaseURL()) }

// supplySiteID identifies custody by root channel ID even when two upstream sites share an endpoint.
func (ch Channel) supplySiteID() int {
	if ch.RetailSiteID != 0 {
		return ch.RetailSiteID
	}
	return ch.Id
}

// SupplySecret resolves a reference against the current binding at dispatch time. Stale cached channels cannot
// use a different site's credential or continue using a retired library entry. Inline credentials retain their legacy behavior.
func (ch *Channel) SupplySecret(db *gorm.DB) (string, error) {
	if ch.RetailKeyID == 0 {
		return ch.Key, nil
	}
	if ch.BaseURL == nil || ch.RetailKeyTarget != ch.supplyTarget() || ch.ChannelInfo.IsMultiKey {
		return "", ErrRetailLibraryUnavailable
	}
	// Recheck current enrollment and both endpoints of the parent/child relation on every dispatch.
	// A cached channel cannot keep access after either site is moved to a customer hosting account.
	cfg, err := retail_setting.Load()
	anchor := ch.supplySiteID()
	if err != nil || !cfg.Enabled || !validRetailSupplySite(*ch, cfg.Group) || !slices.ContainsFunc(cfg.Supplies, func(site retail_setting.Supply) bool {
		return site.ChannelID == anchor && (ch.Id == anchor || site.Kind == "reseller")
	}) {
		return "", ErrRetailLibraryUnavailable
	}
	base := *ch.BaseURL
	occupied := db.Model(&GPTHostingBinding{}).Select("channel_id").Where("channel_id IN ?", []int{ch.Id, anchor})
	// Group and adapter are checked again using the actual joined rows. Avoid loading other credentials on this hot path.
	type liveKey struct {
		RetailUpstreamKey
		OwnerGroup  string
		ParentGroup string
		OwnerInfo   ChannelInfo
		ParentInfo  ChannelInfo
	}
	var live liveKey
	q := db.Table("retail_upstream_keys AS k").Select("k.*, o."+commonGroupCol+" AS owner_group, p."+commonGroupCol+" AS parent_group, o.channel_info AS owner_info, p.channel_info AS parent_info").
		Joins("JOIN channels AS o ON o.retail_key_id = k.id").Joins("JOIN channels AS p ON p.id = k.owner_site_id AND p.id = ?", anchor).
		Where("o.id = ? AND k.id = ? AND o.retail_key_target = ? AND COALESCE(o.retail_site_id,0) = ? AND COALESCE(p.retail_site_id,0) = 0", ch.Id, ch.RetailKeyID, ch.RetailKeyTarget, ch.RetailSiteID).
		Where("o.type = ? AND p.type = ? AND o.base_url = ? AND p.base_url = ?", ch.Type, ch.Type, base, base).Where("NOT EXISTS (?)", occupied).Take(&live)
	if q.Error != nil || live.OwnerInfo.IsMultiKey || live.ParentInfo.IsMultiKey {
		return "", ErrRetailLibraryUnavailable
	}
	for _, g := range []string{live.OwnerGroup, live.ParentGroup} {
		row := Channel{Type: ch.Type, BaseURL: ch.BaseURL, Group: g}
		if !validRetailSupplySite(row, cfg.Group) {
			return "", ErrRetailLibraryUnavailable
		}
	}
	return live.RetailUpstreamKey.secret()
}

// retailKeyOwners reads only ownership metadata for the referenced keys, in one query for a list of routes.
func retailKeyOwners(tx *gorm.DB, channels []Channel) (map[int]int, error) {
	var ids []int
	for _, ch := range channels {
		if ch.RetailKeyID > 0 {
			ids = append(ids, ch.RetailKeyID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var entries []RetailUpstreamKey
	if err := tx.Select("id", "owner_site_id").Where("id IN ?", ids).Find(&entries).Error; err != nil {
		return nil, err
	}
	owners := make(map[int]int, len(entries))
	for _, entry := range entries {
		owners[entry.ID] = entry.OwnerSiteID
	}
	return owners, nil
}

// hasSupplyKey excludes stale and unfiled references from route selection without reading ciphertext.
func (ch Channel) hasSupplyKey(owners map[int]int) bool {
	if ch.RetailKeyID != 0 {
		return ch.BaseURL != nil && !ch.ChannelInfo.IsMultiKey && owners[ch.RetailKeyID] > 0 && owners[ch.RetailKeyID] == ch.supplySiteID() && ch.RetailKeyTarget == ch.supplyTarget()
	}
	return ch.Key != ""
}

// checkSiteSecret enforces the existing one-credential-per-upstream-group rule within one site,
// including mixed legacy and library rows. A foreign legacy reference cannot be used at this site.
func checkSiteSecret(tx *gorm.DB, owner Channel, candidate string) error {
	anchor := owner.supplySiteID()
	var peers []Channel
	if err := tx.Where("(retail_site_id = ? OR id = ?) AND id <> ?", anchor, anchor, owner.Id).Find(&peers).Error; err != nil {
		return err
	}
	for _, peer := range peers {
		if peer.RetailKeyID != 0 {
			var key RetailUpstreamKey
			// Historical foreign references are inert; do not decrypt one during a local uniqueness check.
			err := tx.Where("id = ? AND owner_site_id = ?", peer.RetailKeyID, anchor).Take(&key).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			secret, err := key.secret()
			if err != nil {
				return err
			}
			if hmac.Equal([]byte(secret), []byte(candidate)) {
				return ErrRetailSupplyGroupKey
			}
		} else if peer.Key == candidate {
			return ErrRetailSupplyGroupKey
		}
	}
	return nil
}

// localKeyUses scopes a stored reference to its physical root, including locally suspended or unenrolled routes.
// Foreign references left by earlier versions cannot dispatch and must not participate in a rotation.
func localKeyUses(tx *gorm.DB, id, root int) *gorm.DB {
	return tx.Model(&Channel{}).Where("retail_key_id = ? AND (retail_site_id = ? OR (id = ? AND COALESCE(retail_site_id, 0) = 0))", id, root, root)
}

// validateRestoredSupply checks the routes that would become usable when a legacy key is filed.
// Other roots stay blocked. An unused key is metadata-only and never needs decryption here.
func validateRestoredSupply(tx *gorm.DB, key RetailUpstreamKey, root int) error {
	var local []Channel
	if err := localKeyUses(tx, key.ID, root).Limit(2).Find(&local).Error; err != nil {
		return err
	}
	if len(local) == 0 {
		return nil
	}
	if len(local) > 1 {
		return ErrRetailSupplyGroupKey
	}
	secret, err := key.secret()
	if err != nil {
		return err
	}
	// An inline or already-filed sibling may have adopted this value while the old reference was inert.
	return checkSiteSecret(tx, local[0], secret)
}

// readRetailKeyLibrary uses an independent concurrency stamp. Routine background price renewals do not invalidate the form.
func readRetailKeyLibrary(tx *gorm.DB) (out RetailLibraryState, err error) {
	if err = tx.Select("id", "name", "version", "owner_site_id").Order("id").Find(&out.Keys).Error; err != nil {
		return
	}
	if out.Channels, err = RetailRateSiteChannels(tx); err != nil {
		return
	}
	var rows []Channel
	if err = tx.Select("id", "retail_key_id").Where("retail_key_id > 0").Order("id").Find(&rows).Error; err != nil {
		return
	}
	out.Uses = make(map[int][]int)
	for _, row := range rows {
		out.Uses[row.RetailKeyID] = append(out.Uses[row.RetailKeyID], row.Id)
	}
	var state RetailPriceState
	if err = tx.First(&state, 1).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		err = nil
	} else if err != nil {
		return
	}
	var book retailprice.Book
	if state.Payload != "" {
		if err = common.UnmarshalJsonStr(state.Payload, &book); err != nil {
			return
		}
	}
	out.Groups = book.RegisteredGroups()
	// Include hidden inline secret revisions so importing an old form cannot adopt a newly replaced Key.
	stamps := make([]string, 0, len(out.Channels))
	for _, row := range out.Channels {
		stamps = append(stamps, row.credentialStamp)
	}
	raw, err := common.Marshal([]any{out.Keys, out.Channels, out.Uses, out.Groups, stamps})
	if err == nil {
		out.Stamp = credentialMAC(raw)
	}
	return
}

// ReadRetailKeyLibrary returns keys even when there are no sites or published groups.
func ReadRetailKeyLibrary(ctx context.Context) (out RetailLibraryState, err error) {
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error { var e error; out, e = readRetailKeyLibrary(tx); return e })
	return
}

// EditRetailKeyLibrary serializes binding changes with the existing retail configuration writers.
// Import is explicit and atomic so installations can upgrade without changing credentials used by running older binaries.
func EditRetailKeyLibrary(ctx context.Context, in RetailLibraryEdit, actor int) (int, error) {
	if actor <= 0 || in.ID < 0 || in.Channel < 0 {
		return 0, retailprice.ErrConfig
	}
	id := in.ID
	err := DB.WithContext(ctx).Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		price, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		var ch Channel
		if in.Channel > 0 {
			if err := lockForUpdate(tx).First(&ch, in.Channel).Error; err != nil {
				return retailprice.ErrAccess
			}
		}
		state, err := readRetailKeyLibrary(tx)
		if err != nil {
			return err
		}
		if !hmac.Equal([]byte(in.Stamp), []byte(state.Stamp)) {
			return ErrRetailLibraryConflict
		}
		if ch.Id > 0 && in.Action != "detach" && (!slices.ContainsFunc(state.Channels, func(r RetailBindingChannel) bool { return r.ID == ch.Id && r.BaseURL == in.Destination }) || in.Destination != ch.GetBaseURL()) {
			return retailprice.ErrAccess
		}
		var key RetailUpstreamKey
		if id > 0 {
			if err := lockForUpdate(tx).First(&key, id).Error; err != nil {
				return retailprice.ErrConfig
			}
		}
		// A filing site is the approved root channel, never a child credential. Do not infer
		// custody from a consumer: an existing Key may be in use at several unrelated sites.
		switch in.Action {
		case "import", "create", "assign_owner":
			ownerID := in.OwnerSiteID
			if in.Action == "import" {
				// The client cannot redirect custody during import. Resolve the source's root
				// and verify that the root is itself still eligible, even for a valid child row.
				if ownerID != 0 || ch.Id == 0 {
					return retailprice.ErrConfig
				}
				ownerID = ch.supplySiteID()
			}
			if ownerID <= 0 || !slices.ContainsFunc(state.Channels, func(row RetailBindingChannel) bool {
				return row.ID == ownerID && row.SiteID == row.ID
			}) {
				return retailprice.ErrConfig
			}
			if in.Action == "assign_owner" {
				if id == 0 || key.OwnerSiteID != 0 || ch.Id != 0 || in.Key != "" {
					return retailprice.ErrConfig
				}
				if err := validateRestoredSupply(tx, key, ownerID); err != nil {
					return err
				}
				// File legacy inventory once. Historical foreign references stay visible for cleanup and cannot dispatch.
				return tx.Model(&key).Updates(map[string]any{"owner_site_id": in.OwnerSiteID, "version": key.Version + 1}).Error
			}
			key.OwnerSiteID = ownerID
		default:
			if in.OwnerSiteID != 0 {
				return retailprice.ErrConfig
			}
		}
		// The price revision protects older pricing/binding forms that do not know about library IDs.
		// Changes to an unused credential can never affect the price workflow.
		var affected []int
		if in.Action == "rotate" {
			if key.OwnerSiteID <= 0 {
				return retailprice.ErrConfig
			}
			// Use the stored root before checking enrolled channels below. Silently skipping an
			// unenrolled local route could rotate a credential the operator never confirmed.
			if err := localKeyUses(tx, id, key.OwnerSiteID).Pluck("id", &affected).Error; err != nil {
				return err
			}
		}
		if in.Action == "bind" || in.Action == "detach" {
			affected = []int{ch.Id}
		}
		groups := map[int]string{}
		for _, owner := range affected {
			if group := state.Groups[owner]; group != "" {
				groups[owner] = group
			}
		}
		if (in.Action == "rotate" || in.Action == "bind") && !maps.Equal(groups, in.ConfirmGroups) {
			return retailprice.ErrConfig
		}
		if (len(affected) > 0 && (in.Action == "rotate" || in.Action == "bind" || in.Action == "detach")) || in.Action == "add_route" {
			next := price.Version + 1
			if err := tx.Create(&RetailPriceVersion{Version: next, Payload: price.Payload, ActorID: actor, CreatedAt: time.Now().Unix()}).Error; err != nil {
				return err
			}
			if err := tx.Model(price).Update("version", next).Error; err != nil {
				return err
			}
		}
		switch in.Action {
		case "create", "import", "rotate":
			secret := in.Key
			if in.Action == "import" {
				if ch.Id == 0 || ch.RetailKeyID != 0 || ch.Key == "" || secret != "" {
					return retailprice.ErrConfig
				}
				secret = ch.Key
				if err := checkSiteSecret(tx, ch, secret); err != nil {
					return err
				}
			} else if ch.Id != 0 || secret == "" {
				return retailprice.ErrConfig
			}
			// Legacy inline values may predate single-token validation. Validate the actual value before sealing it.
			if len(secret) > 4096 || strings.ContainsFunc(secret, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
				return retailprice.ErrConfig
			}
			if in.Action == "rotate" {
				if id == 0 {
					return retailprice.ErrConfig
				}
				for _, channelID := range affected {
					var owner Channel
					if err := tx.First(&owner, channelID).Error; err != nil {
						return err
					}
					if !slices.ContainsFunc(state.Channels, func(r RetailBindingChannel) bool {
						return r.ID == channelID && key.OwnerSiteID > 0 && r.SiteID == key.OwnerSiteID
					}) {
						return retailprice.ErrAccess
					}
					if err := checkSiteSecret(tx, owner, secret); err != nil {
						return err
					}
				}
			} else {
				key.Name = strings.TrimSpace(in.Name)
				if id != 0 || key.Name == "" || utf8.RuneCountInString(key.Name) > 120 {
					return retailprice.ErrConfig
				}
				if err := tx.Create(&key).Error; err != nil {
					return err
				}
				id = key.ID
			}
			aead, err := libraryCipher(len(state.Keys) == 0)
			if err != nil {
				return err
			}
			sealed := base64.RawStdEncoding.EncodeToString(aead.Seal(nil, nil, []byte(secret), fmt.Appendf(nil, "v1/%d", id)))
			if err := tx.Model(&key).Updates(map[string]any{"sealed": sealed, "version": key.Version + 1}).Error; err != nil {
				return err
			}
			if in.Action != "import" {
				return nil
			}
		case "bind", "add_route":
			// Check before decrypting or creating a child. Equal URLs do not imply a shared site.
			if key.OwnerSiteID <= 0 || key.OwnerSiteID != ch.supplySiteID() {
				return retailprice.ErrAccess
			}
			if ch.Id != 0 && in.Action == "add_route" {
				// An operator can attach a second Key to a site before assigning a pricing group.
				// Create a disabled draft first. Existing site eligibility and name validation stay centralized.
				if id == 0 || in.Key != "" {
					return retailprice.ErrConfig
				}
				child, err := createRetailSupplyGroupIn(tx, ctx, ch.Id, in.Name, "", in.Models)
				if err != nil {
					return err
				}
				ch = Channel{}
				if err = tx.First(&ch, child).Error; err != nil {
					return err
				}
			} else if ch.Key != "" {
				return common.NewMessage("Import the existing site Key before replacing it")
			}

			if id == 0 || ch.Id == 0 || in.Key != "" {
				return retailprice.ErrConfig
			}
			secret, err := key.secret()
			if err != nil {
				return err
			}
			if err = checkSiteSecret(tx, ch, secret); err != nil {
				return err
			}
			if in.Action == "add_route" && ch.Models != "" {
				ch.Status = common.ChannelStatusEnabled
				if err = tx.Model(&ch).Update("status", ch.Status).Error; err != nil {
					return err
				}
				if err = ch.UpdateAbilities(tx); err != nil {
					return err
				}
			}
		case "detach":
			if id != 0 || ch.Id == 0 || ch.RetailKeyID == 0 || in.Key != "" {
				return retailprice.ErrConfig
			}
			// Once another service or operator owns the channel, removing a stale reference
			// must preserve its credentials, status and abilities. Only an enrolled route relying
			// exclusively on the library needs to be suspended.
			if ch.Key != "" || !slices.ContainsFunc(state.Channels, func(row RetailBindingChannel) bool { return row.ID == ch.Id }) {
				return tx.Table("channels").Where("id = ?", ch.Id).Updates(map[string]any{"retail_key_id": 0, "retail_key_target": ""}).Error
			}
			ch.Status = common.ChannelStatusManuallyDisabled
			if err := tx.Model(&ch).Update("status", ch.Status).Error; err != nil {
				return err
			}
			if err := ch.UpdateAbilities(tx); err != nil {
				return err
			}
		case "delete":
			if id == 0 || ch.Id != 0 || in.Key != "" {
				return retailprice.ErrConfig
			}
			if len(state.Uses[id]) > 0 {
				return ErrRetailLibraryUsed
			}
			return tx.Delete(&key).Error
		default:
			return retailprice.ErrConfig
		}
		target := ""
		if id > 0 {
			target = ch.supplyTarget()
		}
		// Only this root-only transaction writes the read-only reference columns. Removing the inline value prevents stale fallback.
		return tx.Table("channels").Where("id = ?", ch.Id).Updates(map[string]any{"retail_key_id": id, "retail_key_target": target, "key": ""}).Error
	})
	return id, err
}
