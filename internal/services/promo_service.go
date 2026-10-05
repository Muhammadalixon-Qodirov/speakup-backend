package services

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PromoRedeemResult holds the outcome of a successful redemption so
// the handler can return a tidy JSON response without a second DB hit.
type PromoRedeemResult struct {
	PromoCode        *models.PromoCode `json:"promo_code"`
	PremiumDays      int               `json:"premium_days"`
	PremiumExpiresAt time.Time         `json:"premium_expires_at"`
}

// Errors callers can compare against to decide HTTP status codes.
var (
	ErrPromoNotFound       = errors.New("promo kod topilmadi")
	ErrPromoInactive       = errors.New("promo kod faollashtirilmagan")
	ErrPromoExpired        = errors.New("promo kod muddati tugagan")
	ErrPromoExhausted      = errors.New("promo kod ishlatilish chegarasiga yetdi")
	ErrPromoAlreadyClaimed = errors.New("siz bu promo koddan allaqachon foydalangansiz")
	ErrPromoInvalid        = errors.New("promo kod kiritilmagan")
)

// NormalizePromoCode trims whitespace and uppercases - used both at
// insert time (admin creates a code) and at lookup time (user types
// it). Keeps the DB index lookup case-insensitive without an ILIKE.
func NormalizePromoCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// RedeemPromoCode runs the whole grant flow inside a single DB
// transaction so a crash between "find code" and "credit user" can
// never leave the system half-done. Steps:
//
//  1. SELECT FOR UPDATE on the promo code row (lock against
//     concurrent redemptions).
//  2. Validate is_active / expires_at / max_uses.
//  3. Insert the redemption row - the (promo_code_id, user_id)
//     unique index handles the "already claimed" race.
//  4. Increment redeemed_count.
//  5. Stack premium days on top of the user's current expiry
//     (same rule as admin GrantPremium - never resets a longer
//     window).
//
// Returns ErrPromoAlreadyClaimed if the user has already redeemed,
// even if the second attempt comes in via a parallel request.
func RedeemPromoCode(userID uuid.UUID, rawCode string) (*PromoRedeemResult, error) {
	code := NormalizePromoCode(rawCode)
	if code == "" {
		return nil, ErrPromoInvalid
	}

	var result *PromoRedeemResult

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 1. Lock the promo code row for the duration of the tx so
		//    concurrent redemptions serialise on the same row.
		var promo models.PromoCode
		err := tx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("code = ?", code).
			First(&promo).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPromoNotFound
		}
		if err != nil {
			return err
		}

		// 2. Validate state.
		if !promo.IsActive {
			return ErrPromoInactive
		}
		now := time.Now()
		if promo.ExpiresAt != nil && !promo.ExpiresAt.After(now) {
			return ErrPromoExpired
		}
		if promo.MaxUses > 0 && promo.RedeemedCount >= promo.MaxUses {
			return ErrPromoExhausted
		}

		// 3. Insert the redemption. The unique index on
		//    (promo_code_id, user_id) gives us atomic "already
		//    claimed" detection - no SELECT-then-INSERT race.
		redemption := models.PromoRedemption{
			PromoCodeID:        promo.ID,
			UserID:             userID,
			PremiumDaysGranted: promo.PremiumDays,
		}
		if err := tx.Create(&redemption).Error; err != nil {
			// Postgres unique violation surfaces as a generic
			// error; check by re-querying. Rare path so the
			// extra round-trip is fine.
			var existing models.PromoRedemption
			if tx.Where("promo_code_id = ? AND user_id = ?", promo.ID, userID).
				First(&existing).Error == nil {
				return ErrPromoAlreadyClaimed
			}
			return err
		}

		// 4. Bump the counter atomically.
		if err := tx.Model(&models.PromoCode{}).
			Where("id = ?", promo.ID).
			UpdateColumn("redeemed_count", gorm.Expr("redeemed_count + 1")).
			Error; err != nil {
			return err
		}
		promo.RedeemedCount++

		// 5. Credit the user. Stack on top of any existing window
		//    so a user with premium until next month gets +30 days
		//    on top, not a downgrade.
		var user models.User
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		base := now
		if user.IsPremium && user.PremiumExpiresAt != nil && user.PremiumExpiresAt.After(now) {
			base = *user.PremiumExpiresAt
		}
		expiry := base.AddDate(0, 0, promo.PremiumDays)

		if err := tx.Model(&user).Updates(map[string]interface{}{
			"is_premium":         true,
			"premium_expires_at": expiry,
		}).Error; err != nil {
			return err
		}

		result = &PromoRedeemResult{
			PromoCode:        &promo,
			PremiumDays:      promo.PremiumDays,
			PremiumExpiresAt: expiry,
		}
		return nil
	})

	if txErr != nil {
		return nil, txErr
	}
	return result, nil
}

// ListPromoCodes returns every (non-deleted) promo code, newest
// first. Used by the admin panel - the table is small (likely <100
// rows ever) so we don't bother with pagination.
func ListPromoCodes() ([]models.PromoCode, error) {
	var codes []models.PromoCode
	err := database.DB.Order("created_at DESC").Find(&codes).Error
	return codes, err
}

// GetPromoCode fetches one code by ID for the admin detail/edit
// view.
func GetPromoCode(id uuid.UUID) (*models.PromoCode, error) {
	var code models.PromoCode
	if err := database.DB.First(&code, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &code, nil
}

// CreatePromoCode inserts a new code after validating + normalising
// the input. Returns ErrPromoInvalid for empty codes and the
// usual GORM error (probably a unique-violation) if the code
// already exists.
func CreatePromoCode(
	rawCode string,
	premiumDays int,
	maxUses int,
	expiresAt *time.Time,
	isActive bool,
	createdBy *uuid.UUID,
) (*models.PromoCode, error) {
	code := NormalizePromoCode(rawCode)
	if code == "" {
		return nil, ErrPromoInvalid
	}
	if premiumDays < 1 || premiumDays > 3650 {
		return nil, errors.New("premium_days 1..3650 oralig'ida bo'lishi kerak")
	}
	if maxUses < 0 {
		return nil, errors.New("max_uses manfiy bo'la olmaydi")
	}

	promo := models.PromoCode{
		Code:        code,
		PremiumDays: premiumDays,
		MaxUses:     maxUses,
		ExpiresAt:   expiresAt,
		IsActive:    isActive,
		CreatedBy:   createdBy,
	}
	if err := database.DB.Create(&promo).Error; err != nil {
		return nil, err
	}
	return &promo, nil
}

// UpdatePromoCode applies the partial update to an existing code.
// Code itself is updatable (re-normalised) for the case where the
// admin made a typo on creation. PremiumDays / MaxUses / ExpiresAt
// / IsActive can all be tweaked in-place.
func UpdatePromoCode(id uuid.UUID, updates map[string]interface{}) (*models.PromoCode, error) {
	if v, ok := updates["code"]; ok {
		if s, ok := v.(string); ok {
			normalised := NormalizePromoCode(s)
			if normalised == "" {
				return nil, ErrPromoInvalid
			}
			updates["code"] = normalised
		}
	}
	if err := database.DB.Model(&models.PromoCode{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	return GetPromoCode(id)
}

// DeletePromoCode soft-deletes the code so historical redemptions
// keep their parent FK valid. To "really" forget a code, the admin
// would need DB access - intentional, since deletes are irreversible.
func DeletePromoCode(id uuid.UUID) error {
	res := database.DB.Delete(&models.PromoCode{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ListRedemptionsForCode returns every successful redemption of a
// given code with the user preloaded - drives the admin's "who
// used this code" sub-view.
func ListRedemptionsForCode(promoID uuid.UUID, limit int) ([]models.PromoRedemption, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []models.PromoRedemption
	err := database.DB.
		Where("promo_code_id = ?", promoID).
		Preload("User").
		Order("created_at DESC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}
