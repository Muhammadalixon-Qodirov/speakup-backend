package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Testimonial is a public review a user writes about SpeakUp. It's hidden
// until an admin approves it (moderation), then surfaces in the rotating
// "what users say" carousel. One per user (unique user_id): resubmitting
// replaces the old one and sends it back to pending.
//
// Status: "pending" → awaiting moderation
//
//	"approved" → visible to everyone
//	"rejected" → hidden, admin declined
type Testimonial struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"user_id"`

	Text   string `gorm:"type:text;not null" json:"text"`
	Rating int    `gorm:"not null;default:5" json:"rating"` // 1-5 stars

	// "pending" | "approved" | "rejected"
	Status     string     `gorm:"size:16;not null;default:'pending';index" json:"status"`
	ReviewedAt *time.Time `json:"reviewed_at"`

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (tm *Testimonial) BeforeCreate(tx *gorm.DB) error {
	if tm.ID == uuid.Nil {
		tm.ID = uuid.New()
	}
	return nil
}
