package model

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRetailPriceConflict = errors.New("retail_price_version_conflict")

// RetailPriceState is the publication/admission lock; every worker reads the same committed version.
type RetailPriceState struct {
	ID      int `gorm:"primaryKey"`
	Version int64
	Payload string `gorm:"type:text"`
}

// RetailPriceVersion preserves the official tariff and group rates used for historical quotes.
type RetailPriceVersion struct {
	Version   int64  `json:"version" gorm:"primaryKey;autoIncrement:false"`
	Payload   string `json:"-" gorm:"type:text"`
	ActorID   int    `json:"actor_id"`
	CreatedAt int64  `json:"created_at"`
}

// RetailKeyPricePolicy belongs to exactly one key; payload contains no credential material.
type RetailKeyPricePolicy struct {
	TokenID  int    `json:"token_id" gorm:"primaryKey;autoIncrement:false"`
	UserID   int    `json:"user_id" gorm:"index"`
	Revision int64  `json:"revision"`
	Payload  string `json:"-" gorm:"type:text"`
}

// RetailPriceBudget retains separate UTC-day reservations so midnight cannot erase in-flight spending.
type RetailPriceBudget struct {
	ID       string `gorm:"primaryKey;type:varchar(64)"`
	Reserved string `gorm:"type:varchar(40)"`
	Spent    string `gorm:"type:varchar(40)"`
}

// GetRetailPriceBook reads from the database rather than an eventually consistent option cache.
func GetRetailPriceBook() (retailprice.Book, int64, error) {
	var state RetailPriceState
	err := DB.First(&state, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return retailprice.Book{}, 0, nil
	}
	if err != nil {
		return retailprice.Book{}, 0, err
	}
	var book retailprice.Book
	if err = common.UnmarshalJsonStr(state.Payload, &book); err != nil {
		return book, 0, retailprice.ErrConfig
	}
	if err := book.Validate(time.Now().Unix()); err != nil {
		return book, state.Version, err
	}
	return book, state.Version, nil
}

// lockRetailPriceState serializes publication with admission, key-policy edits and settlement.
// SQLite's write lock is acquired before reading; other dialects additionally use FOR UPDATE.
func lockRetailPriceState(tx *gorm.DB) (*RetailPriceState, error) {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RetailPriceState{ID: 1, Payload: `{}`}).Error; err != nil {
		return nil, err
	}
	if err := tx.Model(&RetailPriceState{}).Where("id = ?", 1).Update("version", gorm.Expr("version")).Error; err != nil {
		return nil, err
	}
	var state RetailPriceState
	if err := lockForUpdate(tx).First(&state, 1).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

// PublishRetailPriceBook performs compare-and-swap publication and keeps an immutable audit version.
// Passing ratesOnly updates group multipliers without allowing a caller to rewrite official prices.
func PublishRetailPriceBook(expected int64, book retailprice.Book, ratesOnly []retailprice.Rate, actorID int) (int64, error) {
	if actorID <= 0 || expected < 0 {
		return 0, retailprice.ErrConfig
	}
	var version int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		if state.Version != expected {
			return ErrRetailPriceConflict
		}
		if ratesOnly != nil {
			if err = common.UnmarshalJsonStr(state.Payload, &book); err != nil {
				return retailprice.ErrConfig
			}
			// Import is a complete group snapshot: removed rates cannot remain silently usable.
			book.Rates = ratesOnly
		}
		if err := book.Validate(time.Now().Unix()); err != nil {
			return err
		}
		cfg, err := retail_setting.Load()
		if err != nil {
			return err
		}
		for _, product := range book.Products {
			for _, route := range product.Routes {
				if !slices.ContainsFunc(cfg.Supplies, func(s retail_setting.Supply) bool { return s.ChannelID == route.ChannelID }) {
					return retailprice.ErrAccess
				}
			}
		}
		payload, err := common.Marshal(book)
		if err != nil || len(payload) > 1<<20 {
			return retailprice.ErrConfig
		}
		version = state.Version + 1
		if err := tx.Create(&RetailPriceVersion{Version: version, Payload: string(payload), ActorID: actorID, CreatedAt: time.Now().Unix()}).Error; err != nil {
			return err
		}
		return tx.Model(state).Updates(map[string]any{"version": version, "payload": string(payload)}).Error
	})
	return version, err
}

// GetRetailKeyPolicy verifies key ownership, including for policies that have not yet been configured.
func GetRetailKeyPolicy(userID, tokenID int) (retailprice.KeyPolicy, int64, error) {
	var token Token
	if err := DB.Select("id", "user_id", "service_mode").First(&token, "id = ? AND user_id = ? AND service_mode = ?", tokenID, userID, TokenServiceRetail).Error; err != nil {
		return retailprice.KeyPolicy{}, 0, retailprice.ErrAccess
	}
	var row RetailKeyPricePolicy
	err := DB.First(&row, "token_id = ? AND user_id = ?", tokenID, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return retailprice.KeyPolicy{}, 0, nil
	}
	if err != nil {
		return retailprice.KeyPolicy{}, 0, err
	}
	var policy retailprice.KeyPolicy
	if common.UnmarshalJsonStr(row.Payload, &policy) != nil {
		return policy, 0, retailprice.ErrConfig
	}
	return policy, row.Revision, nil
}

// PutRetailKeyPolicy grants only products already allowed to the user's current server-side group.
func PutRetailKeyPolicy(userID, tokenID int, expected int64, policy retailprice.KeyPolicy) error {
	if userID <= 0 || tokenID <= 0 || expected < 0 {
		return retailprice.ErrConfig
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		var book retailprice.Book
		if common.UnmarshalJsonStr(state.Payload, &book) != nil {
			return retailprice.ErrConfig
		}
		var user User
		if err := tx.Select("id", "group").First(&user, userID).Error; err != nil {
			return retailprice.ErrAccess
		}
		var token Token
		if err := tx.Select("id").First(&token, "id = ? AND user_id = ? AND service_mode = ?", tokenID, userID, TokenServiceRetail).Error; err != nil {
			return retailprice.ErrAccess
		}
		allowedModels := map[string]bool{}
		for _, p := range book.Products {
			if p.ID == policy.ProductID && slices.Contains(p.UserGroups, user.Group) {
				for _, r := range p.Routes {
					for name := range r.Models {
						allowedModels[name] = true
					}
				}
			}
		}
		for name := range policy.Caps {
			if !allowedModels[name] {
				return retailprice.ErrAccess
			}
		}
		var row RetailKeyPricePolicy
		err = tx.First(&row, "token_id = ?", tokenID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if row.Revision != expected {
			return ErrRetailPriceConflict
		}
		payload, err := common.Marshal(policy)
		if err != nil {
			return err
		}
		row = RetailKeyPricePolicy{TokenID: tokenID, UserID: userID, Revision: expected + 1, Payload: string(payload)}
		return tx.Save(&row).Error
	})
}

// RetailQuote constructs a customer quote from a fresh book and verified public supply candidates.
func RetailQuote(userID, tokenID int, modelName string, outputLimit int, eligible []int) (*retailprice.Quote, error) {
	book, version, err := GetRetailPriceBook()
	if err != nil {
		return nil, err
	}
	if !book.Enabled {
		if version > 0 {
			return nil, retailprice.ErrExpired
		}
		return nil, nil
	}
	policy, _, err := GetRetailKeyPolicy(userID, tokenID)
	if err != nil {
		return nil, err
	}
	var user User
	if err := DB.Select("id", "group", "status").First(&user, userID).Error; err != nil || user.Status != common.UserStatusEnabled {
		return nil, retailprice.ErrAccess
	}
	return book.MakeQuote(version, user.Group, policy, modelName, outputLimit, eligible, time.Now().Unix())
}

// RetailCNYQuota converts a frozen RMB quote to legacy wallet units without changing historical balances.
func RetailCNYQuota(amount decimal.Decimal, walletRate string) (int, decimal.Decimal, error) {
	rate, err := retailprice.Amount(walletRate)
	if err != nil || !rate.IsPositive() || amount.IsNegative() {
		return 0, decimal.Zero, retailprice.ErrConfig
	}
	// Divide after scaling to wallet quota and retain the remainder, so even the
	// smallest positive charge reserves one quota unit rather than rounding to zero.
	units, remainder := amount.Mul(decimal.NewFromFloat(common.QuotaPerUnit)).QuoRem(rate, 0)
	if remainder.IsPositive() {
		units = units.Add(decimal.NewFromInt(1))
	}
	quota, clamp := common.QuotaFromDecimalChecked(units)
	if clamp != nil {
		return 0, decimal.Zero, retailprice.ErrBudget
	}
	actual := decimal.NewFromInt(int64(quota)).Mul(rate).Div(decimal.NewFromFloat(common.QuotaPerUnit)).RoundCeil(8)
	return quota, actual, nil
}

// ReservePricedRetailCharge atomically revalidates price/permissions, occupies a daily budget and reserves funds.
func ReservePricedRetailCharge(id string, userID, tokenID int, quote *retailprice.Quote) (int, error) {
	amount, err := retailprice.Amount(quote.ReservedCNY)
	if err != nil {
		return 0, err
	}
	quota, amount, err := RetailCNYQuota(amount, quote.WalletCNYPerUnit)
	if err != nil {
		return 0, err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		state, err := lockRetailPriceState(tx)
		if err != nil {
			return err
		}
		if state.Version != quote.Version || quote.ValidUntil <= time.Now().Unix() {
			return ErrRetailPriceConflict
		}
		var book retailprice.Book
		if common.UnmarshalJsonStr(state.Payload, &book) != nil || !book.Enabled {
			return retailprice.ErrConfig
		}
		var row RetailKeyPricePolicy
		if err := tx.First(&row, "token_id = ? AND user_id = ?", tokenID, userID).Error; err != nil {
			return retailprice.ErrAccess
		}
		var policy retailprice.KeyPolicy
		if common.UnmarshalJsonStr(row.Payload, &policy) != nil {
			return retailprice.ErrConfig
		}
		var user User
		if err := tx.Select("id", "group", "status").First(&user, userID).Error; err != nil || user.Status != common.UserStatusEnabled {
			return retailprice.ErrAccess
		}
		var token Token
		if err := tx.Select("id", "status", "expired_time").First(&token, "id = ? AND user_id = ? AND service_mode = ?", tokenID, userID, TokenServiceRetail).Error; err != nil || token.Status != common.TokenStatusEnabled || token.ExpiredTime != -1 && token.ExpiredTime < time.Now().Unix() {
			return retailprice.ErrAccess
		}
		fresh, err := book.MakeQuote(state.Version, user.Group, policy, quote.Model.ID, quote.OutputLimit, quote.Channels, time.Now().Unix())
		if err != nil {
			return err
		}
		if fresh.ProductID != quote.ProductID {
			return retailprice.ErrAccess
		}
		limit, _ := retailprice.Amount(policy.MaxRequestCNY)
		if amount.GreaterThan(limit) {
			return retailprice.ErrBudget
		}
		day := time.Now().UTC().Format("2006-01-02")
		budgetID := fmt.Sprintf("%d/%s", tokenID, day)
		budget := RetailPriceBudget{ID: budgetID, Reserved: "0", Spent: "0"}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&budget).Error; err != nil {
			return err
		}
		if err := tx.First(&budget, "id = ?", budgetID).Error; err != nil {
			return err
		}
		reserved, err := retailprice.Amount(budget.Reserved)
		if err != nil {
			return err
		}
		spent, err := retailprice.Amount(budget.Spent)
		if err != nil {
			return err
		}
		daily, _ := retailprice.Amount(policy.DailyBudgetCNY)
		if reserved.Add(spent).Add(amount).GreaterThan(daily) {
			return retailprice.ErrBudget
		}
		if err := tx.Model(&budget).Update("reserved", reserved.Add(amount).String()).Error; err != nil {
			return err
		}
		quote.ReservedCNY = amount.String()
		payload, err := common.Marshal(quote)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		charge := RetailCharge{ID: id, UserID: userID, TokenID: tokenID, Reserved: quota, Status: "reserved", PriceSnapshot: string(payload), CreatedAt: now, UpdatedAt: now, PricingVersion: quote.Version, WalletCNYPerUnit: quote.WalletCNYPerUnit, ReservedCNY: amount.String(), ActualCNY: "0", BudgetID: budgetID}
		if err := tx.Create(&charge).Error; err != nil {
			return err
		}
		return applyRetailDelta(tx, userID, tokenID, quota)
	})
	invalidateRetailCaches(userID, tokenID)
	return quota, err
}

// settleRetailPriceBudget releases the original day's reservation, even after midnight or a policy change.
// Caller holds the publication lock and the charge lock; retries cannot double-refund a budget.
func settleRetailPriceBudget(tx *gorm.DB, charge RetailCharge, target int) (string, error) {
	if target > charge.Reserved {
		return "", retailprice.ErrBudget
	}
	rate, err := retailprice.Amount(charge.WalletCNYPerUnit)
	if err != nil {
		return "", err
	}
	actual := decimal.NewFromInt(int64(target)).Mul(rate).Div(decimal.NewFromFloat(common.QuotaPerUnit)).RoundCeil(8)
	var budget RetailPriceBudget
	if err := tx.First(&budget, "id = ?", charge.BudgetID).Error; err != nil {
		return "", err
	}
	reserved, err := retailprice.Amount(budget.Reserved)
	if err != nil {
		return "", err
	}
	spent, err := retailprice.Amount(budget.Spent)
	if err != nil {
		return "", err
	}
	original, err := retailprice.Amount(charge.ReservedCNY)
	if err != nil || reserved.LessThan(original) {
		return "", ErrRetailSettlement
	}
	if err := tx.Model(&budget).Updates(map[string]any{"reserved": reserved.Sub(original).String(), "spent": spent.Add(actual).String()}).Error; err != nil {
		return "", err
	}
	return actual.String(), nil
}
