package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// WeeklyLeaderboardAward is one +10-minute bonus granted to a top-3
// finisher of the Monday-to-Sunday (Tashkent) weekly leaderboard.
//
// The cron job at Monday 00:05 Tashkent (Sun 19:05 UTC) computes last
// week's ranking and writes one of these rows per winner. The bonus
// stays "active" for the next 7 days - SumWeeklyLeaderboardBonus sums
// every award with created_at >= now - 7d and folds it into the user's
// daily-minute allowance (same mechanism as prize boxes).
//
// Unique index on (user_id, week_start) prevents double-awarding when
// the cron fires twice on the same week - e.g. a deploy at the exact
// minute the schedule triggers, or a manual admin retrigger.
type WeeklyLeaderboardAward struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	UserID    uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_weekly_award_user_week" json:"user_id"`
	WeekStart time.Time `gorm:"type:date;not null;uniqueIndex:idx_weekly_award_user_week" json:"week_start"`
	Rank      int       `gorm:"not null" json:"rank"`    // 1, 2 or 3
	Minutes   int       `gorm:"not null" json:"minutes"` // bonus size, currently fixed at 10
	Notified  bool      `gorm:"default:false" json:"notified"`

	User User `gorm:"foreignKey:UserID" json:"-"`
}

// Index hint for the unique constraint so AutoMigrate emits it.
func (WeeklyLeaderboardAward) TableName() string {
	return "weekly_leaderboard_awards"
}

func (a *WeeklyLeaderboardAward) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}
