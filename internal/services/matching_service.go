package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// matchMu prevents two concurrent FindMatch calls from selecting the same user.
var matchMu sync.Mutex

const (
	queueKey = "match_queue"
	queueTTL = 120 // 2 minutes max in queue
)

// GenderPref is what the user wants from the other side.
//
//	"same"  - strict: must be the same gender as me
//	"mixed" - relaxed: I don't care
//	""      - treated as "same" (the safe default)
type GenderPref string

const (
	GenderPrefSame  GenderPref = "same"
	GenderPrefMixed GenderPref = "mixed"
)

// QueueEntry is what we store in the Redis sorted set.
type QueueEntry struct {
	UserID     string   `json:"user_id"`
	Level      string   `json:"level"`       // "basic" | "independent" | "proficient" | ""
	Gender     string   `json:"gender"`      // "male" | "female" | ""
	GenderPref string   `json:"gender_pref"` // "same" | "mixed"
	Interests  []string `json:"interests"`
	Timestamp  float64  `json:"timestamp"`
}

// AddToQueue adds a user to the matching queue.
// All filter/preference dimensions are snapshotted into the entry so
// `FindMatch` can score without a DB lookup per candidate.
func AddToQueue(
	userID string,
	level string,
	gender string,
	genderPref string,
	interests []string,
) {
	ctx := context.Background()

	// Default to "same" - stricter is safer.
	pref := genderPref
	if pref != string(GenderPrefSame) && pref != string(GenderPrefMixed) {
		pref = string(GenderPrefSame)
	}

	entry := QueueEntry{
		UserID:     userID,
		Level:      level,
		Gender:     gender,
		GenderPref: pref,
		Interests:  interests,
		Timestamp:  float64(time.Now().Unix()),
	}
	data, _ := json.Marshal(entry)

	// Sorted set - score = timestamp for FIFO ordering
	database.Redis.ZAdd(ctx, queueKey, redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: string(data),
	})

	// Mark user as in queue (TTL = 2 min)
	database.Redis.Set(
		ctx,
		"queue_user:"+userID,
		"1",
		time.Duration(queueTTL)*time.Second,
	)
}

// QueueSize returns how many users are currently waiting in the match
// queue. Surfaced by /users/online-stats so the home page can show a
// live "X looking for partner" hint.
func QueueSize() int {
	ctx := context.Background()
	n, err := database.Redis.ZCard(ctx, queueKey).Result()
	if err != nil {
		return 0
	}
	return int(n)
}

// RemoveFromQueue removes a user from the matching queue.
func RemoveFromQueue(userID string) {
	ctx := context.Background()

	entries, _ := database.Redis.ZRange(ctx, queueKey, 0, -1).Result()
	for _, entry := range entries {
		var qe QueueEntry
		if err := json.Unmarshal([]byte(entry), &qe); err != nil {
			continue
		}
		if qe.UserID == userID {
			database.Redis.ZRem(ctx, queueKey, entry)
			break
		}
	}

	database.Redis.Del(ctx, "queue_user:"+userID)
}

// sameLevelGroup returns true if the two users belong to the SAME
// specific level group (e.g. both "independent"). Used as a SCORING
// preference, not a hard gate - we'd rather match someone with a
// different level than leave them waiting in an empty queue.
//
// "" (any) on either side is not a same-group match.
func sameLevelGroup(myLevel, otherLevel string) bool {
	if myLevel == "" || otherLevel == "" {
		return false
	}
	return myLevel == otherLevel
}

// canMatchGender enforces gender preference. If EITHER side requests "same"
// the genders must match. Otherwise it's free.
func canMatchGender(myGender, myPref, otherGender, otherPref string) bool {
	if myPref == "" {
		myPref = string(GenderPrefSame)
	}
	if otherPref == "" {
		otherPref = string(GenderPrefSame)
	}
	// Nobody cares → always OK.
	if myPref == string(GenderPrefMixed) && otherPref == string(GenderPrefMixed) {
		return true
	}
	// Someone insists on same-gender.
	if myGender == "" || otherGender == "" {
		// We don't know one of them → can't satisfy "same" constraint.
		return false
	}
	return myGender == otherGender
}

// interestOverlap counts how many interests the two sets share (case-
// insensitive intersection size). This is the interest similarity score.
func interestOverlap(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(a))
	for _, x := range a {
		set[x] = struct{}{}
	}
	n := 0
	for _, y := range b {
		if _, ok := set[y]; ok {
			n++
		}
	}
	return n
}

// FindMatch finds the best match for a user in the queue.
//
// Strict gate (candidate skipped if it fails):
//  1. Gender preference compatibility (see canMatchGender)
//
// Soft preference (no candidate is rejected on this alone):
//   - Level group: huge +100 bonus when both sides match the same
//     specific group, so same-level pairs win whenever possible -
//     but a different-level candidate is still better than no match.
//
// Ranking score:
//   - 100  same specific level group on both sides
//   - 30   one side specified, other is "any"
//   - 5    both "any"
//   - (interest_overlap * 3)
//   - timestamp * 0.0001            FIFO tiebreaker
func FindMatch(userID string, level string) (string, error) {
	return FindMatchFull(userID, level, "", "", nil)
}

// FindMatchFull is the full-featured version that takes the current user's
// gender, their gender preference and interests. It's what the WS layer
// actually calls when a user joins the queue.
//
// CRITICAL INVARIANT: on a successful match, BOTH users are removed from
// the queue (Redis sorted set + the queue_user:<id> marker) BEFORE the
// lock is released. If the removal were deferred to the caller, a second
// concurrent FindMatchFull could see the same candidate as still-queued
// and return them as its own match too - which is exactly the
// "one user in two sessions" bug that was happening in production.
func FindMatchFull(
	userID string,
	level string,
	myGender string,
	myGenderPref string,
	myInterests []string,
) (string, error) {
	matchMu.Lock()
	defer matchMu.Unlock()

	ctx := context.Background()

	// Clean expired entries
	expiredCutoff := float64(time.Now().Unix() - queueTTL)
	database.Redis.ZRemRangeByScore(ctx, queueKey, "0", fmt.Sprintf("%f", expiredCutoff))

	// Hard guard: refuse to match a user who's already in an active
	// session. This is a belt-and-braces check on top of the
	// RemoveFromQueueAtomic below - the user should never be in the
	// queue in the first place if they already have a session, but
	// a client bug or a crashed partner could leave the marker
	// behind.
	if userHasActiveSession(userID) {
		RemoveFromQueue(userID)
		log.Warn().
			Str("user_id", userID).
			Msg("FindMatch: user already has an active session, removing from queue")
		return "", nil
	}

	entries, err := database.Redis.ZRange(ctx, queueKey, 0, -1).Result()
	if err != nil {
		return "", err
	}

	log.Debug().
		Str("user_id", userID).
		Str("level", level).
		Str("gender", myGender).
		Str("gender_pref", myGenderPref).
		Int("queue_size", len(entries)).
		Msg("FindMatch: scanning queue")

	var bestMatch *QueueEntry
	bestScore := -1e18

	for _, entry := range entries {
		var qe QueueEntry
		if err := json.Unmarshal([]byte(entry), &qe); err != nil {
			continue
		}

		if qe.UserID == userID {
			continue
		}

		exists, _ := database.Redis.Exists(ctx, "queue_user:"+qe.UserID).Result()
		if exists == 0 {
			continue
		}

		// Secondary guard: skip candidates who somehow ended up in
		// the queue while in an active session (e.g. crash recovery).
		if userHasActiveSession(qe.UserID) {
			// Queue entry is stale - clean it up so the next scan
			// doesn't waste time on it.
			database.Redis.ZRem(ctx, queueKey, entry)
			database.Redis.Del(ctx, "queue_user:"+qe.UserID)
			continue
		}

		// Hard gate: gender preference (bi-directional). Level is
		// intentionally NOT a hard gate - see soft scoring below.
		if !canMatchGender(myGender, myGenderPref, qe.Gender, qe.GenderPref) {
			continue
		}

		// Score - same-level wins big, different-level still allowed
		// so users don't sit waiting in an empty same-level pool.
		score := 0.0
		switch {
		case sameLevelGroup(level, qe.Level):
			score += 100
		case level == "" && qe.Level == "":
			score += 5
		case level == "" || qe.Level == "":
			score += 30
		default:
			// Both sides specified but different groups - fallback match.
			score += 10
		}

		// Interests: each overlap adds weight. Capped at 10 to avoid a
		// single interest-heavy user dominating when level doesn't match.
		overlap := interestOverlap(myInterests, qe.Interests)
		if overlap > 10 {
			overlap = 10
		}
		score += float64(overlap) * 3

		// FIFO tiebreaker
		score -= qe.Timestamp * 0.0001

		if bestMatch == nil || score > bestScore {
			bestScore = score
			entry := qe
			bestMatch = &entry
		}
	}

	if bestMatch == nil {
		return "", nil
	}

	// ATOMIC CLAIM: while we still hold matchMu, remove BOTH users
	// from the Redis queue + mark their queue_user TTL so no
	// concurrent FindMatchFull can see them. Finding the raw entry
	// string for ZRem is a linear scan but len(entries) is small in
	// practice (<50) and we're already walking it.
	claimEntry := func(uid string) {
		// Find the raw JSON for uid and remove it.
		for _, e := range entries {
			var qe QueueEntry
			if err := json.Unmarshal([]byte(e), &qe); err != nil {
				continue
			}
			if qe.UserID == uid {
				database.Redis.ZRem(ctx, queueKey, e)
				break
			}
		}
		database.Redis.Del(ctx, "queue_user:"+uid)
	}
	claimEntry(bestMatch.UserID)
	claimEntry(userID)

	return bestMatch.UserID, nil
}

// userHasActiveSession reports whether the user is already bound to
// an `active` Session row. Cheap single-row SELECT with an index hit
// on (user1_id, status) / (user2_id, status).
func userHasActiveSession(userID string) bool {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return false
	}
	var count int64
	database.DB.Model(&models.Session{}).
		Where("(user1_id = ? OR user2_id = ?) AND status = ?", uid, uid, "active").
		Count(&count)
	return count > 0
}

// GetPartnerID returns the partner's userID for a given session, but ONLY
// if the caller is a real participant. Otherwise it returns an error -
// this is the authorization gate that stops a malicious user from
// signalling against someone else's session by guessing a session UUID.
//
// Without this check, the previous version would happily return user1's
// ID to any caller, which the WS relay then used to forward fake
// offers/answers/ICE to an unrelated user mid-call.
func GetPartnerID(sessionID string, myUserID string) (string, error) {
	ctx := context.Background()

	data, err := database.Redis.Get(ctx, "session:"+sessionID).Result()
	if err != nil {
		return "", err
	}

	var sessionData struct {
		User1ID string `json:"user1_id"`
		User2ID string `json:"user2_id"`
	}
	if err := json.Unmarshal([]byte(data), &sessionData); err != nil {
		return "", err
	}

	switch myUserID {
	case sessionData.User1ID:
		return sessionData.User2ID, nil
	case sessionData.User2ID:
		return sessionData.User1ID, nil
	default:
		return "", fmt.Errorf("not a participant of session %s", sessionID)
	}
}

// --- Compatibility shims for the older signature ---

// Deprecated: prefer AddToQueue with full args.
func AddToQueueSimple(userID string, level string) {
	AddToQueue(userID, level, "", "", nil)
}

var _ = uuid.Nil // keep uuid import used when callers are trimmed
var _ models.User
