package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
)

// Free-tier limits for the read-aloud Pronunciation trainer, and what premium
// unlocks. The gate exists for a concrete reason: analysis runs on the
// sidecar's 2 CPU cores, which it shares with live voice rooms (docs/TALAFFUZ.md).
// Capping free usage keeps those cores free; premium pays for the headroom.
//
// Two levers, both bypassed by premium (and admins):
//   - a rolling-24h attempt count (PRON_FREE_DAILY_ATTEMPTS)
//   - the longest passage that may be requested (PRON_FREE_MAX_SENTENCES)
//
// The attempt window reuses the same lazy-expiring sorted-set machinery as the
// speaking-minutes meter (limit_service.go), so a reset_at falls out for free.

const pronAttemptHardMaxSentences = 4

func pronAttemptKey(userID string) string {
	return "pron_attempts_24h:" + userID
}

// PronMaxSentencesFor is the longest passage this user may request. Premium
// gets the full range; free users are capped by config (clamped to 1..4 so a
// bad env value can never unlock more than the model supports or less than one).
func PronMaxSentencesFor(isPremium bool) int {
	if isPremium {
		return pronAttemptHardMaxSentences
	}
	n := config.App.PronFreeMaxSentences
	if n < 1 {
		n = 1
	}
	if n > pronAttemptHardMaxSentences {
		n = pronAttemptHardMaxSentences
	}
	return n
}

// PronUsage is the snapshot the frontend uses to render the picker: how many
// checks are left today, how long a passage this tier may request, and whether
// the user is premium. Limit/Remaining are nil for unlimited (premium, admin,
// or a zero/negative configured cap).
type PronUsage struct {
	Used         int    `json:"used"`
	Limit        *int   `json:"limit"`
	Remaining    *int   `json:"remaining"`
	IsPremium    bool   `json:"is_premium"`
	ResetAt      *int64 `json:"reset_at"`
	MaxSentences int    `json:"max_sentences"`
}

// unlimitedPronUsage is the snapshot for anyone who bypasses the cap.
func unlimitedPronUsage(isPremium bool) *PronUsage {
	return &PronUsage{
		Used:         0,
		Limit:        nil,
		Remaining:    nil,
		IsPremium:    isPremium,
		ResetAt:      nil,
		MaxSentences: pronAttemptHardMaxSentences,
	}
}

// GetPronunciationUsage returns the caller's rolling-24h attempt snapshot.
// Premium (and a non-positive configured cap) means unlimited.
func GetPronunciationUsage(userID uuid.UUID, isPremium bool) *PronUsage {
	limit := config.App.PronFreeDailyAttempts
	if isPremium || limit <= 0 {
		return unlimitedPronUsage(isPremium)
	}

	entries, err := cleanAndFetchKey(pronAttemptKey(userID.String()))
	if err != nil && err != redis.Nil {
		// Soft-fail open: a Redis hiccup must not lock a paying-or-free user
		// out of practising. Report the full quota as available.
		return &PronUsage{
			Used:         0,
			Limit:        &limit,
			Remaining:    &limit,
			IsPremium:    false,
			MaxSentences: PronMaxSentencesFor(false),
		}
	}

	used := len(entries)
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}

	var resetAt *int64
	if used > 0 {
		// Entries come back oldest-first; the oldest aging out is when the
		// next attempt frees up.
		t := int64(entries[0].Score) + usageWindowSeconds
		resetAt = &t
	}

	return &PronUsage{
		Used:         used,
		Limit:        &limit,
		Remaining:    &remaining,
		IsPremium:    false,
		ResetAt:      resetAt,
		MaxSentences: PronMaxSentencesFor(false),
	}
}

// PronunciationAttemptAllowed reports whether the user still has a check left in
// the current window. Premium and admins are always allowed.
func PronunciationAttemptAllowed(userID uuid.UUID, isPremium bool) bool {
	u := GetPronunciationUsage(userID, isPremium)
	if u.Remaining == nil {
		return true
	}
	return *u.Remaining > 0
}

// RecordPronunciationAttempt appends one attempt to the rolling-24h window.
// Called only after a successful analysis, so a failed check never costs the
// user a slot. Unlimited tiers skip the write entirely.
func RecordPronunciationAttempt(userID uuid.UUID, isPremium bool) {
	if isPremium || config.App.PronFreeDailyAttempts <= 0 {
		return
	}

	ctx := context.Background()
	now := time.Now()
	key := pronAttemptKey(userID.String())

	database.Redis.ZAdd(ctx, key, redis.Z{
		Score:  float64(now.Unix()),
		Member: fmt.Sprintf("%d", now.UnixNano()),
	})
	database.Redis.Expire(ctx, key, time.Duration(usageWindowSeconds+3600)*time.Second)
}
