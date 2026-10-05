package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuthOTPSession stores web login sessions.
// Flow: pending → completed (bot sets after Telegram auth)
type AuthOTPSession struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"-"`
	SessionToken string    `gorm:"size:64;uniqueIndex;not null" json:"-"`
	Status       string    `gorm:"size:20;default:'pending'" json:"-"` // pending | completed
	TelegramID   int64     `gorm:"default:0" json:"-"`
	ExpiresAt    time.Time `json:"-"`
	CreatedAt    time.Time `json:"-"`
}

func (a *AuthOTPSession) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

func (a *AuthOTPSession) IsExpired() bool {
	return time.Now().After(a.ExpiresAt)
}
