package model

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// RetailCredentialInventory combines the price revision with a keyed fingerprint of eligible channel settings.
// Generic channel edits can change a Key or an alternate route without changing the retail price revision.
type RetailCredentialInventory struct {
	Targets  RetailRouteTargets         `json:"targets"`
	Version  int64                      `json:"version"`
	Stamp    string                     `json:"stamp"`
	Channels []RetailBindingChannel     `json:"channels"`
	Groups   map[int]string             `json:"groups"`
	Products []retailprice.Product      `json:"products"`
	Bindings []retailprice.GroupBinding `json:"bindings"`
}

// RetailCredentialEdit describes one action. A rotation never sends the old secret to the browser.
type RetailCredentialEdit struct {
	Version      int64  `json:"version"`
	Stamp        string `json:"stamp"`
	ConfirmGroup string `json:"confirm_group"`
	Action       string `json:"action"`
	ID           int    `json:"id"`
	Site         int    `json:"site_id"`
	Group        string `json:"group"`
	Name         string `json:"name"`
	Key          string `json:"key"`
	Models       string `json:"models"`
}

// credentialMAC authenticates private concurrency inputs without exposing an offline verifier for weak keys.
func credentialMAC(data []byte) string {
	mac := hmac.New(sha256.New, []byte(common.SessionSecret))
	mac.Write([]byte("retail-credential-inventory-v1\x00"))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// retailCredentialInventory joins only explicitly authorized supply rows. Missing historical references are represented by ID in the book.
func retailCredentialInventory(tx *gorm.DB, state RetailPriceState) (RetailCredentialInventory, retailprice.Book, error) {
	var book retailprice.Book
	var inventory RetailCredentialInventory
	if err := common.UnmarshalJsonStr(state.Payload, &book); err != nil {
		return inventory, book, err
	}
	channels, err := RetailRateSiteChannels(tx)
	if err != nil {
		return inventory, book, err
	}
	// Stable channel order comes from RetailRateSiteChannels. Include hidden mapping and secret revisions as well as visible state.
	stamps := make([]string, 0, len(channels))
	ids := make([]int, 0, len(channels))
	for _, ch := range channels {
		ids = append(ids, ch.ID)
		stamps = append(stamps, ch.credentialStamp)
	}
	// Reuse the relay's active abilities and exact model aliases when previewing possible alternatives.
	cfg, err := retail_setting.Load()
	if err != nil {
		return inventory, book, err
	}
	targets, err := retailCatalogTargetsIn(tx, cfg.Group, ids)
	if err != nil {
		return inventory, book, err
	}
	// A fingerprint requires canonical map ordering even when the application installs another JSON codec.
	payload, err := json.Marshal([]any{state.Version, channels, stamps, targets})
	if err != nil {
		return inventory, book, err
	}
	inventory = RetailCredentialInventory{Targets: targets, Version: state.Version, Stamp: credentialMAC(payload), Channels: channels, Groups: book.RegisteredGroups(), Products: book.Products, Bindings: book.GroupBindings}
	return inventory, book, nil
}

// ReadRetailCredentials returns a consistent read-only inventory, including before the first price publication.
func ReadRetailCredentials(ctx context.Context) (out RetailCredentialInventory, err error) {
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state RetailPriceState
		if e := tx.First(&state, 1).Error; errors.Is(e, gorm.ErrRecordNotFound) {
			state.Payload = "{}"
		} else if e != nil {
			return e
		}
		var e error
		out, _, e = retailCredentialInventory(tx, state)
		return e
	})
	return
}

// EditRetailCredential locks the price state before site and credential rows, matching the launch and legacy binding editors.
// Only the new credential's group identity may be added to the saved book; routing and prices remain byte-for-byte intact on rotation and switches.
func EditRetailCredential(ctx context.Context, in RetailCredentialEdit, actor int) (int, error) {
	if actor <= 0 || in.Site <= 0 || in.ID < 0 || len(in.Key) > 4096 || strings.ContainsFunc(in.Key, unicode.IsSpace) {
		return 0, retailprice.ErrConfig
	}
	id := in.ID
	err := DB.WithContext(ctx).Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		if state.Version != in.Version {
			return ErrRetailPriceConflict
		}
		var parent, ch Channel
		if err := lockForUpdate(tx).First(&parent, in.Site).Error; err != nil {
			return retailprice.ErrAccess
		}
		if id > 0 {
			if err := lockForUpdate(tx).First(&ch, id).Error; err != nil {
				return retailprice.ErrAccess
			}
		}
		inventory, book, err := retailCredentialInventory(tx, *state)
		if err != nil {
			return err
		}
		if !hmac.Equal([]byte(in.Stamp), []byte(inventory.Stamp)) {
			return ErrRetailPriceConflict
		}
		pos := slices.IndexFunc(inventory.Channels, func(c RetailBindingChannel) bool {
			return c.SiteID == in.Site && ((id == 0 && c.ID == in.Site && c.AllowNewCredential) || c.ID == id)
		})
		if pos < 0 {
			return retailprice.ErrAccess
		}
		group := inventory.Groups[id]
		switch in.Action {
		case "create":
			if id != 0 || in.Key == "" || !retailprice.ValidBindingGroup(in.Group) {
				return retailprice.ErrConfig
			}
			id, err = createRetailSupplyGroupIn(tx, ctx, in.Site, in.Name, in.Key, in.Models)
			if err != nil {
				return err
			}
		case "rotate":
			if ch.RetailKeyID != 0 || id == 0 || in.Key == "" || in.ConfirmGroup != in.Group || !retailprice.ValidBindingGroup(in.Group) || (group != "" && group != in.Group) {
				return retailprice.ErrConfig
			}
			// An operator confirms the new upstream Key belongs to the same tariff group. Check duplicates under the parent lock.
			if err := syncRetailSiteAddress(tx, &Channel{Id: id, Key: in.Key}); err != nil {
				return err
			}
			if err := tx.Model(&ch).Update("key", in.Key).Error; err != nil {
				return err
			}
		case "enable", "disable":
			if id == 0 || in.Key != "" {
				return retailprice.ErrConfig
			}
			ch.Status = common.ChannelStatusManuallyDisabled
			if in.Action == "enable" {
				if !inventory.Channels[pos].HasKey || strings.TrimSpace(ch.Models) == "" {
					return ErrRetailSupplyGroupIncomplete
				}
				ch.Status = common.ChannelStatusEnabled
			}
			if err := tx.Model(&ch).Update("status", ch.Status).Error; err != nil {
				return err
			}
			if err := ch.UpdateAbilities(tx); err != nil {
				return err
			}
		default:
			return retailprice.ErrConfig
		}
		if group == "" && (in.Action == "create" || in.Action == "rotate") {
			if book.CredentialGroups == nil {
				book.CredentialGroups = map[int]string{}
			}
			book.CredentialGroups[id] = in.Group
			raw, err := common.Marshal(book)
			if err != nil {
				return err
			}
			state.Payload = string(raw)
		}
		// Advance the shared revision even for a secret-only change so an older launch/binding form cannot overwrite it.
		state.Version++
		if err := tx.Create(&RetailPriceVersion{Version: state.Version, Payload: state.Payload, ActorID: actor, CreatedAt: time.Now().Unix()}).Error; err != nil {
			return err
		}
		return tx.Model(state).Updates(map[string]any{"version": state.Version, "payload": state.Payload}).Error
	})
	return id, err
}
