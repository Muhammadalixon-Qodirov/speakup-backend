package services

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// ChannelBonusChannel is the Telegram channel users subscribe to in
// exchange for the one-shot bonus. Stored without the leading "@".
const ChannelBonusChannel = "speakup_app"

// ChannelBonusMinutes is the permanent +N daily minute bonus granted
// on successful verification. One-time per user.
const ChannelBonusMinutes = 10

// ChannelBonusStatus is the snapshot the frontend reads for the card UI.
type ChannelBonusStatus struct {
	Claimed   bool       `json:"claimed"`
	Channel   string     `json:"channel"`
	Minutes   int        `json:"minutes"`
	GrantedAt *time.Time `json:"granted_at,omitempty"`
}

// Error sentinels the handler maps to specific HTTP codes + error codes.
var (
	ErrChannelBonusAlreadyClaimed = errors.New("channel bonus already claimed")
	ErrNotSubscribed              = errors.New("user is not subscribed to the channel")
	ErrChannelCheckFailed         = errors.New("channel membership check failed")
)

// GetChannelBonusStatus returns whether the user has claimed the bonus.
func GetChannelBonusStatus(userID uuid.UUID) *ChannelBonusStatus {
	var b models.ChannelSubscriptionBonus
	err := database.DB.Where("user_id = ?", userID).First(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &ChannelBonusStatus{
			Claimed: false,
			Channel: ChannelBonusChannel,
			Minutes: ChannelBonusMinutes,
		}
	}
	return &ChannelBonusStatus{
		Claimed:   true,
		Channel:   ChannelBonusChannel,
		Minutes:   b.Minutes,
		GrantedAt: &b.GrantedAt,
	}
}

// ClaimChannelBonus verifies subscription and grants the bonus once.
// Idempotent at the DB level via the unique (user_id) index; the Go
// pre-check + ON CONFLICT DO NOTHING together guarantee no duplicates
// even under concurrent calls.
func ClaimChannelBonus(user *models.User) (*ChannelBonusStatus, error) {
	// Cheap pre-check so we don't hit Telegram needlessly.
	var existing models.ChannelSubscriptionBonus
	if err := database.DB.Where("user_id = ?", user.ID).First(&existing).Error; err == nil {
		return nil, ErrChannelBonusAlreadyClaimed
	}

	// Verify subscription via Telegram. Bot must be an admin of the
	// channel for this API call to succeed.
	isMember, err := bot.IsChannelMember(user.TelegramID, ChannelBonusChannel)
	if err != nil {
		return nil, ErrChannelCheckFailed
	}
	if !isMember {
		return nil, ErrNotSubscribed
	}

	bonus := models.ChannelSubscriptionBonus{
		UserID:    user.ID,
		Channel:   ChannelBonusChannel,
		Minutes:   ChannelBonusMinutes,
		GrantedAt: time.Now(),
	}
	res := database.DB.
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&bonus)
	if res.Error != nil {
		return nil, res.Error
	}
	// RowsAffected == 0 means the unique (user_id) index blocked this
	// insert: another request already claimed it (a concurrent race that
	// slipped past the pre-check). Report it as already-claimed so the
	// bonus is NEVER granted twice - the DB index is the hard guarantee,
	// this just surfaces it cleanly to the caller.
	if res.RowsAffected == 0 {
		return nil, ErrChannelBonusAlreadyClaimed
	}

	return &ChannelBonusStatus{
		Claimed:   true,
		Channel:   ChannelBonusChannel,
		Minutes:   ChannelBonusMinutes,
		GrantedAt: &bonus.GrantedAt,
	}, nil
}

// SumChannelBonusMinutes returns the PERMANENT +N daily-minute bonus this
// user earned by subscribing to the channel. It's a one-time claim (the
// unique user_id index guarantees it's granted at most once - see
// ClaimChannelBonus) but, once granted, it boosts the daily limit
// FOREVER: every day the user's cap is base + N. The bonus minutes
// themselves still "burn" as normal daily usage - they just never expire
// as an entitlement. There is no time window: the row's existence alone
// means the bonus is active.
func SumChannelBonusMinutes(userID string) int {
	var count int64
	database.DB.Model(&models.ChannelSubscriptionBonus{}).
		Where("user_id = ?", userID).
		Count(&count)
	if count == 0 {
		return 0
	}
	return ChannelBonusMinutes
}
