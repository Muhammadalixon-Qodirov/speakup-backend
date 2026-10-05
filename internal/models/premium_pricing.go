package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PremiumSettings is the single row that holds what premium costs and
// where the money is sent.
//
// All of this used to be typed into the frontend as constants - the
// price, the card number, the admin's Telegram handle. Changing any of
// them meant a code change and a deploy, so in practice they never
// changed: a price rise or a new card had to wait for whoever could
// build the app. This table moves those three facts to the admin panel,
// where the person who actually decides them can edit them.
//
// Exactly one row exists. It is created on first read with the values
// that were hard-coded before, so an untouched install behaves exactly
// as it did.
type PremiumSettings struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// BasePriceUZS is the undiscounted month price in whole so'm.
	BasePriceUZS int `gorm:"not null;default:30000" json:"base_price_uzs"`

	// Payment destination shown in the purchase sheet.
	CardNumber    string `gorm:"size:32;not null;default:''" json:"card_number"`
	CardOwner     string `gorm:"size:128;not null;default:''" json:"card_owner"`
	AdminUsername string `gorm:"size:64;not null;default:''" json:"admin_username"`

	// Who last touched it. Snapshotted by name so the audit line
	// survives the admin account being removed.
	UpdatedByID   *uuid.UUID `gorm:"type:uuid" json:"updated_by_id"`
	UpdatedByName string     `gorm:"size:128;not null;default:''" json:"updated_by_name"`
}

// Defaults for the singleton row - deliberately the exact values that
// were compiled into the frontend before this table existed.
const (
	DefaultPremiumPriceUZS = 30000
	DefaultPremiumCard     = "4023060514050896"
	DefaultPremiumOwner    = "Nozimjonov X"
	DefaultPremiumAdminTG  = "xojiakbar_nozimjonov"
)

func (s *PremiumSettings) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}

// PremiumCampaign is one time-boxed discount with a reason attached.
//
// A bare "-30%" tells a user nothing and reads like a trick. A discount
// that says WHY it exists - Navro'z, the app's birthday, a weekend
// flash sale - is the same money but a different message, so the reason
// is a first-class field here rather than something the admin has to
// smuggle into a title. Theme picks the visual treatment the Mini App
// wraps around it.
//
// Campaigns are independent of the prize-wheel coupons a user may hold:
// see services.ResolvePremiumPricing for how the two combine.
type PremiumCampaign struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Title is the headline the user sees: "Navro'z sovg'asi".
	Title string `gorm:"size:120;not null" json:"title"`

	// Reason is the human "why", shown under the title:
	// "Bahor bayrami munosabati bilan".
	Reason string `gorm:"size:240;not null;default:''" json:"reason"`

	// Emoji is a single glyph used as the campaign's badge. Optional -
	// the theme supplies one when this is empty.
	Emoji string `gorm:"size:16;not null;default:''" json:"emoji"`

	// Theme selects the gradient/wording treatment on the client.
	// See the Theme* constants.
	Theme string `gorm:"size:24;not null;default:'holiday'" json:"theme"`

	// DiscountPercent is 1..90 off the base price.
	DiscountPercent int `gorm:"not null" json:"discount_percent"`

	// Window. Nil StartsAt means "already running"; nil EndsAt means
	// "until an admin turns it off" - no countdown is shown then.
	StartsAt *time.Time `gorm:"index" json:"starts_at"`
	EndsAt   *time.Time `gorm:"index" json:"ends_at"`

	// IsActive is the manual switch, checked on top of the window, so a
	// campaign can be parked without losing its dates.
	IsActive bool `gorm:"not null;default:true" json:"is_active"`

	// StacksWithCoupons decides what happens when the user also holds
	// prize-wheel discount coupons. False (the default) means the user
	// gets whichever is larger and keeps the coupons for later; true
	// adds them together, capped by MaxTotalDiscountPercent.
	StacksWithCoupons bool `gorm:"not null;default:false" json:"stacks_with_coupons"`

	CreatedByID   *uuid.UUID `gorm:"type:uuid" json:"created_by_id"`
	CreatedByName string     `gorm:"size:128;not null;default:''" json:"created_by_name"`
}

// Campaign themes. Each one is a visual treatment on the client, not a
// behaviour - any theme works with any discount and any reason.
const (
	ThemeHoliday  = "holiday"  // bayram - warm gold
	ThemeNewYear  = "newyear"  // yangi yil - blue/violet
	ThemeRamadan  = "ramadan"  // ramazon - deep green
	ThemeFlash    = "flash"    // short flash sale - hot red
	ThemeBirthday = "birthday" // our own anniversary - pink
	ThemeStudent  = "student"  // back-to-school - indigo
	ThemeCustom   = "custom"   // brand gradient, no seasonal colouring
)

// ValidCampaignTheme reports whether theme is one the client can render.
// Unknown themes are rejected rather than stored, so no campaign can
// silently fall back to an unstyled box on users' screens.
func ValidCampaignTheme(theme string) bool {
	switch theme {
	case ThemeHoliday, ThemeNewYear, ThemeRamadan, ThemeFlash,
		ThemeBirthday, ThemeStudent, ThemeCustom:
		return true
	}
	return false
}

// IsLive reports whether the campaign should be offered at time now -
// switched on, started, and not yet finished.
func (c *PremiumCampaign) IsLive(now time.Time) bool {
	if !c.IsActive || c.DiscountPercent <= 0 {
		return false
	}
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return false
	}
	if c.EndsAt != nil && !now.Before(*c.EndsAt) {
		return false
	}
	return true
}

func (c *PremiumCampaign) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// CampaignThemes lists every theme in the order the admin picker shows
// them. Served to the admin panel so the two never drift apart.
func CampaignThemes() []string {
	return []string{
		ThemeHoliday, ThemeNewYear, ThemeRamadan, ThemeFlash,
		ThemeBirthday, ThemeStudent, ThemeCustom,
	}
}
