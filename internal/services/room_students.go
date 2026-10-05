package services

import (
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// The teacher's view of their students.
//
// Everything here is scoped to ONE room and readable only by that room's
// owner - a teacher sees the people who joined THEIR link and nobody
// else. The student's public-queue sessions are counted (they are the
// student's own practice and the whole point of tracking progress), but
// no other room's data ever leaks in.
//
// The students know: joining a teacher's room is joining a class, and the
// room screen states that the teacher sees their activity. Same principle
// as the listen-in indicator - nothing is observed silently.

// RoomDailyPoint is one bar of the room's attendance chart.
type RoomDailyPoint struct {
	// Date is a Tashkent calendar day, "2026-08-01".
	Date string `json:"date"`
	// ActiveStudents is how many DISTINCT members spoke that day. This is
	// the attendance number - 40 sessions from two students is not a
	// class that showed up.
	ActiveStudents int `json:"active_students"`
	Sessions       int `json:"sessions"`
	Minutes        int `json:"minutes"`
}

// RoomDailyAttendance returns a day-by-day series for the last `days`
// days, including days with zero activity.
//
// Empty days are filled in on purpose: a chart that silently skips them
// draws a flat line of attendance where there were actually gaps, which
// is precisely the thing a centre is paying to be able to see.
func RoomDailyAttendance(roomID uuid.UUID, days int) ([]RoomDailyPoint, error) {
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}

	type row struct {
		Day      time.Time
		Students int
		Sessions int
		Minutes  int
	}

	since := startOfTashkentDay(time.Now()).AddDate(0, 0, -(days - 1))

	var rows []row
	err := database.DB.Raw(`
		SELECT DATE(created_at AT TIME ZONE 'Asia/Tashkent')       AS day,
		       COUNT(DISTINCT user_id)                             AS students,
		       COUNT(DISTINCT session_id)                          AS sessions,
		       -- Two casts matter here. The UNION lists each session
		       -- twice (once per speaker), so raw seconds are doubled -
		       -- hence /120 rather than /60. And SUM() over a bigint
		       -- column returns NUMERIC in Postgres, which arrives in Go
		       -- as 81.6166... and will not scan into an int; ::bigint
		       -- forces integer division. The rest of the codebase casts
		       -- the same way (see the weekly leaderboard query).
		       COALESCE(SUM(duration_seconds)::bigint / 120, 0)    AS minutes
		FROM (
			SELECT id AS session_id, user1_id AS user_id, duration_seconds, created_at
			FROM sessions
			WHERE room_id = ? AND status = 'ended'
			  AND created_at >= ? AND deleted_at IS NULL
			UNION ALL
			SELECT id AS session_id, user2_id AS user_id, duration_seconds, created_at
			FROM sessions
			WHERE room_id = ? AND status = 'ended'
			  AND created_at >= ? AND deleted_at IS NULL
		) combined
		GROUP BY 1
	`, roomID, since, roomID, since).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	byDay := make(map[string]row, len(rows))
	for _, r := range rows {
		byDay[r.Day.Format("2006-01-02")] = r
	}

	out := make([]RoomDailyPoint, 0, days)
	for i := 0; i < days; i++ {
		d := since.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		r := byDay[key]
		out = append(out, RoomDailyPoint{
			Date:           key,
			ActiveStudents: r.Students,
			Sessions:       r.Sessions,
			Minutes:        r.Minutes,
		})
	}
	return out, nil
}

// StudentAIResult is one AI speaking attempt, as the teacher sees it.
type StudentAIResult struct {
	// Kind is "practice" (a single AI speaking check) or "mock" (the
	// full 3-part IELTS test). A teacher reads these very differently.
	Kind          string    `json:"kind"`
	ReportID      uuid.UUID `json:"report_id"`
	Topic         string    `json:"topic"`
	OverallBand   float64   `json:"overall_band"`
	Fluency       float64   `json:"fluency"`
	Lexical       float64   `json:"lexical"`
	Grammar       float64   `json:"grammar"`
	Pronunciation float64   `json:"pronunciation"`
	CreatedAt     time.Time `json:"created_at"`
}

// StudentActivity is the full picture of one student inside one room.
type StudentActivity struct {
	UserID   uuid.UUID `json:"user_id"`
	Name     string    `json:"name"`
	PhotoURL *string   `json:"photo_url"`
	Level    *string   `json:"level"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`

	// Speaking inside THIS room.
	RoomSessions int        `json:"room_sessions"`
	RoomMinutes  int        `json:"room_minutes"`
	LastSpokeAt  *time.Time `json:"last_spoke_at"`

	// Today, so the teacher can answer "who turned up for this lesson".
	TodaySessions int `json:"today_sessions"`
	TodayMinutes  int `json:"today_minutes"`

	// Platform-wide effort. Included because a centre's real question is
	// "is this student practising at all", and a student who speaks every
	// night in the public queue is doing exactly that.
	TotalSessions int `json:"total_sessions"`
	TotalMinutes  int `json:"total_minutes"`
	CurrentStreak int `json:"current_streak"`
	MaxStreak     int `json:"max_streak"`

	// AI results.
	BestBand   *float64          `json:"best_band"`
	LatestBand *float64          `json:"latest_band"`
	AIResults  []StudentAIResult `json:"ai_results"`
}

// RoomStudentActivity assembles one student's card for the teacher.
//
// Membership is verified by the caller; this function assumes the student
// belongs to the room.
func RoomStudentActivity(roomID, userID uuid.UUID, aiLimit int) (*StudentActivity, error) {
	if aiLimit <= 0 || aiLimit > 50 {
		aiLimit = 20
	}

	member, err := GetMembership(roomID, userID)
	if err != nil {
		return nil, err
	}

	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}

	act := StudentActivity{
		UserID:        user.ID,
		Name:          user.DisplayName(),
		PhotoURL:      user.PhotoURL,
		Level:         user.Level,
		Role:          member.Role,
		JoinedAt:      member.JoinedAt,
		RoomSessions:  member.TotalSessions,
		RoomMinutes:   member.TotalMinutes,
		LastSpokeAt:   member.LastSpokeAt,
		TotalSessions: user.TotalSessions,
		TotalMinutes:  user.TotalMinutes,
		CurrentStreak: user.CurrentStreak,
		MaxStreak:     user.MaxStreak,
	}

	act.TodaySessions, act.TodayMinutes = studentTodayInRoom(roomID, userID)
	act.AIResults = studentAIResults(userID, aiLimit)

	for i, r := range act.AIResults {
		if i == 0 {
			b := r.OverallBand
			act.LatestBand = &b
		}
		if act.BestBand == nil || r.OverallBand > *act.BestBand {
			b := r.OverallBand
			act.BestBand = &b
		}
	}

	return &act, nil
}

// studentTodayInRoom counts one student's activity in this room during
// the current Tashkent calendar day.
func studentTodayInRoom(roomID, userID uuid.UUID) (sessions, minutes int) {
	start := startOfTashkentDay(time.Now())

	var row struct {
		Sessions int
		Minutes  int
	}
	database.DB.Raw(`
		SELECT COUNT(*) AS sessions,
		       -- ::bigint - SUM() over bigint yields NUMERIC, which
		       -- cannot scan into a Go int.
		       COALESCE(SUM(duration_seconds)::bigint / 60, 0) AS minutes
		FROM sessions
		WHERE room_id = ? AND status = 'ended' AND deleted_at IS NULL
		  AND created_at >= ?
		  AND (user1_id = ? OR user2_id = ?)
	`, roomID, start, userID, userID).Scan(&row)

	return row.Sessions, row.Minutes
}

// studentAIResults merges the two AI report tables into one timeline,
// newest first.
//
// Transcripts are deliberately NOT included. A teacher needs the scores
// and the criteria breakdown to teach from; the verbatim text of what a
// student said is theirs to share, and putting it in a list view would
// make it something a teacher browses rather than something a student
// hands over.
func studentAIResults(userID uuid.UUID, limit int) []StudentAIResult {
	out := make([]StudentAIResult, 0, limit)

	var simple []models.SpeakingReport
	database.DB.
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&simple)
	for _, r := range simple {
		out = append(out, StudentAIResult{
			Kind:          "practice",
			ReportID:      r.ID,
			Topic:         r.Topic,
			OverallBand:   r.OverallBand,
			Fluency:       r.FluencyScore,
			Lexical:       r.LexicalScore,
			Grammar:       r.GrammarScore,
			Pronunciation: r.PronunciationScore,
			CreatedAt:     r.CreatedAt,
		})
	}

	var full []models.FullTestReport
	database.DB.
		Where("user_id = ? AND overall_band > 0", userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&full)
	for _, r := range full {
		// The full test scores each part separately; the teacher wants
		// one number per criterion, so average the three parts.
		out = append(out, StudentAIResult{
			Kind:          "mock",
			ReportID:      r.ID,
			Topic:         r.TopicID,
			OverallBand:   r.OverallBand,
			Fluency:       avg3(r.Part1Fluency, r.Part2Fluency, r.Part3Fluency),
			Lexical:       avg3(r.Part1Lexical, r.Part2Lexical, r.Part3Lexical),
			Grammar:       avg3(r.Part1Grammar, r.Part2Grammar, r.Part3Grammar),
			Pronunciation: avg3(r.Part1Pronunciation, r.Part2Pronunciation, r.Part3Pronunciation),
			CreatedAt:     r.CreatedAt,
		})
	}

	// Merge the two sources into one chronological timeline.
	for i := 1; i < len(out); i++ {
		item := out[i]
		j := i - 1
		for j >= 0 && out[j].CreatedAt.Before(item.CreatedAt) {
			out[j+1] = out[j]
			j--
		}
		out[j+1] = item
	}

	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// avg3 averages the parts that actually have a score. A test abandoned
// after Part 1 must not be dragged down by two zeroes.
func avg3(a, b, c float64) float64 {
	sum, n := 0.0, 0
	for _, v := range []float64{a, b, c} {
		if v > 0 {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// RoomStudentSummary is one row of the teacher's student table - the
// same shape as the report, plus AI and today's attendance.
type RoomStudentSummary struct {
	UserID   uuid.UUID `json:"user_id"`
	Name     string    `json:"name"`
	PhotoURL *string   `json:"photo_url"`
	Level    *string   `json:"level"`
	Role     string    `json:"role"`

	RoomSessions int        `json:"room_sessions"`
	RoomMinutes  int        `json:"room_minutes"`
	LastSpokeAt  *time.Time `json:"last_spoke_at"`

	TodaySessions int `json:"today_sessions"`
	TodayMinutes  int `json:"today_minutes"`

	CurrentStreak int `json:"current_streak"`

	AITests    int      `json:"ai_tests"`
	LatestBand *float64 `json:"latest_band"`
	BestBand   *float64 `json:"best_band"`
}

// RoomStudentsOverview is the teacher's main table: every member with
// today's attendance, their in-room totals and their AI band.
//
// Members with no activity are included with zeroes - "who did NOT
// practise" is the teacher's first question and a list that omits them
// cannot answer it.
func RoomStudentsOverview(roomID uuid.UUID) ([]RoomStudentSummary, error) {
	var members []models.RoomMember
	if err := database.DB.
		Preload("User").
		Where("room_id = ?", roomID).
		Order("role = 'owner' DESC, total_minutes DESC, joined_at ASC").
		Find(&members).Error; err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, nil
	}

	userIDs := make([]uuid.UUID, 0, len(members))
	for _, m := range members {
		userIDs = append(userIDs, m.UserID)
	}

	todaySessions, todayMinutes := roomTodayByUser(roomID, userIDs)
	aiCount, aiLatest, aiBest := aiSummaryByUser(userIDs)

	out := make([]RoomStudentSummary, 0, len(members))
	for _, m := range members {
		row := RoomStudentSummary{
			UserID:        m.UserID,
			Role:          m.Role,
			RoomSessions:  m.TotalSessions,
			RoomMinutes:   m.TotalMinutes,
			LastSpokeAt:   m.LastSpokeAt,
			TodaySessions: todaySessions[m.UserID],
			TodayMinutes:  todayMinutes[m.UserID],
			AITests:       aiCount[m.UserID],
		}
		if m.User != nil {
			row.Name = m.User.DisplayName()
			row.PhotoURL = m.User.PhotoURL
			row.Level = m.User.Level
			row.CurrentStreak = m.User.CurrentStreak
		}
		if b, ok := aiLatest[m.UserID]; ok {
			v := b
			row.LatestBand = &v
		}
		if b, ok := aiBest[m.UserID]; ok {
			v := b
			row.BestBand = &v
		}
		out = append(out, row)
	}
	return out, nil
}

// roomTodayByUser batches today's attendance for the whole roster in one
// query - the alternative is an N+1 that grows with class size.
func roomTodayByUser(roomID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]int, map[uuid.UUID]int) {
	sessions := make(map[uuid.UUID]int, len(userIDs))
	minutes := make(map[uuid.UUID]int, len(userIDs))
	if len(userIDs) == 0 {
		return sessions, minutes
	}

	start := startOfTashkentDay(time.Now())

	type row struct {
		UserID   uuid.UUID
		Sessions int
		Minutes  int
	}
	var rows []row
	database.DB.Raw(`
		SELECT user_id,
		       COUNT(*) AS sessions,
		       COALESCE(SUM(duration_seconds)::bigint / 60, 0) AS minutes
		FROM (
			SELECT user1_id AS user_id, duration_seconds FROM sessions
			WHERE room_id = ? AND status = 'ended' AND deleted_at IS NULL AND created_at >= ?
			UNION ALL
			SELECT user2_id AS user_id, duration_seconds FROM sessions
			WHERE room_id = ? AND status = 'ended' AND deleted_at IS NULL AND created_at >= ?
		) combined
		WHERE user_id IN ?
		GROUP BY user_id
	`, roomID, start, roomID, start, userIDs).Scan(&rows)

	for _, r := range rows {
		sessions[r.UserID] = r.Sessions
		minutes[r.UserID] = r.Minutes
	}
	return sessions, minutes
}

// aiSummaryByUser batches AI band summaries across both report tables.
func aiSummaryByUser(userIDs []uuid.UUID) (counts map[uuid.UUID]int, latest, best map[uuid.UUID]float64) {
	counts = make(map[uuid.UUID]int, len(userIDs))
	latest = make(map[uuid.UUID]float64, len(userIDs))
	best = make(map[uuid.UUID]float64, len(userIDs))
	if len(userIDs) == 0 {
		return
	}

	type row struct {
		UserID    uuid.UUID
		Cnt       int
		BestBand  float64
		LatestAt  time.Time
		LatestVal float64
	}
	var rows []row
	database.DB.Raw(`
		SELECT user_id,
		       COUNT(*)                                          AS cnt,
		       MAX(overall_band)                                 AS best_band,
		       MAX(created_at)                                   AS latest_at,
		       (ARRAY_AGG(overall_band ORDER BY created_at DESC))[1] AS latest_val
		FROM (
			SELECT user_id, overall_band, created_at
			FROM speaking_reports
			WHERE user_id IN ? AND deleted_at IS NULL AND overall_band > 0
			UNION ALL
			SELECT user_id, overall_band, created_at
			FROM full_test_reports
			WHERE user_id IN ? AND deleted_at IS NULL AND overall_band > 0
		) combined
		GROUP BY user_id
	`, userIDs, userIDs).Scan(&rows)

	for _, r := range rows {
		counts[r.UserID] = r.Cnt
		best[r.UserID] = r.BestBand
		latest[r.UserID] = r.LatestVal
	}
	return
}
