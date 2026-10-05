package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type WordBank struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID      uuid.UUID  `gorm:"type:uuid;not null;index" json:"user_id"`
	SessionID   *uuid.UUID `gorm:"type:uuid" json:"session_id"`
	Word        string     `gorm:"size:256;not null" json:"word"`
	Translation *string    `gorm:"size:256" json:"translation"`
	Context     *string    `gorm:"type:text" json:"context"` // sentence it was used in

	User    User     `gorm:"foreignKey:UserID" json:"-"`
	Session *Session `gorm:"foreignKey:SessionID" json:"-"`
}

func (w *WordBank) BeforeCreate(tx *gorm.DB) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	return nil
}
