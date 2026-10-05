package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

var (
	ErrSchedulePremium   = errors.New("speaking rejalashtirish premium foydalanuvchilar uchun")
	ErrScheduleNotFriend = errors.New("faqat do'stlaringiz bilan rejalashtira olasiz")
	ErrScheduleTooSoon   = errors.New("kamida 15 daqiqa keyingi vaqtni tanlang")
	ErrScheduleTooFar    = errors.New("30 kundan uzoqqa rejalashtirib bo'lmaydi")
	ErrScheduleClash     = errors.New("shu vaqt atrofida boshqa rejangiz bor")
	ErrScheduleLimit     = errors.New("bir vaqtda 5 tadan ortiq reja bo'la olmaydi")
	ErrScheduleNotFound  = errors.New("reja topilmadi")
	ErrScheduleNotLive   = errors.New("hozir bu rejaga qo'shilib bo'lmaydi")
)

const (
	// scheduleMinLead keeps "schedule" meaningfully different from "call
	// me now" - anything sooner is what the Speak button is for.
	scheduleMinLead = 15 * time.Minute
	// scheduleMaxLead - a month out is already optimistic for a language
	// exchange; beyond that the partner has usually forgotten.
	scheduleMaxLead = 30 * 24 * time.Hour
	// scheduleClashWindow stops two appointments being booked so close
	// together that the first one is still running when the second opens.
	scheduleClashWindow = 30 * time.Minute
	// scheduleMaxActive per organiser, so one enthusiastic user cannot
	// fill a friend's calendar.
	scheduleMaxActive = 5
)

// HasActivePremium is the single definition of "may schedule".
//
// The is_premium flag alone is not enough: a lapsed subscription keeps
// the flag until the nightly expiry cron runs, and during that window a
// user could book appointments they are no longer paying for.
func HasActivePremium(u *models.User) bool {
	if u == nil || !u.IsPremium {
		return false
	}
	if u.PremiumExpiresAt == nil {
		return true
	}
	return u.PremiumExpiresAt.After(time.Now())
}

// CreateScheduledSession books an appointment. The organiser must be
// premium and the two must already be friends.
func CreateScheduledSession(organiser *models.User, partnerID uuid.UUID, at time.Time, note string) (*models.ScheduledSession, error) {
	if !HasActivePremium(organiser) {
		return nil, ErrSchedulePremium
	}
	if organiser.ID == partnerID {
		return nil, ErrFriendSelf
	}
	if !AreFriends(organiser.ID, partnerID) {
		return nil, ErrScheduleNotFriend
	}

	now := time.Now()
	if at.Before(now.Add(scheduleMinLead)) {
		return nil, ErrScheduleTooSoon
	}
	if at.After(now.Add(scheduleMaxLead)) {
		return nil, ErrScheduleTooFar
	}

	var active int64
	database.DB.Model(&models.ScheduledSession{}).
		Where("user1_id = ? AND status IN ? AND scheduled_at > ?",
			organiser.ID,
			[]string{models.ScheduledPending, models.ScheduledConfirmed},
			now).
		Count(&active)
	if active >= scheduleMaxActive {
		return nil, ErrScheduleLimit
	}

	// A clash on EITHER side is a clash: the invitee's calendar matters
	// just as much as the organiser's, and they never get to see it.
	var clash int64
	database.DB.Model(&models.ScheduledSession{}).
		Where("status IN ? AND scheduled_at BETWEEN ? AND ? AND (user1_id IN ? OR user2_id IN ?)",
			[]string{models.ScheduledPending, models.ScheduledConfirmed},
			at.Add(-scheduleClashWindow), at.Add(scheduleClashWindow),
			[]uuid.UUID{organiser.ID, partnerID},
			[]uuid.UUID{organiser.ID, partnerID}).
		Count(&clash)
	if clash > 0 {
		return nil, ErrScheduleClash
	}

	s := models.ScheduledSession{
		User1ID:     organiser.ID,
		User2ID:     partnerID,
		ScheduledAt: at,
		Status:      models.ScheduledPending,
		Note:        note,
	}
	if err := database.DB.Create(&s).Error; err != nil {
		return nil, err
	}

	return LoadScheduledSession(s.ID)
}

// LoadScheduledSession fetches one appointment with both profiles.
func LoadScheduledSession(id uuid.UUID) (*models.ScheduledSession, error) {
	var s models.ScheduledSession
	if err := database.DB.
		Preload("User1").Preload("User2").
		First(&s, "id = ?", id).Error; err != nil {
		return nil, ErrScheduleNotFound
	}
	return &s, nil
}

// ListScheduledFor returns a user's appointments that still matter:
// everything upcoming plus anything inside its join window right now.
func ListScheduledFor(userID uuid.UUID) ([]models.ScheduledSession, error) {
	var out []models.ScheduledSession
	cutoff := time.Now().Add(-models.ScheduledJoinWindowMinutes * time.Minute)
	err := database.DB.
		Preload("User1").Preload("User2").
		Where("(user1_id = ? OR user2_id = ?) AND status IN ? AND scheduled_at > ?",
			userID, userID,
			[]string{models.ScheduledPending, models.ScheduledConfirmed},
			cutoff).
		Order("scheduled_at ASC").
		Find(&out).Error
	return out, err
}

// ListScheduledHistory returns finished appointments - started, missed,
// cancelled - so a user can see what happened to what they booked.
func ListScheduledHistory(userID uuid.UUID, limit int) ([]models.ScheduledSession, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	var out []models.ScheduledSession
	err := database.DB.
		Preload("User1").Preload("User2").
		Where("(user1_id = ? OR user2_id = ?) AND (status IN ? OR scheduled_at <= ?)",
			userID, userID,
			[]string{models.ScheduledStarted, models.ScheduledMissed,
				models.ScheduledCancelled, models.ScheduledDeclined},
			time.Now().Add(-models.ScheduledJoinWindowMinutes*time.Minute)).
		Order("scheduled_at DESC").
		Limit(limit).
		Find(&out).Error
	return out, err
}

// ConfirmScheduled records the invitee's "I'll be there".
//
// Confirmation is a courtesy, not a gate: an unanswered appointment
// still opens at its time. Treating silence as a refusal would break
// the exact case the feature is sold on - the partner who forgot to
// open the app until the bot pinged them.
func ConfirmScheduled(id, userID uuid.UUID) (*models.ScheduledSession, error) {
	res := database.DB.Model(&models.ScheduledSession{}).
		Where("id = ? AND user2_id = ? AND status = ?", id, userID, models.ScheduledPending).
		Updates(map[string]interface{}{
			"status":       models.ScheduledConfirmed,
			"confirmed_at": time.Now(),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrScheduleNotFound
	}
	return LoadScheduledSession(id)
}

// DeclineScheduled is the invitee saying no in advance, which is far
// better for the organiser than silence followed by an empty room.
func DeclineScheduled(id, userID uuid.UUID) (*models.ScheduledSession, error) {
	res := database.DB.Model(&models.ScheduledSession{}).
		Where("id = ? AND user2_id = ? AND status IN ?",
			id, userID, []string{models.ScheduledPending, models.ScheduledConfirmed}).
		Updates(map[string]interface{}{
			"status":          models.ScheduledDeclined,
			"cancelled_by_id": userID,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrScheduleNotFound
	}
	return LoadScheduledSession(id)
}

// CancelScheduled lets either side call the appointment off.
func CancelScheduled(id, userID uuid.UUID) (*models.ScheduledSession, error) {
	res := database.DB.Model(&models.ScheduledSession{}).
		Where("id = ? AND (user1_id = ? OR user2_id = ?) AND status IN ?",
			id, userID, userID,
			[]string{models.ScheduledPending, models.ScheduledConfirmed}).
		Updates(map[string]interface{}{
			"status":          models.ScheduledCancelled,
			"cancelled_by_id": userID,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrScheduleNotFound
	}
	return LoadScheduledSession(id)
}

// MarkScheduledStarted attaches the real session to the appointment.
func MarkScheduledStarted(id uuid.UUID, sessionID uuid.UUID) {
	database.DB.Model(&models.ScheduledSession{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":     models.ScheduledStarted,
			"session_id": sessionID,
		})
}

// --- Join window presence ---
//
// Both sides have to be standing at the door before it opens. Presence
// lives in Redis with a TTL rather than in Postgres because it is
// worthless a minute later, and because a crashed process must not leave
// a user permanently "waiting".

func scheduledPresenceKey(id uuid.UUID, userID string) string {
	return fmt.Sprintf("sched:present:%s:%s", id.String(), userID)
}

// MarkScheduledPresence records that a user is waiting in the join
// window and reports whether the other side is already there.
func MarkScheduledPresence(id uuid.UUID, userID, partnerID string) bool {
	ctx := context.Background()
	ttl := time.Duration(models.ScheduledJoinWindowMinutes) * time.Minute

	database.Redis.Set(ctx, scheduledPresenceKey(id, userID), "1", ttl)

	n, err := database.Redis.Exists(ctx, scheduledPresenceKey(id, partnerID)).Result()
	if err != nil {
		return false
	}
	return n > 0
}

// ClaimScheduledPairing hands exactly one caller the right to create the
// session for this appointment.
//
// Two people can press Join in the same millisecond, and both would then
// see the other as present. Without this claim both would call
// CreateSession; one would win the advisory lock and the other would get
// "already in a session" back - surfacing as an error toast at the exact
// moment the call is starting. SETNX makes the race have a winner.
//
// The key outlives the join window so a retry inside the same
// appointment cannot double-create.
func ClaimScheduledPairing(id uuid.UUID) bool {
	ok, err := database.Redis.SetNX(
		context.Background(),
		fmt.Sprintf("sched:pairing:%s", id.String()),
		"1",
		time.Duration(models.ScheduledJoinWindowMinutes+5)*time.Minute,
	).Result()
	if err != nil {
		// Redis being down must not block a real appointment; fall back
		// to letting the caller through and rely on CreateSession's
		// advisory lock as the second line of defence.
		return true
	}
	return ok
}

// ClearScheduledPresence removes a user from the doorway - they backed
// out, or the pairing already happened.
func ClearScheduledPresence(id uuid.UUID, userIDs ...string) {
	ctx := context.Background()
	for _, uid := range userIDs {
		database.Redis.Del(ctx, scheduledPresenceKey(id, uid))
	}
}

// IsScheduledPresent reports whether a user is currently waiting.
func IsScheduledPresent(id uuid.UUID, userID string) bool {
	n, err := database.Redis.Exists(context.Background(), scheduledPresenceKey(id, userID)).Result()
	return err == nil && n > 0
}

// --- Sweeps, driven by the once-a-minute cron ---

// DueForReminder returns appointments whose 15-minute nudge is owed.
func DueForReminder(now time.Time) []models.ScheduledSession {
	var out []models.ScheduledSession
	database.DB.
		Preload("User1").Preload("User2").
		Where("status IN ? AND reminded_at IS NULL AND scheduled_at > ? AND scheduled_at <= ?",
			[]string{models.ScheduledPending, models.ScheduledConfirmed},
			now, now.Add(models.ScheduledReminderLead)).
		Find(&out)
	return out
}

// DueForStart returns appointments whose time has just arrived.
func DueForStart(now time.Time) []models.ScheduledSession {
	var out []models.ScheduledSession
	database.DB.
		Preload("User1").Preload("User2").
		Where("status IN ? AND start_notified_at IS NULL AND scheduled_at <= ? AND scheduled_at > ?",
			[]string{models.ScheduledPending, models.ScheduledConfirmed},
			now, now.Add(-models.ScheduledJoinWindowMinutes*time.Minute)).
		Find(&out)
	return out
}

// ExpireStaleScheduled closes the window on appointments nobody walked
// into, so the card disappears from both screens instead of lingering as
// a button that no longer works.
func ExpireStaleScheduled(now time.Time) []models.ScheduledSession {
	var stale []models.ScheduledSession
	database.DB.
		Preload("User1").Preload("User2").
		Where("status IN ? AND scheduled_at <= ?",
			[]string{models.ScheduledPending, models.ScheduledConfirmed},
			now.Add(-models.ScheduledJoinWindowMinutes*time.Minute)).
		Find(&stale)

	if len(stale) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(stale))
	for i := range stale {
		ids = append(ids, stale[i].ID)
	}
	database.DB.Model(&models.ScheduledSession{}).
		Where("id IN ?", ids).
		Update("status", models.ScheduledMissed)

	return stale
}

// MarkReminded / MarkStartNotified are the idempotence stamps for the
// two notification passes.
func MarkReminded(id uuid.UUID) {
	now := time.Now()
	database.DB.Model(&models.ScheduledSession{}).
		Where("id = ? AND reminded_at IS NULL", id).
		Update("reminded_at", now)
}

func MarkStartNotified(id uuid.UUID) {
	now := time.Now()
	database.DB.Model(&models.ScheduledSession{}).
		Where("id = ? AND start_notified_at IS NULL", id).
		Update("start_notified_at", now)
}
