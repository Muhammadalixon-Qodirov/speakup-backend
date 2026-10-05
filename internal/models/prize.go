package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Prize is a one-shot reward box. Each user gets 3 random days per month
// on which a Prize row's `available_on` falls. On that day the user can
// open the box; spinning chooses one of several outcomes weighted by
// rarity. See services/prize_service.go for the full rarity table.
type Prize struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`

	// Calendar day on which this prize box becomes openable.
	AvailableOn time.Time `gorm:"type:date;not null;index" json:"available_on"`

	// pending → openable today (or earlier and still unopened)
	// opened  → spun, prize granted
	// expired → day passed without being opened
	Status string `gorm:"size:16;not null;default:'pending'" json:"status"`

	// Result fields - populated when status flips to "opened".
	PrizeType  *string    `gorm:"size:32" json:"prize_type"`  // "minutes" | "discount" | "premium"
	PrizeValue *int       `json:"prize_value"`                 // minutes count, %, or premium days
	OpenedAt   *time.Time `json:"opened_at"`

	// UsedAt is non-nil once the prize has been "spent". For discounts this
	// is set the moment an admin grants premium that consumed the discount,
	// so the same coupon can never be applied twice. Minutes/premium prizes
	// don't use this field - they apply instantly when opened.
	UsedAt *time.Time `json:"used_at"`
}

func (p *Prize) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}
