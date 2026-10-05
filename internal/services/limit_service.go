package services

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// DailyUsage holds the user's speaking usage inside the rolling 24h window.
//
// The base free-tier limit is `FreeDailyLimitMinutes` (default 20) per
// every 24-hour rolling window - not per calendar day. Users can earn a
// bonus (+5 min per referred friend, max 3 friends = +15 min) which
// inflates `minutes_limit` for exactly as long as the referred friends
// remain inside the rolling window.
//
// Example - 5 sessions × 4 min starting at 10:00, 11:00, 12:00, 13:00, 14:00:
//
//	at 14:00 → 20 used, 0 left, reset_at = 10:00 + 24h = tomorrow 10:00
//	at tomorrow 10:00 → first entry drops out, 16 used, 4 left, reset_at = 11:00+24h
type DailyUsage struct {
	MinutesUsed  int  `json:"minutes_used"`
	MinutesLimit *int `json:"minutes_limit"` // nil = unlimited (premium)
	MinutesLeft  *int `json:"minutes_left"`
	IsPremium    bool `json:"is_premium"`
	// Unix seconds when the next block of minutes comes back (i.e. when the
	// oldest entry in the window expires). Null when nothing is used yet or
	// the user is premium.
	ResetAt *int64 `json:"reset_at"`
	// Referral bonus minutes currently folded into `minutes_limit`.
	BonusMinutes int `json:"bonus_minutes"`
	// Minutes spoken inside today's Tashkent calendar day (used by the
	// home-screen streak progress bar). Distinct from `minutes_used`,
	// which spans the rolling 24h window.
	TodayMinutes int `json:"today_minutes"`
	// How many minutes the user needs to speak today to lock in a streak day.
	StreakThreshold int `json:"streak_threshold"`
}

// ReferralBonusFor returns how many minutes of bonus the user currently
// has from successful referrals inside the rolling 24 h window.
//
// Intentionally a package-level var so we can inject a stub during unit
// tests and so callers don't create an import cycle with the handlers
// package (the handler's CountActiveReferrals is wired in at startup).
var ReferralBonusFor func(userID string) int = func(userID string) int { return 0 }

const usageWindowSeconds = int64(24 * 60 * 60) // 24h rolling window

func usageKey(userID string) string {
	return "usage_24h:" + userID
}

// roomUsageKey is the parallel window for minutes spoken inside a private
// room (a teacher's class).
//
// Room minutes are deliberately kept OUT of `usage_24h` - that set is the
// billing meter for the free daily limit, and classroom speaking is free.
// But they must still count toward the streak and the home-screen daily
// progress bar, otherwise a student who spent an hour in class would see
// "0 minutes today" and lose their streak. Hence a second, non-billing
// window with the same shape and TTL.
func roomUsageKey(userID string) string {
	return "room_usage_24h:" + userID
}

// cleanAndFetch trims expired entries from the sorted set and returns the
// remaining (unexpired) entries as [score, member] pairs.
func cleanAndFetch(userID string) ([]redis.Z, error) {
	return cleanAndFetchKey(usageKey(userID))
}

// cleanAndFetchRoom is cleanAndFetch for the non-billing room window.
func cleanAndFetchRoom(userID string) ([]redis.Z, error) {
	return cleanAndFetchKey(roomUsageKey(userID))
}

func cleanAndFetchKey(key string) ([]redis.Z, error) {
	ctx := context.Background()

	now := time.Now().Unix()
	cutoff := now - usageWindowSeconds

	// Drop anything older than 24h.
	database.Redis.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", cutoff))

	// Fetch remaining with scores so we can compute reset_at.
	return database.Redis.ZRangeWithScores(ctx, key, 0, -1).Result()
}

// sumMinutes parses the minute count out of each member string and sums them.
// Member format: "<nanoseconds>:<minutes>".
func sumMinutes(entries []redis.Z) int {
	total := 0
	for _, z := range entries {
		member, ok := z.Member.(string)
		if !ok {
			continue
		}
		parts := strings.SplitN(member, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if m, err := strconv.Atoi(parts[1]); err == nil {
			total += m
		}
	}
	return total
}

// GetDailyUsage returns how many minutes the user has spoken within the
// rolling 24-hour window plus the timestamp at which the oldest entry
// will age out (reset_at).
func GetDailyUsage(user *models.User) (*DailyUsage, error) {
	if user.IsPremium {
		return &DailyUsage{
			MinutesUsed:     0,
			MinutesLimit:    nil,
			MinutesLeft:     nil,
			IsPremium:       true,
			ResetAt:         nil,
			TodayMinutes:    CountTodayTotalMinutes(user.ID.String()),
			StreakThreshold: StreakDailyMinThreshold,
		}, nil
	}

	entries, err := cleanAndFetch(user.ID.String())
	if err != nil && err != redis.Nil {
		// Soft-fail: pretend the user has the full quota.
	}

	minutesUsed := sumMinutes(entries)
	bonus := ReferralBonusFor(user.ID.String())
	limit := config.App.FreeDailyLimitMinutes + bonus
	minutesLeft := limit - minutesUsed
	if minutesLeft < 0 {
		minutesLeft = 0
	}

	var resetAt *int64
	if len(entries) > 0 {
		// Oldest entry → its score is the smallest. The sorted set returns
		// entries in ascending order, so the first one is oldest.
		oldest := int64(entries[0].Score)
		t := oldest + usageWindowSeconds
		resetAt = &t
	}

	return &DailyUsage{
		MinutesUsed:     minutesUsed,
		MinutesLimit:    &limit,
		MinutesLeft:     &minutesLeft,
		IsPremium:       false,
		ResetAt:         resetAt,
		BonusMinutes:    bonus,
		TodayMinutes:    CountTodayTotalMinutes(user.ID.String()),
		StreakThreshold: StreakDailyMinThreshold,
	}, nil
}

// CheckDailyLimit returns whether the user can start a new session + how
// many minutes they still have in the 24h window.
func CheckDailyLimit(userID uuid.UUID) (bool, int) {
	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		return false, 0
	}

	if user.IsPremium {
		return true, 999
	}

	usage, err := GetDailyUsage(&user)
	if err != nil || usage == nil {
		return false, 0
	}
	if usage.MinutesLeft == nil {
		return true, 999
	}
	return *usage.MinutesLeft > 0, *usage.MinutesLeft
}

// GetSessionLimitMinutes calculates how long a new session can last.
//
// Business rule: **a single premium user "carries" the call**. If
// at least one side is premium, the session is unlimited (9999 min)
// - the premium user paid for the privilege of speaking as long as
// they want, and it would feel unfair to cut them off because their
// partner happened to be on the free tier. The free user still
// "spends" their daily allowance for the time they were talking
// (IncrementUsage runs on session end as usual), so they will hit
// their cap on the NEXT match attempt - but the current call is
// allowed to finish naturally.
//
// Only when BOTH users are on the free tier do we compute the
// shared limit as the minimum of their remaining minutes (so
// neither side blows past their daily cap mid-call).
func GetSessionLimitMinutes(user1ID, user2ID uuid.UUID) int {
	var u1, u2 models.User
	if err := database.DB.First(&u1, "id = ?", user1ID).Error; err != nil {
		return config.App.FreeDailyLimitMinutes
	}
	if err := database.DB.First(&u2, "id = ?", user2ID).Error; err != nil {
		return config.App.FreeDailyLimitMinutes
	}

	// Premium "carries" the call - even one side is enough to
	// unlock the unlimited window. 9999 is the sentinel the rest
	// of the codebase already treats as "no limit" (see
	// session.IsPremiumSession in CreateSession).
	if u1.IsPremium || u2.IsPremium {
		return 9999
	}

	// Both free - pick the smaller of the two daily allowances so
	// neither user ends the session over their cap.
	limits := []int{}
	for _, u := range []models.User{u1, u2} {
		usage, _ := GetDailyUsage(&u)
		if usage == nil || usage.MinutesLeft == nil {
			continue
		}
		left := *usage.MinutesLeft
		if left < 1 {
			left = 1 // never offer a 0-minute session
		}
		limits = append(limits, left)
	}

	if len(limits) == 0 {
		return config.App.FreeDailyLimitMinutes
	}

	min := limits[0]
	for _, l := range limits[1:] {
		if l < min {
			min = l
		}
	}
	return min
}

// IncrementUsage records a new usage entry in the 24h sorted-set window.
// Each call appends a unique member so multiple calls within the same
// second don't collide. Entries older than 24h are expired lazily on read.
//
// When called from the session-end path, pass a non-empty sessionID so
// the function can deduplicate retries (WebSocket reconnect, network
// replay). The idempotency key lives 24h in Redis - the same window as
// the usage sorted set.
func IncrementUsage(userID string, minutes int) error {
	return IncrementUsageIdempotent(userID, minutes, "")
}

// IncrementUsageIdempotent is the session-aware variant. If sessionID
// is non-empty, a Redis SETNX guard prevents double-counting from
// WebSocket reconnects or duplicate session_end events.
func IncrementUsageIdempotent(userID string, minutes int, sessionID string) error {
	if minutes <= 0 {
		return nil
	}

	ctx := context.Background()

	// Idempotency guard: if we've already counted this session for this
	// user, silently return success.
	if sessionID != "" {
		idempotencyKey := fmt.Sprintf("usage_counted:%s:%s", sessionID, userID)
		acquired, err := database.Redis.SetNX(ctx, idempotencyKey, "1", 24*time.Hour).Result()
		if err == nil && !acquired {
			// Already counted - skip.
			return nil
		}
		// On Redis error we fall through and count (fail-open).
	}

	return appendUsageEntry(usageKey(userID), minutes)
}

// IncrementRoomUsage records classroom minutes in the non-billing window.
//
// Same idempotency guard as IncrementUsageIdempotent - a WebSocket
// reconnect that replays session_end must not double-count - but a
// separate key namespace so the two guards can't collide.
func IncrementRoomUsage(userID string, minutes int, sessionID string) error {
	if minutes <= 0 {
		return nil
	}

	ctx := context.Background()

	if sessionID != "" {
		idempotencyKey := fmt.Sprintf("room_usage_counted:%s:%s", sessionID, userID)
		acquired, err := database.Redis.SetNX(ctx, idempotencyKey, "1", 24*time.Hour).Result()
		if err == nil && !acquired {
			return nil
		}
	}

	return appendUsageEntry(roomUsageKey(userID), minutes)
}

// appendUsageEntry writes one "<nanos>:<minutes>" member into a rolling
// 24h sorted set. Shared by the billing and room windows.
func appendUsageEntry(key string, minutes int) error {
	ctx := context.Background()
	now := time.Now()

	// Member must be unique per write, otherwise ZADD silently dedupes.
	member := fmt.Sprintf("%d:%d", now.UnixNano(), minutes)

	database.Redis.ZAdd(ctx, key, redis.Z{
		Score:  float64(now.Unix()),
		Member: member,
	})

	// TTL slightly longer than the window so cleanup always has headroom.
	database.Redis.Expire(ctx, key, time.Duration(usageWindowSeconds+3600)*time.Second)

	return nil
}
