package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// StreakDay records a calendar day on which the user spoke at least the
// streak threshold (10 minutes by default) of practice English. Each row
// is a single (user, day) pair; the unique index prevents duplicates.
//
// We use Tashkent local-day boundaries - see services/streak_service.go.
type StreakDay struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_streak_user_day,priority:1" json:"user_id"`
	Day    time.Time `gorm:"type:date;not null;uniqueIndex:idx_streak_user_day,priority:2" json:"day"`
}

func (s *StreakDay) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}
