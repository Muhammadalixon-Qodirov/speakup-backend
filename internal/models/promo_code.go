package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PromoCode is a redeemable string that grants the holder a fixed
// number of premium days. Created and managed by admins from the
// admin panel. Soft-deleted via gorm.DeletedAt so historical
// redemptions still resolve their parent code.
//
// Validation rules enforced at redeem time (services/promo_service):
//   - `is_active` must be true
//   - `expires_at` must be null OR in the future
//   - `max_uses` (if non-zero) must be > current `redeemed_count`
//   - the same user cannot redeem the same code twice (enforced by
//     the unique index on PromoRedemption)
type PromoCode struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Code stored UPPERCASE to keep the lookup case-insensitive at
	// the cheapest possible cost - the DB never has to ILIKE.
	// Trimmed of whitespace before insert.
	Code string `gorm:"size:64;uniqueIndex;not null" json:"code"`

	// Number of premium days granted on each successful redemption.
	// 30 = one month, 365 = one year. Must be > 0.
	PremiumDays int `gorm:"not null;default:30" json:"premium_days"`

	// Optional cap on total redemptions. Zero = unlimited.
	MaxUses int `gorm:"not null;default:0" json:"max_uses"`

	// Optional expiry date. Null = never expires.
	ExpiresAt *time.Time `json:"expires_at"`

	// Soft enable/disable without deleting - admin can re-enable
	// later. Default true so newly created codes are immediately
	// usable.
	IsActive bool `gorm:"not null;default:true" json:"is_active"`

	// Atomically incremented inside RedeemPromoCode. Used both for
	// max_uses checks and for the admin "X people used this code"
	// stat (cheap to read, no COUNT(*) over redemptions).
	RedeemedCount int `gorm:"not null;default:0" json:"redeemed_count"`

	// Who created this code (audit). Optional - null if seeded by a
	// migration.
	CreatedBy *uuid.UUID `gorm:"type:uuid" json:"created_by"`
}

func (p *PromoCode) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}
