package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Topic-word review statuses for the Vocabulary Sprint self-learning loop.
const (
	TopicWordPending  = "pending"  // accumulated, awaiting weekly AI review
	TopicWordTrusted  = "trusted"  // AI confirmed on-topic
	TopicWordRejected = "rejected" // AI judged clearly off-topic
)

// TopicWord is one (topic, word) pair the Vocabulary Sprint has seen players
// use. It powers the self-learning topic categoriser: runtime accumulates
// usage here, and a weekly AI job promotes words to "trusted" (on-topic) or
// demotes clearly off-topic ones to "rejected".
type TopicWord struct {
	ID    uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Topic string    `gorm:"size:64;not null;uniqueIndex:idx_topic_word" json:"topic"`
	Word  string    `gorm:"size:64;not null;uniqueIndex:idx_topic_word" json:"word"`

	Uses   int     `gorm:"default:0" json:"uses"`     // times accepted across games
	SumSim float64 `gorm:"default:0" json:"sum_sim"`  // Σ cosine sim; avg = SumSim/Uses

	Status         string     `gorm:"size:16;default:'pending';index" json:"status"`
	LastReviewedAt *time.Time `json:"last_reviewed_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (TopicWord) TableName() string { return "topic_words" }

func (t *TopicWord) BeforeCreate(*gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}
