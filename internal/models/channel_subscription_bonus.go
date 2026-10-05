package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ChannelSubscriptionBonus tracks the one-shot reward a user gets for
// subscribing to @speakup_app. The row's mere existence means "claimed";
// the unique index on user_id enforces the one-per-user rule at the
// database level so concurrent double-claims can't slip through.
type ChannelSubscriptionBonus struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"user_id"`
	Channel   string    `gorm:"size:64;not null" json:"channel"`   // "speakup_app"
	Minutes   int       `gorm:"not null;default:10" json:"minutes"`
	GrantedAt time.Time `json:"granted_at"`
}

func (b *ChannelSubscriptionBonus) BeforeCreate(tx *gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	if b.GrantedAt.IsZero() {
		b.GrantedAt = time.Now()
	}
	return nil
}
