package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DictationSentence is an AI-generated sentence that tops up the Dictation Race
// bank beyond the embedded seed set, so the content stays fresh as the game is
// played. A scheduled job generates more whenever the pool drops below target.
type DictationSentence struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Text      string    `gorm:"size:255;not null;uniqueIndex" json:"text"`
	Level     string    `gorm:"size:8;not null" json:"level"`
	CreatedAt time.Time `json:"created_at"`
}

func (DictationSentence) TableName() string { return "dictation_sentences" }

func (d *DictationSentence) BeforeCreate(*gorm.DB) error {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	return nil
}
