package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Course is one line of a centre's course list, shown on the profile card
// a user reaches by tapping the in-session banner.
//
// Price is a free-text string on purpose: centres quote in wildly
// different shapes ("450 000 so'm/oy", "kelishilgan holda", "birinchi dars
// bepul") and forcing a number would just make them write nonsense in a
// field we'd then have to render as money.
type Course struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Duration    string `json:"duration,omitempty"`
	Price       string `json:"price,omitempty"`
}

// CourseList is a JSONB-backed slice. The whole list is edited as one
// unit (it's a short list on a profile form), so a JSON column beats a
// child table with four extra CRUD endpoints.
type CourseList []Course

func (c *CourseList) Scan(value interface{}) error {
	if value == nil {
		*c = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("CourseList: unsupported scan type")
	}
	if len(raw) == 0 {
		*c = nil
		return nil
	}
	return json.Unmarshal(raw, c)
}

func (c CourseList) Value() (driver.Value, error) {
	if c == nil {
		return "[]", nil
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (CourseList) GormDataType() string { return "jsonb" }

// Moderation states for a centre's public profile.
//
// Anything a centre writes about itself ends up on OTHER users' screens
// as an advertisement, so it cannot go live unreviewed - a centre could
// otherwise upload a competitor's logo, quote a price it doesn't honour,
// or post something plainly inappropriate.
const (
	// CenterModerationDraft - created but never submitted.
	CenterModerationDraft = "draft"
	// CenterModerationPending - waiting for a SpeakUp admin.
	CenterModerationPending = "pending"
	// CenterModerationApproved - the banner may show it.
	CenterModerationApproved = "approved"
	// CenterModerationRejected - admin refused; the note says why.
	CenterModerationRejected = "rejected"
)

// StudyCenter is a partner language school.
//
// Relationship to Room: a centre OWNS rooms (its groups). Today a centre
// usually has exactly one, which is why rooms.center_id is nullable and
// a room works perfectly well without a centre - but the column is here
// from day one so a centre that grows to five teachers doesn't need a
// data migration, just five rooms pointing at the same centre.
type StudyCenter struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// OwnerID is the "Markaz admin" account. Created by a SpeakUp admin -
	// there is no self-service signup, which is what stops anyone from
	// minting a partner profile and getting free advertising.
	OwnerID uuid.UUID `gorm:"type:uuid;not null;index" json:"owner_id"`
	Owner   *User     `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`

	// --- Profile: the centre fills these in itself ---

	Name string `gorm:"size:128;not null" json:"name"`
	// LogoURL is a path served by the app's static handler
	// (/uploads/centers/<id>.png), never an external URL - see
	// services.SaveCenterLogo for why we re-encode rather than store
	// whatever bytes were uploaded.
	LogoURL *string    `gorm:"size:512" json:"logo_url"`
	About   *string    `gorm:"size:1024" json:"about"`
	Courses CourseList `gorm:"type:jsonb" json:"courses"`

	// Contact block. Lower risk than name/logo/about, so edits here go
	// live without re-moderation (see services.CenterProfileNeedsReview).
	Phone        *string `gorm:"size:32" json:"phone"`
	Address      *string `gorm:"size:256" json:"address"`
	TelegramURL  *string `gorm:"size:256" json:"telegram_url"`
	InstagramURL *string `gorm:"size:256" json:"instagram_url"`
	WebsiteURL   *string `gorm:"size:256" json:"website_url"`
	// SignupURL backs the "ro'yxatdan o'tish" button on the profile card.
	SignupURL *string `gorm:"size:256" json:"signup_url"`

	// --- Moderation: SpeakUp admin controls ---

	ModerationStatus string     `gorm:"size:16;default:'draft';index" json:"moderation_status"`
	ModerationNote   *string    `gorm:"size:512" json:"moderation_note"`
	ReviewedAt       *time.Time `json:"reviewed_at"`
	ReviewedBy       *uuid.UUID `gorm:"type:uuid" json:"-"`

	// --- Contract: SpeakUp admin controls ---

	IsActive bool `gorm:"default:true;index" json:"is_active"`
	// IsAdvertised is the contract term: may this centre appear in the
	// in-session banner rotation? Separate from IsActive so a centre can
	// keep its rooms and dashboard after the advertising deal lapses.
	IsAdvertised      bool       `gorm:"default:false;index" json:"is_advertised"`
	ContractExpiresAt *time.Time `json:"contract_expires_at"`

	// --- Banner analytics: what we actually sell ---

	BannerImpressions int64 `gorm:"default:0" json:"banner_impressions"`
	BannerClicks      int64 `gorm:"default:0" json:"banner_clicks"`
}

func (sc *StudyCenter) BeforeCreate(tx *gorm.DB) error {
	if sc.ID == uuid.Nil {
		sc.ID = uuid.New()
	}
	if sc.ModerationStatus == "" {
		sc.ModerationStatus = CenterModerationDraft
	}
	return nil
}

// ContractLive reports whether the partnership is currently in force.
func (sc *StudyCenter) ContractLive() bool {
	if sc == nil || !sc.IsActive {
		return false
	}
	if sc.ContractExpiresAt != nil && sc.ContractExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}

// BannerEligible reports whether this centre may appear in the rotation.
//
// Every clause is a separate promise:
//   - ContractLive  - we only advertise partners we currently have a deal with
//   - IsAdvertised  - advertising is a contract term, not automatic
//   - approved      - a human at SpeakUp has seen exactly this content
//   - LogoURL       - a banner with no logo is a blank rectangle
func (sc *StudyCenter) BannerEligible() bool {
	if !sc.ContractLive() || !sc.IsAdvertised {
		return false
	}
	if sc.ModerationStatus != CenterModerationApproved {
		return false
	}
	return sc.LogoURL != nil && *sc.LogoURL != ""
}
