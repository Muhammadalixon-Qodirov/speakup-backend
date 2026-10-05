package services

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm/clause"

	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

// WeeklyLeaderboardBonusMinutes is the fixed size of each top-3 reward.
// Kept as a constant so the scheduler, the bonus-sum helper and any
// admin-panel copy all agree on one number.
const WeeklyLeaderboardBonusMinutes = 10

// WeeklyLeaderboardBonusAITests is the extra AI-speaking-test allowance
// every top-3 finisher gets for the following week. Adds to the base
// weekly limit (free=1, premium=2) so a free-tier winner can take 2
// tests that week and a premium winner can take 3.
const WeeklyLeaderboardBonusAITests = 1

// weeklyLbCandidate is an aggregated row we build up while computing
// last week's ranking. Matches the fields the public Leaderboard handler
// uses for composite scoring so the winner list is consistent with what
// users actually saw.
type weeklyLbCandidate struct {
	UserID        uuid.UUID
	Score         float64
	Sessions      int
	Minutes       int
	Referrals     int
	CurrentStreak int
	RawRating     float64
	RatingCount   int
}

// SumWeeklyLeaderboardBonus totals every active weekly-leaderboard
// bonus for the given user. "Active" = awarded less than 7 days ago,
// so Monday's winners carry their +10 into the following Monday. Wired
// into the daily-usage bonus calculation in cmd/server/main.go.
func SumWeeklyLeaderboardBonus(userID string) int {
	cutoff := time.Now().AddDate(0, 0, -7)
	var total int
	database.DB.Model(&models.WeeklyLeaderboardAward{}).
		Where("user_id = ? AND created_at >= ?", userID, cutoff).
		Select("COALESCE(SUM(minutes), 0)").
		Scan(&total)
	return total
}

// SumWeeklyLeaderboardAIBonus returns the extra AI-speaking-test slots
// the user earned from the weekly leaderboard in the last 7 days.
// Counts awards (not minutes) × WeeklyLeaderboardBonusAITests. Wired
// into the weekly limit check in ai_fulltest.go + the usage snapshot
// in ai_service.go so both server-side enforcement and the frontend
// "X / Y" label reflect the bonus.
func SumWeeklyLeaderboardAIBonus(userID string) int {
	cutoff := time.Now().AddDate(0, 0, -7)
	var count int64
	database.DB.Model(&models.WeeklyLeaderboardAward{}).
		Where("user_id = ? AND created_at >= ?", userID, cutoff).
		Count(&count)
	return int(count) * WeeklyLeaderboardBonusAITests
}

// AwardWeeklyLeaderboardPrizes snapshots last week's top 3 (Mon-Sun
// Tashkent) and inserts one WeeklyLeaderboardAward per winner. Idempotent
// via the unique (user_id, week_start) constraint - calling this twice
// on the same Monday is safe, the second call is a no-op.
//
// Returns the list of newly-inserted winners (may be empty if none
// qualified, if re-running on the same week, or if DB errors).
func AwardWeeklyLeaderboardPrizes() []models.WeeklyLeaderboardAward {
	now := time.Now()
	// "Last week" is the Monday before this one. If today is Mon 00:05
	// UZT, the window we just closed is [prev Mon 00:00, this Mon 00:00).
	thisMonday := utils.StartOfTashkentWeek(now)
	lastMonday := thisMonday.AddDate(0, 0, -7)

	winners := computeWeeklyTop3(lastMonday, thisMonday)
	if len(winners) == 0 {
		log.Info().
			Time("week_start", lastMonday).
			Msg("weekly leaderboard: no qualifying users, skipping prize")
		return nil
	}

	inserted := make([]models.WeeklyLeaderboardAward, 0, len(winners))
	for i, w := range winners {
		award := models.WeeklyLeaderboardAward{
			UserID:    w.UserID,
			WeekStart: lastMonday,
			Rank:      i + 1,
			Minutes:   WeeklyLeaderboardBonusMinutes,
		}
		// ON CONFLICT DO NOTHING via the unique (user_id, week_start)
		// index - survives accidental double-runs without erroring.
		res := database.DB.
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&award)
		if res.Error != nil {
			log.Warn().
				Err(res.Error).
				Str("user_id", w.UserID.String()).
				Msg("weekly leaderboard: award insert failed")
			continue
		}
		if res.RowsAffected == 0 {
			// Already awarded on a previous run - skip notification too
			// so winners don't receive the same bot DM twice.
			continue
		}
		inserted = append(inserted, award)
		log.Info().
			Str("user_id", w.UserID.String()).
			Int("rank", i+1).
			Int("minutes", WeeklyLeaderboardBonusMinutes).
			Time("week_start", lastMonday).
			Msg("weekly leaderboard: bonus awarded")
	}

	return inserted
}

// computeWeeklyTop3 replicates the ranking logic of the public Leaderboard
// handler but for an arbitrary [start, end) window. Pure function over
// DB state so the scheduler can reuse it without pulling in an HTTP context.
// All ranking inputs (streak, rating, sessions, minutes, referrals) are
// weekly-scoped - matches what the user saw on the leaderboard all week.
func computeWeeklyTop3(start, end time.Time) []weeklyLbCandidate {
	// Candidate pool - only users that had some activity in the window.
	type sessionAgg struct {
		UserID       uuid.UUID
		SessionCount int
		TotalMinutes int
	}

	var sessionRows []sessionAgg
	database.DB.Raw(`
		SELECT user_id, SUM(session_count) AS session_count, SUM(total_minutes) AS total_minutes
		FROM (
			SELECT user1_id AS user_id, COUNT(*) AS session_count, COALESCE(SUM(duration_seconds / 60)::bigint, 0) AS total_minutes
			FROM sessions
			WHERE status = 'ended' AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
				AND room_id IS NULL
			GROUP BY user1_id
			UNION ALL
			SELECT user2_id AS user_id, COUNT(*) AS session_count, COALESCE(SUM(duration_seconds / 60)::bigint, 0) AS total_minutes
			FROM sessions
			WHERE status = 'ended' AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
				AND room_id IS NULL
			GROUP BY user2_id
		) combined
		GROUP BY user_id
	`, start, end, start, end).Scan(&sessionRows)

	type refAgg struct {
		ReferredBy uuid.UUID
		C          int
	}
	var refRows []refAgg
	database.DB.Raw(`
		SELECT referred_by, COUNT(*) AS c
		FROM users
		WHERE referred_by IS NOT NULL AND created_at >= ? AND created_at < ?
		GROUP BY referred_by
	`, start, end).Scan(&refRows)
	refMap := make(map[uuid.UUID]int, len(refRows))
	for _, r := range refRows {
		refMap[r.ReferredBy] = r.C
	}

	// Weekly streak (days-spoken in window) + weekly rating.
	type streakAgg struct {
		UserID uuid.UUID
		Days   int
	}
	var streakRows []streakAgg
	database.DB.Raw(`
		SELECT user_id, COUNT(DISTINCT DATE(created_at AT TIME ZONE 'Asia/Tashkent')) AS days
		FROM (
			SELECT user1_id AS user_id, created_at FROM sessions
			WHERE status = 'ended' AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
				AND room_id IS NULL
			UNION ALL
			SELECT user2_id AS user_id, created_at FROM sessions
			WHERE status = 'ended' AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
				AND room_id IS NULL
		) combined
		GROUP BY user_id
	`, start, end, start, end).Scan(&streakRows)
	streakMap := make(map[uuid.UUID]int, len(streakRows))
	for _, r := range streakRows {
		streakMap[r.UserID] = r.Days
	}

	type ratingAgg struct {
		RateeID uuid.UUID
		Avg     float64
		Votes   int
	}
	var ratingRows []ratingAgg
	database.DB.Raw(`
		SELECT ratee_id, AVG(rating)::float AS avg, COUNT(*) AS votes
		FROM session_ratings
		WHERE created_at >= ? AND created_at < ? AND deleted_at IS NULL
		GROUP BY ratee_id
	`, start, end).Scan(&ratingRows)
	ratingMap := make(map[uuid.UUID]ratingAgg, len(ratingRows))
	for _, r := range ratingRows {
		ratingMap[r.RateeID] = r
	}

	// Seed candidate set from both signals.
	candidates := make(map[uuid.UUID]*weeklyLbCandidate)
	for _, s := range sessionRows {
		candidates[s.UserID] = &weeklyLbCandidate{
			UserID:   s.UserID,
			Sessions: s.SessionCount,
			Minutes:  s.TotalMinutes,
		}
	}
	for uid, c := range refMap {
		if candidates[uid] == nil {
			candidates[uid] = &weeklyLbCandidate{UserID: uid}
		}
		candidates[uid].Referrals = c
	}

	if len(candidates) == 0 {
		return nil
	}

	// Filter out banned/soft-deleted users.
	ids := make([]uuid.UUID, 0, len(candidates))
	for uid := range candidates {
		ids = append(ids, uid)
	}
	var users []models.User
	database.DB.Where("id IN ? AND is_banned = ? AND is_active = ?", ids, false, true).Find(&users)
	userMap := make(map[uuid.UUID]models.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}

	scored := make([]weeklyLbCandidate, 0, len(candidates))
	for uid, c := range candidates {
		if _, ok := userMap[uid]; !ok {
			continue
		}
		if c.Sessions == 0 && c.Referrals == 0 {
			continue
		}
		c.CurrentStreak = streakMap[uid]
		if r, ok := ratingMap[uid]; ok {
			c.RawRating = r.Avg
			c.RatingCount = r.Votes
		}
		weighted := BayesianRating(c.RawRating, c.RatingCount)
		c.Score = Composite(c.CurrentStreak, weighted, c.Minutes, c.Referrals)
		scored = append(scored, *c)
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > 3 {
		scored = scored[:3]
	}
	return scored
}
