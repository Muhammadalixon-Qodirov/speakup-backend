package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// roomMatchMu serialises room matching exactly like matchMu does for the
// public queue: without it, two students pressing Speak in the same
// millisecond can both claim the same third student.
//
// It is a SEPARATE mutex from matchMu on purpose - a busy public queue
// must never add latency to a classroom in session, and the two queues
// share no state.
var roomMatchMu sync.Mutex

const (
	// roomQueueTTL matches the public queue's 2 minutes. A student who
	// tapped Speak and then walked away ages out instead of poisoning
	// the pool for the rest of the lesson.
	roomQueueTTL = 120

	// recentPartnerMemory is how many previous partners we remember per
	// student per room. 3 is the sweet spot: in a 6-student class it
	// still leaves a legal choice, and in a 20-student class it makes
	// repeats effectively impossible.
	recentPartnerMemory = 3

	// recentPartnerWindow is how long that memory lasts. One lesson is
	// typically 60-90 minutes; 6 hours covers a full teaching day so a
	// student doesn't get the same partner in the morning and afternoon
	// class, while still resetting by the next day.
	recentPartnerWindow = 6 * time.Hour
)

func roomQueueKey(roomID string) string     { return "room_queue:" + roomID }
func roomQueueUserKey(userID string) string { return "room_queue_user:" + userID }
func roomRecentKey(roomID, userID string) string {
	return fmt.Sprintf("room_recent:%s:%s", roomID, userID)
}

// RoomQueueEntry is what we store in the room's Redis sorted set.
//
// Unlike the public QueueEntry there is no level/gender/interest snapshot:
// a room is a class the teacher assembled deliberately, so filtering
// inside it would only shrink an already-small pool and leave students
// staring at a spinner. Matching here is random-with-variety, nothing else.
type RoomQueueEntry struct {
	UserID    string  `json:"user_id"`
	RoomID    string  `json:"room_id"`
	Timestamp float64 `json:"timestamp"`
}

// AddToRoomQueue puts a student into their room's waiting pool.
//
// A user can only be in ONE queue at a time. Joining a room queue clears
// any public-queue membership (and vice versa - see handleJoinQueue),
// otherwise a student could be matched publicly mid-lesson.
func AddToRoomQueue(roomID, userID string) {
	ctx := context.Background()

	// Leaving the public queue first is what makes the two pools
	// mutually exclusive.
	RemoveFromQueue(userID)
	// And leave any other room's queue - a student may belong to
	// several rooms but can only wait in one.
	RemoveFromRoomQueue(userID)

	entry := RoomQueueEntry{
		UserID:    userID,
		RoomID:    roomID,
		Timestamp: float64(time.Now().Unix()),
	}
	data, _ := json.Marshal(entry)

	database.Redis.ZAdd(ctx, roomQueueKey(roomID), redis.Z{
		Score:  entry.Timestamp,
		Member: string(data),
	})

	// The marker stores WHICH room the user is queued in, so
	// RemoveFromRoomQueue can clean up without scanning every room.
	database.Redis.Set(ctx, roomQueueUserKey(userID), roomID,
		time.Duration(roomQueueTTL)*time.Second)
}

// CurrentRoomQueue returns the room ID the user is currently waiting in,
// or "" when they're not in any room queue.
func CurrentRoomQueue(userID string) string {
	ctx := context.Background()
	roomID, err := database.Redis.Get(ctx, roomQueueUserKey(userID)).Result()
	if err != nil {
		return ""
	}
	return roomID
}

// RemoveFromRoomQueue takes the user out of whichever room queue they're
// in. Safe to call unconditionally.
func RemoveFromRoomQueue(userID string) {
	ctx := context.Background()

	roomID := CurrentRoomQueue(userID)
	if roomID == "" {
		return
	}

	key := roomQueueKey(roomID)
	entries, _ := database.Redis.ZRange(ctx, key, 0, -1).Result()
	for _, raw := range entries {
		var e RoomQueueEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue
		}
		if e.UserID == userID {
			database.Redis.ZRem(ctx, key, raw)
			break
		}
	}
	database.Redis.Del(ctx, roomQueueUserKey(userID))
}

// RoomQueueMembers returns the user IDs currently waiting in a room,
// oldest first. Powers the teacher's live panel.
func RoomQueueMembers(roomID string) []string {
	ctx := context.Background()

	// Expire stale entries first so the panel never shows a ghost.
	cutoff := float64(time.Now().Unix() - roomQueueTTL)
	database.Redis.ZRemRangeByScore(ctx, roomQueueKey(roomID), "0", fmt.Sprintf("%f", cutoff))

	entries, err := database.Redis.ZRange(ctx, roomQueueKey(roomID), 0, -1).Result()
	if err != nil {
		return nil
	}

	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		var e RoomQueueEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue
		}
		// Only trust entries whose marker is still alive.
		if database.Redis.Exists(ctx, roomQueueUserKey(e.UserID)).Val() == 0 {
			database.Redis.ZRem(ctx, roomQueueKey(roomID), raw)
			continue
		}
		out = append(out, e.UserID)
	}
	return out
}

// RoomQueueSize is the count form of RoomQueueMembers.
func RoomQueueSize(roomID string) int {
	return len(RoomQueueMembers(roomID))
}

// ClearRoomQueue empties a room's waiting pool. Called when the owner
// freezes the room: without it, students who were already waiting would
// keep getting matched into a room that is supposed to be closed.
func ClearRoomQueue(roomID string) {
	ctx := context.Background()

	for _, uid := range RoomQueueMembers(roomID) {
		database.Redis.Del(ctx, roomQueueUserKey(uid))
	}
	database.Redis.Del(ctx, roomQueueKey(roomID))
}

// --- Recent-partner memory ---

// rememberRoomPair records that these two spoke, so the next match in
// this room prefers someone else. Stored per (room, user) as a capped
// list, newest first.
func rememberRoomPair(roomID, userA, userB string) {
	ctx := context.Background()
	for _, pair := range [][2]string{{userA, userB}, {userB, userA}} {
		key := roomRecentKey(roomID, pair[0])
		pipe := database.Redis.Pipeline()
		pipe.LPush(ctx, key, pair[1])
		pipe.LTrim(ctx, key, 0, recentPartnerMemory-1)
		pipe.Expire(ctx, key, recentPartnerWindow)
		pipe.Exec(ctx)
	}
}

// recentPartners returns the user's last partners in this room, newest
// first (index 0 = most recent).
func recentPartners(roomID, userID string) []string {
	ctx := context.Background()
	out, err := database.Redis.LRange(ctx, roomRecentKey(roomID, userID), 0, recentPartnerMemory-1).Result()
	if err != nil {
		return nil
	}
	return out
}

// repeatPenalty scores how undesirable it is to pair `candidate` with a
// student whose recent list is `recent`.
//
// The scale matters: the penalty for the MOST recent partner (1000) is an
// order of magnitude above the largest possible positive score (random
// jitter 100 + wait bonus 60), so a fresh partner ALWAYS beats a repeat.
// Within the repeats, the older the pairing the smaller the penalty, so
// when everyone in a tiny class has already spoken to everyone the system
// degrades gracefully into "whoever you spoke to longest ago" rather than
// refusing to match.
func repeatPenalty(recent []string, candidate string) float64 {
	for i, id := range recent {
		if id == candidate {
			return float64(1000 - i*200)
		}
	}
	return 0
}

// --- Matching ---

// FindRoomMatch picks a partner for `userID` inside `roomID`.
//
// Selection is random by design - in a classroom the point is that you
// don't know who you'll get - with two correctives layered on top:
//
//   - rand(0..100)          the randomness itself
//   - min(waited_s/2, 60)   anti-starvation: nobody sits forever
//   - repeatPenalty(...)    variety: never the same partner twice in a row
//
// Returns "" (and a nil error) when nobody is available.
//
// CRITICAL INVARIANT - identical to FindMatchFull: on success BOTH users
// are removed from the room queue while the lock is still held, so a
// concurrent call can never hand the same student to two partners.
func FindRoomMatch(roomID, userID string) (string, error) {
	roomMatchMu.Lock()
	defer roomMatchMu.Unlock()

	ctx := context.Background()
	key := roomQueueKey(roomID)

	// Drop entries older than the TTL.
	cutoff := float64(time.Now().Unix() - roomQueueTTL)
	database.Redis.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%f", cutoff))

	// Same belt-and-braces guard as the public queue: a student already
	// in a live call must never be handed a second one.
	if userHasActiveSession(userID) {
		RemoveFromRoomQueue(userID)
		log.Warn().
			Str("user_id", userID).
			Str("room_id", roomID).
			Msg("FindRoomMatch: user already has an active session")
		return "", nil
	}

	entries, err := database.Redis.ZRange(ctx, key, 0, -1).Result()
	if err != nil {
		return "", err
	}

	recent := recentPartners(roomID, userID)
	now := float64(time.Now().Unix())

	var best *RoomQueueEntry
	bestScore := -1e18

	for _, raw := range entries {
		var e RoomQueueEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue
		}
		if e.UserID == userID {
			continue
		}

		// Stale entry - the marker expired but the sorted-set row lived on.
		if database.Redis.Exists(ctx, roomQueueUserKey(e.UserID)).Val() == 0 {
			database.Redis.ZRem(ctx, key, raw)
			continue
		}

		// The candidate may have been pulled into a session by another
		// path since they queued.
		if userHasActiveSession(e.UserID) {
			database.Redis.ZRem(ctx, key, raw)
			database.Redis.Del(ctx, roomQueueUserKey(e.UserID))
			continue
		}

		waited := now - e.Timestamp
		if waited < 0 {
			waited = 0
		}
		waitBonus := waited / 2
		if waitBonus > 60 {
			waitBonus = 60
		}

		score := rand.Float64()*100 + waitBonus - repeatPenalty(recent, e.UserID)

		if best == nil || score > bestScore {
			bestScore = score
			cand := e
			best = &cand
		}
	}

	if best == nil {
		return "", nil
	}

	claimRoomQueueEntry(ctx, key, entries, best.UserID)
	claimRoomQueueEntry(ctx, key, entries, userID)
	rememberRoomPair(roomID, userID, best.UserID)

	return best.UserID, nil
}

// claimRoomQueueEntry removes one user's entry from the room queue. The
// caller must hold roomMatchMu - this is the "claim" half of the atomic
// match, and doing it outside the lock reintroduces the double-match race.
func claimRoomQueueEntry(ctx context.Context, key string, entries []string, uid string) {
	for _, raw := range entries {
		var e RoomQueueEntry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			continue
		}
		if e.UserID == uid {
			database.Redis.ZRem(ctx, key, raw)
			break
		}
	}
	database.Redis.Del(ctx, roomQueueUserKey(uid))
}

// PairAllInRoom empties the room queue in one shot and returns the pairs
// to create. This backs the teacher's "start round" button: instead of
// students trickling into matches, the whole class is paired at once.
//
// The algorithm is a shuffle followed by a greedy pass that skips recent
// partners where it can:
//
//  1. shuffle the waiting students (fair, unpredictable ordering)
//  2. for each unpaired student, take the first unpaired candidate they
//     have NOT recently spoken to
//  3. if every remaining candidate is a recent partner, take the one they
//     spoke to longest ago rather than leaving them out
//
// With an odd number of students one is left over; they are returned in
// `leftover` and put back in the queue by the caller so they match as
// soon as anyone else presses Speak.
func PairAllInRoom(roomID string) (pairs [][2]string, leftover []string) {
	roomMatchMu.Lock()
	defer roomMatchMu.Unlock()

	ctx := context.Background()
	key := roomQueueKey(roomID)

	waiting := RoomQueueMembers(roomID)

	// Drop anyone who slipped into a session between queueing and now.
	eligible := waiting[:0]
	for _, uid := range waiting {
		if userHasActiveSession(uid) {
			database.Redis.Del(ctx, roomQueueUserKey(uid))
			continue
		}
		eligible = append(eligible, uid)
	}

	if len(eligible) < 2 {
		return nil, append([]string(nil), eligible...)
	}

	order := append([]string(nil), eligible...)
	rand.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	pairs, leftover = pairGreedy(order, func(uid string) []string {
		return recentPartners(roomID, uid)
	})

	// Clear the queue for everyone we paired, and remember the pairings
	// so the NEXT round gives them someone new.
	entries, _ := database.Redis.ZRange(ctx, key, 0, -1).Result()
	for _, p := range pairs {
		claimRoomQueueEntry(ctx, key, entries, p[0])
		claimRoomQueueEntry(ctx, key, entries, p[1])
		rememberRoomPair(roomID, p[0], p[1])
	}

	return pairs, leftover
}

// pairGreedy is the pure pairing core of PairAllInRoom, split out so the
// interesting logic is testable without Redis.
//
// `order` is the already-shuffled list of waiting students; `recentOf`
// returns a student's recent partners (newest first). It walks the list
// once and, for each still-unpaired student, prefers the first unpaired
// candidate they have NOT recently spoken to. When every remaining
// candidate is a repeat it falls back to the lowest-penalty one - i.e.
// the partner they spoke to longest ago - rather than leaving the
// student unpaired, which matters in a small class where everyone has
// already spoken to everyone.
func pairGreedy(order []string, recentOf func(string) []string) (pairs [][2]string, leftover []string) {
	used := make(map[string]bool, len(order))

	for i, a := range order {
		if used[a] {
			continue
		}
		recent := recentOf(a)

		partner := ""
		fallback := ""
		fallbackPenalty := 1e18

		for _, b := range order[i+1:] {
			if used[b] {
				continue
			}
			p := repeatPenalty(recent, b)
			if p == 0 {
				partner = b
				break
			}
			if p < fallbackPenalty {
				fallbackPenalty = p
				fallback = b
			}
		}
		if partner == "" {
			partner = fallback
		}
		if partner == "" {
			continue // nobody left for this student
		}

		used[a] = true
		used[partner] = true
		pairs = append(pairs, [2]string{a, partner})
	}

	for _, uid := range order {
		if !used[uid] {
			leftover = append(leftover, uid)
		}
	}
	return pairs, leftover
}

// UserHasActiveSession is the exported form of the internal guard, for
// callers outside this package (the WS manual-pairing handler needs to
// refuse pairing someone who is already on a call).
func UserHasActiveSession(userID string) bool {
	return userHasActiveSession(userID)
}

// --- Room-scoped session creation ---

// CreateRoomSession is CreateSession for a room match: same double-booking
// guarantees, but the row is stamped with the room and given the room's
// own time limit instead of the caller's daily remainder.
func CreateRoomSession(user1ID, user2ID string, roomID uuid.UUID, limitMinutes int) (*models.Session, error) {
	session, err := CreateSession(user1ID, user2ID, limitMinutes)
	if err != nil {
		return nil, err
	}

	// Stamp the room on the row. Done as a follow-up update rather than
	// threading a parameter through CreateSession so the public-queue
	// path stays byte-for-byte unchanged.
	if err := database.DB.Model(&models.Session{}).
		Where("id = ?", session.ID).
		Update("room_id", roomID).Error; err != nil {
		log.Error().Err(err).
			Str("session_id", session.ID.String()).
			Msg("CreateRoomSession: failed to stamp room_id")
	}
	session.RoomID = &roomID

	return session, nil
}

// ActiveRoomSpeakers returns the user IDs currently inside an active
// session that belongs to this room. Powers the "speaking now" column of
// the teacher panel.
func ActiveRoomSpeakers(roomID uuid.UUID) []string {
	var sessions []models.Session
	database.DB.
		Select("user1_id", "user2_id").
		Where("room_id = ? AND status = ?", roomID, "active").
		Find(&sessions)

	out := make([]string, 0, len(sessions)*2)
	for _, s := range sessions {
		out = append(out, s.User1ID.String(), s.User2ID.String())
	}
	return out
}

// RoomSessionLimitMinutes is how long a single room call may run.
//
// Room speaking is deliberately NOT charged against the free daily limit
// (that's the whole pitch to learning centres), so there is no per-user
// remainder to compute here. We still return a finite ceiling rather than
// "unlimited" so a forgotten open tab can't hold a slot all day - the
// frontend shows it as the call timer.
const RoomSessionLimitMinutes = 120
