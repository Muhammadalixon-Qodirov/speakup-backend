package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Session struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Participants
	User1ID uuid.UUID `gorm:"type:uuid;not null;index" json:"user1_id"`
	User2ID uuid.UUID `gorm:"type:uuid;not null;index" json:"user2_id"`
	User1   User      `gorm:"foreignKey:User1ID" json:"user1,omitempty"`
	User2   User      `gorm:"foreignKey:User2ID" json:"user2,omitempty"`

	// Session info
	Topic  *string `gorm:"size:256" json:"topic"`
	Status string  `gorm:"size:32;default:'active';index" json:"status"` // active | ended | cancelled

	// RoomID is set when the pair was matched inside a teacher's private
	// room instead of the public queue. It changes three things downstream:
	//   1. minutes are NOT charged against the free daily limit
	//   2. the session shows up in the teacher's room report
	//   3. LimitMinutes is the room's own limit, not the daily remainder
	// Nil for every ordinary public-queue session.
	RoomID *uuid.UUID `gorm:"type:uuid;index" json:"room_id"`

	// Timing
	StartedAt       *time.Time `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	DurationSeconds int        `gorm:"default:0" json:"duration_seconds"`

	// Limits
	IsPremiumSession bool `gorm:"default:false" json:"is_premium_session"`
	LimitMinutes     int  `gorm:"default:20" json:"limit_minutes"`

	// Relationships
	Ratings []SessionRating `gorm:"foreignKey:SessionID" json:"ratings,omitempty"`
}

func (s *Session) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}

// DurationMinutes returns session duration in minutes.
func (s *Session) DurationMinutes() int {
	return s.DurationSeconds / 60
}

type SessionRating struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	SessionID uuid.UUID `gorm:"type:uuid;not null;index" json:"session_id"`
	RaterID   uuid.UUID `gorm:"type:uuid;not null" json:"rater_id"`
	RateeID   uuid.UUID `gorm:"type:uuid;not null" json:"ratee_id"`
	Rating    int       `gorm:"not null" json:"rating"` // 1-5
	Feedback  *string   `gorm:"type:text" json:"feedback"`

	Session Session `gorm:"foreignKey:SessionID" json:"-"`
}

func (r *SessionRating) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}
