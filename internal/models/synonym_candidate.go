package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Synonym-candidate review statuses for the Synonym Duel self-learning loop.
const (
	SynonymPending  = "pending"  // a player answer not in the bank, awaiting AI review
	SynonymAccepted = "accepted" // AI confirmed it's a valid synonym/antonym
	SynonymRejected = "rejected" // AI judged it wrong
)

// SynonymCandidate is a player answer that wasn't in the embedded synonyms bank
// for a given (word, type) prompt. The weekly AI job decides whether it's a
// genuine synonym/antonym; accepted ones then count as correct at runtime
// (via an in-memory cache) without rebuilding the binary.
type SynonymCandidate struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Word   string    `gorm:"size:64;not null;uniqueIndex:idx_syn_candidate" json:"word"`
	Type   string    `gorm:"size:16;not null;uniqueIndex:idx_syn_candidate" json:"type"` // "synonym"|"antonym"
	Answer string    `gorm:"size:64;not null;uniqueIndex:idx_syn_candidate" json:"answer"`

	Uses           int        `gorm:"default:0" json:"uses"`
	Status         string     `gorm:"size:16;default:'pending';index" json:"status"`
	LastReviewedAt *time.Time `json:"last_reviewed_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (SynonymCandidate) TableName() string { return "synonym_candidates" }

func (s *SynonymCandidate) BeforeCreate(*gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}
