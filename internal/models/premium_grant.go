package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PremiumGrant is the record of one admin handing premium to a user.
//
// Premium here is not sold through a payment provider - an admin flips
// it on by hand after a bank transfer, or gives it away to a relative, a
// partner centre, a contest winner. Until now the flip left no trace at
// all: a month later nobody could say whether an account was revenue or
// a favour, and with several admins nobody could say who gave it.
//
// So every grant writes a row here. Kind is what makes the ledger
// useful - "how much did we actually sell this month" is a sum over
// KindSale, and every other reason is deliberately NOT counted as income.
type PremiumGrant struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Who received it.
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	User   *User     `gorm:"foreignKey:UserID" json:"user,omitempty"`

	// Which admin granted it. Nullable so the row survives the admin
	// account being deleted; the name is snapshotted for the same reason.
	GrantedByID   *uuid.UUID `gorm:"type:uuid;index" json:"granted_by_id"`
	GrantedByName string     `gorm:"size:128" json:"granted_by_name"`

	// Why. See the Kind* constants - this is the point of the table.
	Kind string `gorm:"size:24;not null;index" json:"kind"`

	// AmountUZS is the money actually received, in whole so'm. Required
	// for KindSale, left at 0 for the free kinds. Whole so'm rather than
	// tiyin: these are manual bank transfers typed in by a human, and
	// nobody types tiyin.
	AmountUZS int `gorm:"default:0" json:"amount_uzs"`

	// Free-text context an admin would otherwise keep in their head:
	// "Aziz aka's cousin", "paid via Payme, receipt in the chat".
	Note string `gorm:"size:512" json:"note"`

	Months int `json:"months"`

	// The window before and after this grant. Stored because premium
	// STACKS - without the "before" value, three months added on top of
	// an existing year is indistinguishable from a fresh three-month sale.
	PreviousExpiresAt *time.Time `json:"previous_expires_at"`
	ExpiresAt         time.Time  `json:"expires_at"`

	// Discount coupons burned by this grant, if any. Kept so a sale that
	// looks under-priced can be explained instead of investigated.
	DiscountPercent int `gorm:"default:0" json:"discount_percent"`
	CouponsUsed     int `gorm:"default:0" json:"coupons_used"`
}

// Grant kinds. Only KindSale counts as revenue; keeping the free reasons
// apart is what lets the ledger answer "who got this for nothing, and on
// whose word".
const (
	// KindSale - the user paid us. AmountUZS is required.
	KindSale = "sale"
	// KindFriend - a relative, a friend, someone's acquaintance. Free.
	KindFriend = "friend"
	// KindPartner - a learning centre, blogger or other partnership.
	KindPartner = "partner"
	// KindPromo - contest prize, giveaway, marketing campaign.
	KindPromo = "promo"
	// KindCompensation - an apology for an outage or a lost session.
	KindCompensation = "compensation"
	// KindTest - our own accounts, QA. Excluded from every report.
	KindTest = "test"
)

// ValidGrantKind reports whether kind is one we accept. The API rejects
// anything else rather than silently storing a typo that would then be
// missing from every filter.
func ValidGrantKind(kind string) bool {
	switch kind {
	case KindSale, KindFriend, KindPartner, KindPromo, KindCompensation, KindTest:
		return true
	}
	return false
}

// IsRevenue reports whether a grant of this kind is money earned.
func IsRevenue(kind string) bool { return kind == KindSale }

func (g *PremiumGrant) BeforeCreate(tx *gorm.DB) error {
	if g.ID == uuid.Nil {
		g.ID = uuid.New()
	}
	return nil
}
