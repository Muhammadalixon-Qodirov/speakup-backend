package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PromoRedemption records a single use of a PromoCode by a User.
// The (PromoCodeID, UserID) composite unique index prevents a user
// from redeeming the same code twice - enforced at the DB layer so
// even a race between two concurrent POSTs from the same user
// surfaces as a clean unique-violation error.
type PromoRedemption struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	PromoCodeID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_promo_user,priority:1" json:"promo_code_id"`
	UserID      uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_promo_user,priority:2;index" json:"user_id"`

	// Snapshot of how many days were granted at redemption time.
	// We store this separately from PromoCode.PremiumDays in case
	// the admin later edits the promo code - the historical record
	// of "this user got X days" must stay accurate.
	PremiumDaysGranted int `gorm:"not null" json:"premium_days_granted"`

	// Joined when listing redemptions in the admin panel.
	User      User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
	PromoCode PromoCode `gorm:"foreignKey:PromoCodeID" json:"promo_code,omitempty"`
}

func (r *PromoRedemption) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
