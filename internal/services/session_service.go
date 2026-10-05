package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
	"gorm.io/gorm"
)

// CreateSession creates a new speaking session between two users.
//
// Hard guarantee: if EITHER user already has an `active` session in
// the DB, this function refuses to create a second one and returns
// `ErrUserAlreadyInSession`. This is the last line of defence
// against the "one user in two sessions" race: the matching service
// is supposed to prevent that at the queue level, but a DB-level
// check makes it impossible even if the queue check is bypassed.
//
// The check + insert run inside a single transaction with a
// serializable isolation guarantee via pg_advisory_xact_lock on
// sorted user IDs - two concurrent CreateSession calls involving
// the same user will serialise on the advisory lock and the second
// one will see the row from the first and bail out.
func CreateSession(user1ID, user2ID string, limitMinutes int) (*models.Session, error) {
	uid1, err := uuid.Parse(user1ID)
	if err != nil {
		return nil, err
	}
	uid2, err := uuid.Parse(user2ID)
	if err != nil {
		return nil, err
	}

	var session models.Session
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// Advisory lock keyed on the sorted pair so the same (A,B)
		// pair always maps to the same lock slot regardless of
		// caller. Two concurrent sessions involving user A always
		// contend on A's slot at least.
		for _, id := range sortedLockKeys(uid1, uid2) {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", id).Error; err != nil {
				return err
			}
		}

		// Refuse if either side is already in a live session.
		var existingCount int64
		if err := tx.Model(&models.Session{}).
			Where("(user1_id IN ? OR user2_id IN ?) AND status = ?",
				[]uuid.UUID{uid1, uid2}, []uuid.UUID{uid1, uid2}, "active").
			Count(&existingCount).Error; err != nil {
			return err
		}
		if existingCount > 0 {
			return ErrUserAlreadyInSession
		}

		now := time.Now()
		topic := utils.GetRandomTopic()
		session = models.Session{
			User1ID:          uid1,
			User2ID:          uid2,
			Topic:            &topic,
			Status:           "active",
			StartedAt:        &now,
			LimitMinutes:     limitMinutes,
			IsPremiumSession: limitMinutes >= 9999,
		}
		return tx.Create(&session).Error
	})
	if txErr != nil {
		return nil, txErr
	}

	// Cache session in Redis for fast signaling lookups. Done
	// outside the transaction because Redis doesn't need to be
	// rolled back if the commit failed (the row wouldn't exist in
	// the first place).
	topic := ""
	if session.Topic != nil {
		topic = *session.Topic
	}
	sessionData, _ := json.Marshal(map[string]string{
		"user1_id": user1ID,
		"user2_id": user2ID,
		"topic":    topic,
	})
	ctx := context.Background()
	database.Redis.Set(ctx, "session:"+session.ID.String(), string(sessionData), 24*time.Hour)

	return &session, nil
}

// sortedLockKeys deterministically hashes a pair of user UUIDs into
// a stable pair of int64 slot IDs for pg_advisory_xact_lock. We
// sort first so (A,B) and (B,A) produce the same pair.
func sortedLockKeys(a, b uuid.UUID) []int64 {
	// UUID has a Compare-friendly big-endian byte layout; compare
	// bytes directly. Convert each UUID to an int64 by XOR-folding
	// its 16 bytes down to 8 - good enough for collision
	// avoidance at our scale (hundreds to thousands of users), and
	// avoids pulling in a hash/big-int dep.
	fold := func(u uuid.UUID) int64 {
		var out int64
		for i := 0; i < 16; i++ {
			out ^= int64(u[i]) << ((i % 8) * 8)
		}
		return out
	}
	x := fold(a)
	y := fold(b)
	if x == y {
		// Extremely unlikely but harmless - use different slots.
		y++
	}
	if x > y {
		x, y = y, x
	}
	return []int64{x, y}
}

// ErrUserAlreadyInSession is returned by CreateSession when one of
// the requested users is already bound to an `active` Session row.
// Callers (the WS match layer) should surface this to the user as
// a silent retry - the matching service's guards normally prevent
// the race from reaching this point, but a crashed client + stale
// DB state can still trigger it.
var ErrUserAlreadyInSession = errors.New("user already in an active session")

// EndSession ends an active session, calculates duration, updates user stats.
// Uses atomic UPDATE to prevent double-ending from both users simultaneously
// AND requires the caller to actually be a participant - otherwise anyone
// who guessed a session UUID could end someone else's call.
func EndSession(sessionID string, userID string) (*models.Session, error) {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, err
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, err
	}

	// Atomic: only end if still active AND the caller is one of the
	// two participants. Combined into a single UPDATE so an attacker
	// can't race the auth check.
	now := time.Now()
	result := database.DB.Model(&models.Session{}).
		Where("id = ? AND status = ? AND (user1_id = ? OR user2_id = ?)",
			sid, "active", uid, uid).
		Updates(map[string]interface{}{
			"status":   "ended",
			"ended_at": now,
		})

	if result.RowsAffected == 0 {
		// Either already ended, not a participant, or session doesn't exist.
		// All three are safe to treat the same way - return nil so the WS
		// handler doesn't notify anyone.
		return nil, nil
	}

	// Cleanup Redis session keys eagerly. The 24h TTL would expire them
	// anyway, but immediate removal keeps memory lean and avoids serving
	// stale data to any code that checks the cache.
	go func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		database.Redis.Del(cleanCtx, "session:"+sessionID)
	}()

	// Reload session
	var session models.Session
	if err := database.DB.First(&session, "id = ?", sid).Error; err != nil {
		return nil, err
	}

	// Calculate duration
	if session.StartedAt != nil {
		duration := now.Sub(*session.StartedAt)
		session.DurationSeconds = int(duration.Seconds())
		database.DB.Model(&session).Update("duration_seconds", session.DurationSeconds)
	}

	// Update both users' stats atomically with SQL expressions.
	// Note: streak recompute happens in the WS handler AFTER usage
	// has been incremented in Redis, otherwise this session's minutes
	// wouldn't be visible to the streak calculator.
	for _, uid := range []uuid.UUID{session.User1ID, session.User2ID} {
		database.DB.Exec(
			"UPDATE users SET total_sessions = total_sessions + 1, total_minutes = total_minutes + ? WHERE id = ?",
			session.DurationMinutes(), uid,
		)
	}

	return &session, nil
}

// GetSession gets a session by ID (only if user is a participant).
func GetSession(sessionID string, userID string) (*models.Session, error) {
	sid, _ := uuid.Parse(sessionID)
	uid, _ := uuid.Parse(userID)

	var session models.Session
	err := database.DB.
		Preload("User1").
		Preload("User2").
		Where("id = ? AND (user1_id = ? OR user2_id = ?)", sid, uid, uid).
		First(&session).Error

	if err != nil {
		return nil, err
	}
	return &session, nil
}

// RateSession saves a rating for a session partner.
// Uses atomic SQL to prevent race conditions on concurrent ratings.
func RateSession(sessionID string, raterID string, rating int, feedback *string) error {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return err
	}
	raterUUID, err := uuid.Parse(raterID)
	if err != nil {
		return err
	}

	// Get session to find ratee
	var session models.Session
	if err := database.DB.First(&session, "id = ?", sid).Error; err != nil {
		return err
	}

	// Determine who is being rated
	rateeID := session.User2ID
	if session.User1ID != raterUUID {
		rateeID = session.User1ID
	}

	// Check if already rated
	var existing models.SessionRating
	if err := database.DB.Where("session_id = ? AND rater_id = ?", sid, raterUUID).First(&existing).Error; err == nil {
		return fmt.Errorf("already rated this session")
	}

	// Create rating
	sessionRating := &models.SessionRating{
		SessionID: sid,
		RaterID:   raterUUID,
		RateeID:   rateeID,
		Rating:    rating,
		Feedback:  feedback,
	}
	if err := database.DB.Create(sessionRating).Error; err != nil {
		return err
	}

	// Atomic rating update. Postgres row-locks the user row for the
	// duration of the UPDATE so concurrent rate calls serialize. The
	// CAST + CASE makes the first-rating edge case explicit and
	// promotes the math to NUMERIC so we don't lose precision once
	// rating_count grows large.
	if err := database.DB.Exec(`
		UPDATE users
		SET
			rating = CASE
				WHEN rating_count = 0 THEN ?::numeric
				ELSE (rating::numeric * rating_count + ?::numeric) / (rating_count + 1)
			END,
			rating_count = rating_count + 1
		WHERE id = ?
	`, rating, rating, rateeID).Error; err != nil {
		return err
	}

	return nil
}
