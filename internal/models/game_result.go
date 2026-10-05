package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GameResult is one finished in-session mini-game. It's the permanent
// record behind the Game Leaderboard and a user's game history. Live
// game state lives only in memory (internal/games) - just like chat,
// only the OUTCOME is persisted here.
//
// WinnerID is nil for a draw OR a collaborative game (Story Chain),
// which have no winner by design. Score columns hold whatever the
// engine computed; XP columns hold what was actually awarded.
type GameResult struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	GameType string `gorm:"size:32;not null;index" json:"game_type"`

	// The speaking session this game was played inside (nullable - a
	// game could in theory be started outside a session in future).
	SessionID *uuid.UUID `gorm:"type:uuid;index" json:"session_id"`

	Player1ID uuid.UUID `gorm:"type:uuid;not null;index" json:"player1_id"`
	Player2ID uuid.UUID `gorm:"type:uuid;not null;index" json:"player2_id"`

	// Nil = draw or collaborative game (no winner).
	WinnerID *uuid.UUID `gorm:"type:uuid;index" json:"winner_id"`

	Score1 int `gorm:"default:0" json:"score1"`
	Score2 int `gorm:"default:0" json:"score2"`
	XP1    int `gorm:"default:0" json:"xp1"`
	XP2    int `gorm:"default:0" json:"xp2"`

	// Game-specific summary for the result screen / analytics, stored as
	// JSON text (e.g. word lists, story text, round breakdown). Kept as a
	// string so we don't pull in a new datatypes dependency.
	Meta string `gorm:"type:jsonb" json:"meta,omitempty"`
}

func (g *GameResult) BeforeCreate(tx *gorm.DB) error {
	if g.ID == uuid.Nil {
		g.ID = uuid.New()
	}
	return nil
}
