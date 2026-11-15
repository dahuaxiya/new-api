package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"gorm.io/gorm"
)

// RetailStudioToken finds the signed-in owner's dedicated wallet identity.
// Creation is explicit and serialized across processes using the pricing admission lock;
// reopening the page must not create another daily budget or re-enable a suspended identity.
func RetailStudioToken(userID int, create bool) (*Token, error) {
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
		if _, err := lockRetailPriceState(tx); err != nil {
			return err
		}
		err := find(tx)
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var user User
		if err := tx.Select("id", "status").First(&user, userID).Error; err != nil || user.Status != common.UserStatusEnabled {
			return retailprice.ErrAccess
		}
		key, err := common.GenerateKey()
		if err != nil {
			return err
		}
		// The key satisfies the existing unique constraint, but ValidateUserToken
		// refuses this service mode. The response exposes only the numeric policy ID.
		token = Token{UserId: userID, ServiceMode: TokenServiceStudio, Key: key,
			Name: "Studio", Status: common.TokenStatusEnabled, ExpiredTime: -1,
			UnlimitedQuota: true, CreatedTime: common.GetTimestamp()}
		return tx.Create(&token).Error
	})
	return &token, err
}
