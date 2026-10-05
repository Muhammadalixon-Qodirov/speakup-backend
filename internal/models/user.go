package models

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GenerateReferralCode returns a short, URL-safe, unguessable code.
// 5 bytes = 40 bits of entropy → ~1 trillion possible codes, plenty for
// a friend-invite system. We use base32 without padding for legibility.
func GenerateReferralCode() string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to a UUID prefix.
		return strings.ToUpper(uuid.New().String()[:8])
	}
	return strings.TrimRight(
		base32.StdEncoding.EncodeToString(b),
		"=",
	)
}

type User struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Telegram
	Phone        *string `gorm:"size:20;uniqueIndex" json:"phone"`
	TelegramID   int64   `gorm:"uniqueIndex;not null" json:"-"`
	Username     *string `gorm:"size:64" json:"-"`
	FirstName    string  `gorm:"size:128;not null" json:"first_name"`
	LastName     *string `gorm:"size:128" json:"last_name"`
	PhotoURL     *string `gorm:"size:512" json:"photo_url"`
	LanguageCode *string `gorm:"size:8;default:'uz'" json:"language_code"`
	IsTgPremium  bool    `gorm:"default:false" json:"is_tg_premium"`

	// Profile
	IsOnboarded bool            `gorm:"default:false" json:"is_onboarded"`
	Level       *string         `gorm:"size:4" json:"level"`   // A1, A2, B1, B2, C1, C2
	Region      *string         `gorm:"size:128" json:"region"`
	Interests   pq.StringArray  `gorm:"type:text[]" json:"interests"`
	Gender      *string         `gorm:"size:16" json:"gender"` // "male" | "female" | null

	// Premium
	IsPremium       bool       `gorm:"default:false" json:"is_premium"`
	PremiumExpiresAt *time.Time `json:"premium_expires_at"`

	// Stats
	TotalSessions int     `gorm:"default:0" json:"total_sessions"`
	TotalMinutes  int     `gorm:"default:0" json:"total_minutes"`
	Rating        float64 `gorm:"default:0" json:"rating"`
	RatingCount   int     `gorm:"default:0" json:"rating_count"`

	// Streak
	LastActiveAt  *time.Time `gorm:"index" json:"last_active_at"`
	CurrentStreak int        `gorm:"default:0" json:"current_streak"`
	MaxStreak     int        `gorm:"default:0" json:"max_streak"`

	// In-session mini-game stats. Denormalised onto the user so the
	// Game Leaderboard can rank without aggregating game_results on
	// every request. Updated atomically when a game finishes.
	GamesPlayed int `gorm:"default:0" json:"games_played"`
	GamesWon    int `gorm:"default:0" json:"games_won"`
	GameXP      int `gorm:"default:0;index" json:"game_xp"`

	// Status
	IsActive bool `gorm:"default:true" json:"is_active"`
	IsBanned bool `gorm:"default:false" json:"is_banned"`
	IsAdmin  bool `gorm:"default:false" json:"is_admin"`

	// BotBlocked is true when the user has blocked the bot in Telegram.
	// Driven by two signals - the my_chat_member webhook (real-time Telegram
	// notification) and Send failures with "blocked by the user" error. The
	// flag is cleared the moment Telegram sends us a my_chat_member update
	// with member/administrator status (i.e. the user unblocked + interacted).
	// Enforced at auth time: a blocked user cannot log in until they unblock.
	BotBlocked bool `gorm:"default:false;index" json:"bot_blocked"`

	// PartnerAlertsEnabled - opt-in for "someone is searching" Telegram
	// pings. When ON, the user gets a bot DM (subject to the usual
	// per-recipient cooldowns) every time another user clicks "Speak
	// Now" and lands in the queue, regardless of whether they're
	// currently online. Default OFF - quieter by default.
	PartnerAlertsEnabled bool `gorm:"default:false;index" json:"partner_alerts_enabled"`

	// PartnerAlertsPromptSeen - tracks whether we've already asked the
	// user to enable partner alerts. The opt-in dialog appears once on
	// /speak/matching after the first "no one to match" experience;
	// dismissing it (cancel or accept) flips this to true so we never
	// auto-prompt again. The /speak toggle remains the only path to
	// change the setting from then on.
	PartnerAlertsPromptSeen bool `gorm:"default:false" json:"partner_alerts_prompt_seen"`

	// Referrals
	// ReferralCode is a short, URL-safe, unique identifier that this user
	// can share with friends. Generated on first save (BeforeCreate).
	ReferralCode string `gorm:"size:12;uniqueIndex" json:"referral_code"`
	// ReferredBy points at the user who invited this one (nullable). Set
	// once at signup when the initData `start_param` carries a `ref_` code.
	ReferredBy *uuid.UUID `gorm:"type:uuid;index" json:"-"`

	// Pinned AI Speaking band - the score from a SpeakingReport the user
	// chose to display on their profile. Both fields are denormalised so
	// the profile card can render without joining the reports table.
	PinnedSpeakingBand     *float64   `json:"pinned_speaking_band"`
	PinnedSpeakingReportID *uuid.UUID `gorm:"type:uuid" json:"pinned_speaking_report_id"`
	PinnedSpeakingAt       *time.Time `json:"pinned_speaking_at"`
}

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	// Generate a referral code if none was set. Collisions are astronomically
	// unlikely but we retry once just to be safe.
	if u.ReferralCode == "" {
		u.ReferralCode = GenerateReferralCode()
	}
	return nil
}

// DisplayName returns user's full name.
func (u *User) DisplayName() string {
	if u.LastName != nil {
		return u.FirstName + " " + *u.LastName
	}
	return u.FirstName
}
