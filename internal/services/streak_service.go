package services

import (
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm/clause"
)

// StreakDailyMinThreshold is how many minutes a user needs to speak in a
// single calendar day for that day to count toward the streak.
//
// Must stay <= the free daily limit (FREE_DAILY_LIMIT_MINUTES), otherwise
// a free user can never reach it and the streak becomes unwinnable.
const StreakDailyMinThreshold = 10

// tashkentLoc is the canonical "today" timezone for streak boundaries.
var tashkentLoc = time.FixedZone("UZT", 5*60*60)

// startOfTashkentDay returns the local Tashkent midnight for the given time.
func startOfTashkentDay(t time.Time) time.Time {
	t = t.In(tashkentLoc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, tashkentLoc)
}

// RecordStreakIfQualified is called from the session-end path. If the user's
// total speaking time for *today* (Tashkent calendar day) has crossed the
// streak threshold, we mark today as a streak day and recompute their
// running streak counter.
func RecordStreakIfQualified(userID string, todayMinutes int) {
	if todayMinutes < StreakDailyMinThreshold {
		return
	}

	uid, err := uuid.Parse(userID)
	if err != nil {
		return
	}

	today := startOfTashkentDay(time.Now())

	// Atomic INSERT ... ON CONFLICT DO NOTHING. The (user_id, day) unique
	// index makes this truly idempotent even under concurrent calls from
	// a WebSocket reconnect or partner-side streak trigger.
	day := models.StreakDay{
		UserID: uid,
		Day:    today,
	}
	res := database.DB.
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "day"}},
			DoNothing: true,
		}).
		Create(&day)
	if res.Error != nil {
		log.Warn().Err(res.Error).Msg("RecordStreakIfQualified: insert failed")
		return
	}

	// Only recompute if a new row was actually inserted.
	if res.RowsAffected > 0 {
		recomputeStreak(uid)
	}
}

// recomputeStreak walks backwards from today, counting consecutive days
// the user has at least one StreakDay row. Grace rule: if today hasn't
// been qualified yet but yesterday was, start the walk from yesterday -
// users shouldn't lose their streak the moment Tashkent midnight ticks
// over, only after they've missed a full calendar day. The walk stops
// at the first gap. Updates current_streak + max_streak.
func recomputeStreak(userID uuid.UUID) {
	// Pull the user's recent streak days into a set (last 60 days is plenty).
	cutoff := startOfTashkentDay(time.Now()).AddDate(0, 0, -60)

	var days []models.StreakDay
	if err := database.DB.
		Where("user_id = ? AND day >= ?", userID, cutoff).
		Order("day DESC").
		Find(&days).Error; err != nil {
		return
	}

	dayset := make(map[string]bool, len(days))
	for _, d := range days {
		dayset[d.Day.In(tashkentLoc).Format("2006-01-02")] = true
	}

	today := startOfTashkentDay(time.Now())
	yesterday := today.AddDate(0, 0, -1)

	// Pick the walk starting point. Today wins if qualified; otherwise
	// yesterday (grace period). If neither is in the dayset, the streak
	// is broken - force 0 without walking.
	var cursor time.Time
	switch {
	case dayset[today.Format("2006-01-02")]:
		cursor = today
	case dayset[yesterday.Format("2006-01-02")]:
		cursor = yesterday
	default:
		cursor = time.Time{} // zero - will short-circuit below
	}

	streak := 0
	if !cursor.IsZero() {
		// Walk back counting consecutive days. Hard cap iterations as a
		// safety valve - 60 is the real ceiling, but a corrupted dayset
		// would otherwise loop forever.
		for i := 0; i < 1000; i++ {
			key := cursor.Format("2006-01-02")
			if !dayset[key] {
				break
			}
			streak++
			cursor = cursor.AddDate(0, 0, -1)
		}
		if streak >= 1000 {
			log.Warn().
				Str("user_id", userID.String()).
				Int("streak", streak).
				Msg("streak walk hit safety cap")
		}
	}

	updates := map[string]interface{}{
		"current_streak": streak,
	}
	// Bump max_streak if needed.
	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err == nil {
		if streak > user.MaxStreak {
			updates["max_streak"] = streak
		}
	}
	database.DB.Model(&models.User{}).
		Where("id = ?", userID).
		Updates(updates)
}

// RecomputeAllStreaks walks every user's streak_days and refreshes their
// stored current_streak. Safe to call on a schedule - idempotent and
// cheap enough for a few thousand users (one short query per user, all
// indexed). Called daily from the cron so broken streaks actually flip
// to 0 without waiting for the user to qualify again.
func RecomputeAllStreaks() (int, error) {
	var userIDs []uuid.UUID
	if err := database.DB.
		Model(&models.User{}).
		Where("is_active = ? AND is_banned = ?", true, false).
		Pluck("id", &userIDs).Error; err != nil {
		return 0, err
	}
	for _, uid := range userIDs {
		recomputeStreak(uid)
	}
	return len(userIDs), nil
}

// GetStreakCalendar returns the set of streak-day dates inside the given
// month (1-12) of the given year, formatted as "YYYY-MM-DD". Used by the
// home calendar widget.
func GetStreakCalendar(userID string, year int, month int) ([]string, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, err
	}

	first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, tashkentLoc)
	last := first.AddDate(0, 1, 0)

	var rows []models.StreakDay
	err = database.DB.
		Where("user_id = ? AND day >= ? AND day < ?", uid, first, last).
		Order("day ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Day.In(tashkentLoc).Format("2006-01-02"))
	}
	return out, nil
}

// CountTodayMinutes returns how many BILLED minutes the user has spoken
// inside the current Tashkent calendar day - i.e. public-queue sessions
// that consume the free daily limit. Pulled from the rolling-window data
// in Redis with a `since` filter.
//
// For anything user-facing ("have I practised today?", streak progress)
// use CountTodayTotalMinutes instead, which also includes classroom time.
func CountTodayMinutes(userID string) int {
	entries, err := cleanAndFetch(userID)
	if err != nil {
		return 0
	}
	return sumTodayEntries(entries)
}

// CountTodayRoomMinutes is CountTodayMinutes for private-room (classroom)
// speaking, which is free and therefore tracked in a separate window.
func CountTodayRoomMinutes(userID string) int {
	entries, err := cleanAndFetchRoom(userID)
	if err != nil {
		return 0
	}
	return sumTodayEntries(entries)
}

// CountTodayTotalMinutes is everything the user spoke today, billed or
// not. This is the number that decides the streak and drives the home
// progress bar: a student who spent an hour in their teacher's room has
// unquestionably practised, and must not see "0 minutes today".
func CountTodayTotalMinutes(userID string) int {
	return CountTodayMinutes(userID) + CountTodayRoomMinutes(userID)
}

// sumTodayEntries adds up the minutes of every entry that falls inside
// the current Tashkent calendar day.
func sumTodayEntries(entries []redis.Z) int {
	startOfDay := startOfTashkentDay(time.Now()).Unix()

	total := 0
	for _, z := range entries {
		if int64(z.Score) < startOfDay {
			continue
		}
		member, ok := z.Member.(string)
		if !ok {
			continue
		}
		// member format: "<nano>:<minutes>"
		var nano int64
		var minutes int
		if _, err := fmtScan(member, &nano, &minutes); err == nil {
			total += minutes
		}
	}
	return total
}

// fmtScan is a tiny helper to parse "%d:%d" without importing fmt.
func fmtScan(s string, a *int64, b *int) (int, error) {
	colon := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return 0, errInvalidFormat
	}
	left := s[:colon]
	right := s[colon+1:]
	var ai int64
	for i := 0; i < len(left); i++ {
		c := left[i]
		if c < '0' || c > '9' {
			return 0, errInvalidFormat
		}
		ai = ai*10 + int64(c-'0')
	}
	var bi int
	for i := 0; i < len(right); i++ {
		c := right[i]
		if c < '0' || c > '9' {
			return 0, errInvalidFormat
		}
		bi = bi*10 + int(c-'0')
	}
	*a = ai
	*b = bi
	return 2, nil
}

var errInvalidFormat = &simpleErr{"invalid format"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
