package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FeedbackTicket is a user-submitted message to the admin team.
//
// Status state machine:
//   sent      → initial state when user posts
//   viewed    → an admin opened it
//   resolved  → admin replied + marked done
//   cancelled → admin discarded
type FeedbackTicket struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`

	// "question" | "suggestion" | "complaint"
	Category string `gorm:"size:24;not null" json:"category"`
	Message  string `gorm:"type:text;not null" json:"message"`

	// "sent" | "viewed" | "resolved" | "cancelled"
	Status string `gorm:"size:16;not null;default:'sent';index" json:"status"`

	AdminReply *string    `gorm:"type:text" json:"admin_reply"`
	RepliedAt  *time.Time `json:"replied_at"`

	// UserSeenReply flips to true once the user actually loads their ticket
	// list after the admin replied - drives the "unread reply" badge on the
	// profile menu.
	UserSeenReply bool `gorm:"not null;default:false" json:"user_seen_reply"`

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (f *FeedbackTicket) BeforeCreate(tx *gorm.DB) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	return nil
}
