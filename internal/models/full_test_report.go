package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// FullTestReport stores a complete IELTS-style test (Part 1 + 2 + 3).
type FullTestReport struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID  uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	TopicID string    `gorm:"size:64" json:"topic_id"`

	// Part scores (each 1.0-9.0)
	Part1Fluency       float64 `json:"part1_fluency"`
	Part1Lexical       float64 `json:"part1_lexical"`
	Part1Grammar       float64 `json:"part1_grammar"`
	Part1Pronunciation float64 `json:"part1_pronunciation"`
	Part1Band          float64 `json:"part1_band"`
	Part1Feedback      string  `gorm:"type:text" json:"part1_feedback"`
	Part1Transcript    string  `gorm:"type:text" json:"part1_transcript"`

	Part2Fluency       float64 `json:"part2_fluency"`
	Part2Lexical       float64 `json:"part2_lexical"`
	Part2Grammar       float64 `json:"part2_grammar"`
	Part2Pronunciation float64 `json:"part2_pronunciation"`
	Part2Band          float64 `json:"part2_band"`
	Part2Feedback      string  `gorm:"type:text" json:"part2_feedback"`
	Part2Transcript    string  `gorm:"type:text" json:"part2_transcript"`

	Part3Fluency       float64 `json:"part3_fluency"`
	Part3Lexical       float64 `json:"part3_lexical"`
	Part3Grammar       float64 `json:"part3_grammar"`
	Part3Pronunciation float64 `json:"part3_pronunciation"`
	Part3Band          float64 `json:"part3_band"`
	Part3Feedback      string  `gorm:"type:text" json:"part3_feedback"`
	Part3Transcript    string  `gorm:"type:text" json:"part3_transcript"`

	// Overall
	OverallBand    float64 `json:"overall_band"`
	GrammarErrors  string  `gorm:"type:text" json:"grammar_errors"`
	Suggestions    string  `gorm:"type:text" json:"suggestions"`
	TotalWords     int     `json:"total_words"`
	TotalDuration  int     `json:"total_duration"`
	WordsPerMinute float64 `json:"words_per_minute"`

	// Improve result (JSON blob from /ai/improve, nullable)
	ImproveResult *string `gorm:"type:text" json:"improve_result,omitempty"`

	// Status: pending_part2 | pending_part3 | completed
	Status string `gorm:"size:32;default:'pending_part2'" json:"status"`

	User User `gorm:"foreignKey:UserID" json:"-"`
}

func (r *FullTestReport) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
