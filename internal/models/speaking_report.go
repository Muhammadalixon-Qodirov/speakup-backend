package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SpeakingReport stores IELTS-style AI analysis.
// This is a STANDALONE test - not tied to 1v1 sessions.
type SpeakingReport struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	Topic  string    `gorm:"size:512" json:"topic"`

	// Transcript from Whisper
	Transcript  string `gorm:"type:text" json:"transcript"`
	WordCount   int    `gorm:"default:0" json:"word_count"`
	DurationSec int    `gorm:"default:0" json:"duration_sec"`

	// IELTS Band Scores (1.0 - 9.0, step 0.5)
	FluencyScore       float64 `gorm:"default:0" json:"fluency_score"`
	LexicalScore       float64 `gorm:"default:0" json:"lexical_score"`
	GrammarScore       float64 `gorm:"default:0" json:"grammar_score"`
	PronunciationScore float64 `gorm:"default:0" json:"pronunciation_score"`
	OverallBand        float64 `gorm:"default:0" json:"overall_band"`

	// Detailed feedback
	FluencyFeedback       string `gorm:"type:text" json:"fluency_feedback"`
	LexicalFeedback       string `gorm:"type:text" json:"lexical_feedback"`
	GrammarFeedback       string `gorm:"type:text" json:"grammar_feedback"`
	PronunciationFeedback string `gorm:"type:text" json:"pronunciation_feedback"`

	// Errors and suggestions (JSON arrays)
	GrammarErrors string `gorm:"type:text" json:"grammar_errors"`
	Suggestions   string `gorm:"type:text" json:"suggestions"`

	// Metrics from Whisper
	WordsPerMinute float64 `gorm:"default:0" json:"words_per_minute"`
	PauseCount     int     `gorm:"default:0" json:"pause_count"`
	AvgConfidence  float64 `gorm:"default:0" json:"avg_confidence"`

	// Improve result (JSON blob from /ai/improve, nullable)
	ImproveResult *string `gorm:"type:text" json:"improve_result,omitempty"`

	User User `gorm:"foreignKey:UserID" json:"-"`
}

func (r *SpeakingReport) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
