package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
)

// RetailStudioToken finds the signed-in owner's dedicated wallet identity.
// Creation happens on an explicit send or settings action and is serialized across processes using the pricing admission lock;
// reopening the page must not create another daily budget or re-enable a suspended identity.
func RetailStudioToken(userID int, create bool, productIDs ...string) (*Token, error) {
	if userID <= 0 || !common.IsRetailSite() {
		return nil, retailprice.ErrAccess
	}
	var token Token
	find := func(tx *gorm.DB) error {
		return tx.Where("user_id = ? AND service_mode = ?", userID, TokenServiceStudio).Order("id").First(&token).Error
	}
	if !create {
		return &token, find(DB)
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		err = find(tx)
		missing := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !missing {
			return err
		}
		var user User
		if err := tx.Select("id", "status", "group").First(&user, userID).Error; err != nil || user.Status != common.UserStatusEnabled {
			return retailprice.ErrAccess
		}
		if missing {
			key, err := common.GenerateKey()
			if err != nil {
				return err
			}
			// The key satisfies the existing unique constraint, but ValidateUserToken
			// refuses this service mode. The response exposes only the numeric policy ID.
			token = Token{UserId: userID, ServiceMode: TokenServiceStudio, Key: key,
				Name: "Studio", Status: common.TokenStatusEnabled, ExpiredTime: -1,
				UnlimitedQuota: true, CreatedTime: common.GetTimestamp()}
			if err := tx.Create(&token).Error; err != nil {
				return err
			}
		}
		if len(productIDs) == 0 || productIDs[0] == "" {
			return nil
		}
		if token.Status != common.TokenStatusEnabled || token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp() {
			return retailprice.ErrAccess
		}
		var book retailprice.Book
		if common.UnmarshalJsonStr(state.Payload, &book) != nil {
			return retailprice.ErrConfig
		}
		allowed := false
		for _, p := range book.Products {
			if p.ID == productIDs[0] && p.AllowsGroup(user.Group) {
				allowed = true
			}
		}
		if !allowed || !book.Enabled {
			return retailprice.ErrAccess
		}
		var row RetailKeyPricePolicy
		err = tx.First(&row, "token_id = ?", token.Id).Error
		// Older Studio identities saved the model list as manual ceilings. On the first
		// authorized send or settings visit, adopt live prices under the same lock as
		// budget edits. Keep the token and ledger so today's spending still counts.
		if err == nil {
			var saved retailprice.KeyPolicy
			if common.UnmarshalJsonStr(row.Payload, &saved) != nil || saved.Validate() != nil || saved.GroupCurrent {
				return retailprice.ErrConfig
			}
			if saved.StudioCurrent {
				return nil
			}
			saved.StudioCurrent = true
			saved.Caps = map[string]retailprice.Prices{}
			payload, err := common.Marshal(saved)
			if err != nil {
				return err
			}
			row.Payload, row.Revision = string(payload), row.Revision+1
			return tx.Save(&row).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		request, daily, err := retail_setting.StudioBudgets()
		if err != nil {
			return err
		}
		policy := retailprice.KeyPolicy{ProductID: productIDs[0], AcceptFloating: true, StudioCurrent: true, Caps: map[string]retailprice.Prices{}, MaxRequestCNY: request, DailyBudgetCNY: daily}
		payload, err := common.Marshal(policy)
		if err != nil {
			return err
		}
		return tx.Create(&RetailKeyPricePolicy{TokenID: token.Id, UserID: userID, Revision: 1, Payload: string(payload)}).Error
	})
	return &token, err
}
