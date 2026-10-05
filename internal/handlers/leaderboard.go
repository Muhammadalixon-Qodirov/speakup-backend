package handlers

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// leaderboardMaxScan caps the user pool the scoring loop walks. The
// composite score is cheap, but we never want to load the entire users
// table into memory. 2000 active users is far more than the top-100
// leaderboard could ever surface - anything past that is noise.
const leaderboardMaxScan = 2000

// leaderboardCacheTTL is how long the rendered leaderboard rows stay in
// Redis. 60s is short enough that ranks feel live but long enough to
// absorb traffic spikes (every cache hit is ~one Redis GET vs ~3 DB
// queries + an in-memory sort).
const leaderboardCacheTTL = 60 * time.Second

// LeaderboardEntry is what the frontend renders for each row.
type LeaderboardEntry struct {
	Rank          int      `json:"rank"`
	UserID        string   `json:"user_id"`
	Name          string   `json:"name"`
	PhotoURL      *string  `json:"photo_url"`
	Level         *string  `json:"level"`
	Rating        float64  `json:"rating"`
	CurrentStreak int      `json:"current_streak"`
	Minutes       int      `json:"minutes"`     // weekly or all-time depending on period
	Sessions      int      `json:"sessions"`    // weekly or all-time
	Referrals     int      `json:"referrals"`   // weekly or all-time count
	Score         float64  `json:"score"`       // composite score that drove ranking
	IsPremium     bool     `json:"is_premium"`
	IsCurrentUser bool     `json:"is_current_user"`
	// Pinned IELTS Speaking band - surfaced so the leaderboard can
	// render the same coloured badge that appears on the user's profile.
	PinnedSpeakingBand *float64 `json:"pinned_speaking_band"`
}

type leaderboardRow struct {
	UserID       uuid.UUID
	SessionCount int
	TotalMinutes int
}

// Scoring helpers (BayesianRating, Composite) live in services so the
// Monday-cron prize job can reuse the exact same formula.

// Leaderboard handles GET /leaderboard?period=all|weekly&limit=50
//
// `period=weekly` → counts sessions/minutes from the last 7 days
// `period=all`    → uses the user's lifetime totals
//
// There is no level filter - the leaderboard is global. The result is
// cached in Redis for 60s so traffic spikes don't hammer Postgres.
func Leaderboard(c *fiber.Ctx) error {
	currentUser := middleware.GetCurrentUser(c)
	period := c.Query("period", "all")
	switch period {
	case "weekly", "stars", "streak":
		// valid as-is
	default:
		period = "all"
	}
	limit := c.QueryInt("limit", 50)
	if limit > 100 {
		limit = 100
	}

	// Weekly window: calendar week aligned to Monday 00:00 Tashkent.
	// At the boundary the board "resets" - everyone's weekly counters
	// go to zero at Mon 00:00 UZT until they earn new activity. Cron at
	// Mon 00:05 UZT snapshots last week's top 3 and grants them the
	// +10-minute bonus via WeeklyLeaderboardAward.
	weekAgo := utils.StartOfTashkentWeek(time.Now())

	if period == "weekly" {
		// Tell the client when the current window ends so it can show
		// a "keyingi reset: X soat" countdown. Headers (not body) so we
		// stay backward-compatible with old clients that expect a plain
		// array. Set before the cache branch so a cache hit still
		// carries the timing.
		c.Set("X-Week-Start", weekAgo.Format(time.RFC3339))
		c.Set("X-Next-Reset-At", utils.NextTashkentWeekStart(time.Now()).Format(time.RFC3339))
	}

	// Cache lookup. The cached payload is computed without "is current
	// user" so we restamp that flag per request.
	if entries, ok := loadCachedLeaderboard(period); ok {
		stamped := stampCurrentUser(entries, currentUser)
		if len(stamped) > limit {
			stamped = stamped[:limit]
		}
		return utils.Success(c, stamped)
	}

	// All DB calls share a 5-second deadline so a slow query can't pin
	// the request goroutine indefinitely.
	ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
	defer cancel()
	db := database.DB.WithContext(ctx)

	// 1. Pull active users with a hard ceiling. We can never display more
	//    than the top `limit` rows, so loading more than `leaderboardMaxScan`
	//    is wasted work. Order by total_minutes so the most active users
	//    are guaranteed to be in the candidate pool.
	var users []models.User
	if err := db.
		Where("is_banned = ? AND is_active = ?", false, true).
		Order("total_minutes DESC").
		Limit(leaderboardMaxScan).
		Find(&users).Error; err != nil {
		log.Warn().Err(err).Msg("leaderboard: user fetch failed")
		return utils.Success(c, []LeaderboardEntry{})
	}

	if len(users) == 0 {
		return utils.Success(c, []LeaderboardEntry{})
	}

	// 2. For weekly, compute per-user session/minute aggregates.
	//
	// `room_id IS NULL` excludes private-room (classroom) sessions on
	// purpose. Room minutes are uncapped and free, so counting them here
	// would let one learning centre sweep the weekly prize by simply
	// holding a long lesson. Teachers get their own room report instead;
	// this board stays a public-queue competition.
	weeklyByUser := make(map[uuid.UUID]leaderboardRow)
	if period == "weekly" {
		var rows []leaderboardRow
		db.Raw(`
			SELECT user_id, SUM(session_count) AS session_count, SUM(total_minutes) AS total_minutes
			FROM (
				SELECT user1_id AS user_id, COUNT(*) AS session_count, COALESCE(SUM(duration_seconds / 60)::bigint, 0) AS total_minutes
				FROM sessions
				WHERE status = 'ended' AND created_at >= ? AND deleted_at IS NULL
					AND room_id IS NULL
				GROUP BY user1_id
				UNION ALL
				SELECT user2_id AS user_id, COUNT(*) AS session_count, COALESCE(SUM(duration_seconds / 60)::bigint, 0) AS total_minutes
				FROM sessions
				WHERE status = 'ended' AND created_at >= ? AND deleted_at IS NULL
					AND room_id IS NULL
				GROUP BY user2_id
			) combined
			GROUP BY user_id
		`, weekAgo, weekAgo).Scan(&rows)
		for _, r := range rows {
			weeklyByUser[r.UserID] = r
		}
	}

	// 3. Pull referral counts (lifetime + last 7d) in two short queries.
	type refCountRow struct {
		ReferredBy uuid.UUID
		C          int
	}
	totalRefMap := make(map[uuid.UUID]int)
	{
		var rows []refCountRow
		db.Raw(`
			SELECT referred_by, COUNT(*) AS c
			FROM users
			WHERE referred_by IS NOT NULL
			GROUP BY referred_by
		`).Scan(&rows)
		for _, r := range rows {
			totalRefMap[r.ReferredBy] = r.C
		}
	}
	weeklyRefMap := make(map[uuid.UUID]int)
	if period == "weekly" {
		var rows []refCountRow
		db.Raw(`
			SELECT referred_by, COUNT(*) AS c
			FROM users
			WHERE referred_by IS NOT NULL AND created_at >= ?
			GROUP BY referred_by
		`, weekAgo).Scan(&rows)
		for _, r := range rows {
			weeklyRefMap[r.ReferredBy] = r.C
		}
	}

	// 3b. Weekly streak + rating. In weekly mode these should reflect
	// THIS WEEK only - a user with a 30-day lifetime streak but no
	// sessions this week should show 0 streak here, same for rating.
	// Computed in two batch queries to avoid N+1.
	type weeklyStreakRow struct {
		UserID uuid.UUID
		Days   int
	}
	weeklyStreakMap := make(map[uuid.UUID]int)
	type weeklyRatingRow struct {
		RateeID uuid.UUID
		Avg     float64
		Votes   int
	}
	weeklyRatingMap := make(map[uuid.UUID]weeklyRatingRow)
	if period == "weekly" {
		var streakRows []weeklyStreakRow
		// Count distinct Tashkent-local calendar days on which the
		// user had an ended session - caps at 7 per natural week.
		db.Raw(`
			SELECT user_id, COUNT(DISTINCT DATE(created_at AT TIME ZONE 'Asia/Tashkent')) AS days
			FROM (
				SELECT user1_id AS user_id, created_at FROM sessions
				WHERE status = 'ended' AND created_at >= ? AND deleted_at IS NULL
					AND room_id IS NULL
				UNION ALL
				SELECT user2_id AS user_id, created_at FROM sessions
				WHERE status = 'ended' AND created_at >= ? AND deleted_at IS NULL
					AND room_id IS NULL
			) combined
			GROUP BY user_id
		`, weekAgo, weekAgo).Scan(&streakRows)
		for _, r := range streakRows {
			weeklyStreakMap[r.UserID] = r.Days
		}

		var ratingRows []weeklyRatingRow
		db.Raw(`
			SELECT ratee_id, AVG(rating)::float AS avg, COUNT(*) AS votes
			FROM session_ratings
			WHERE created_at >= ? AND deleted_at IS NULL
			GROUP BY ratee_id
		`, weekAgo).Scan(&ratingRows)
		for _, r := range ratingRows {
			weeklyRatingMap[r.RateeID] = r
		}
	}

	// 4. Score every user.
	entries := make([]LeaderboardEntry, 0, len(users))
	for _, u := range users {
		var minutes, sessions, referrals, streak int
		var rawRating float64
		var ratingCount int
		if period == "weekly" {
			if r, ok := weeklyByUser[u.ID]; ok {
				minutes = r.TotalMinutes
				sessions = r.SessionCount
			}
			referrals = weeklyRefMap[u.ID]
			streak = weeklyStreakMap[u.ID]
			if wr, ok := weeklyRatingMap[u.ID]; ok {
				rawRating = wr.Avg
				ratingCount = wr.Votes
			}
		} else {
			minutes = u.TotalMinutes
			sessions = u.TotalSessions
			referrals = totalRefMap[u.ID]
			streak = u.CurrentStreak
			rawRating = u.Rating
			ratingCount = u.RatingCount
		}

		// Skip users with absolutely no activity in this window.
		if period == "weekly" && sessions == 0 && referrals == 0 {
			continue
		}
		// Stars tab needs enough ratings to be statistically meaningful.
		// Combined with Bayesian smoothing (m=20), a hard floor of 5
		// ratings means even a 5×5★ blitz user can't out-rank a
		// long-time user with 50+ mixed but high-average ratings.
		if period == "stars" && ratingCount < 5 {
			continue
		}
		// Streak tab: a streak of 0 isn't an achievement worth showing.
		if period == "streak" && streak == 0 {
			continue
		}

		// Use the Bayesian-weighted rating so a single 5-star vote can't
		// vault someone above a heavily-rated 4.x user.
		weighted := services.BayesianRating(rawRating, ratingCount)
		var score float64
		switch period {
		case "stars":
			// Pure rating ranking, Bayesian-shrunk.
			score = weighted
		case "streak":
			// Pure streak ranking. Tie-break by composite so two users
			// with the same streak don't shuffle randomly between hits.
			score = float64(streak)*1000 + services.Composite(streak, weighted, minutes, referrals)
		default:
			score = services.Composite(streak, weighted, minutes, referrals)
		}

		entries = append(entries, LeaderboardEntry{
			UserID:             u.ID.String(),
			Name:               u.DisplayName(),
			PhotoURL:           u.PhotoURL,
			Level:              u.Level,
			Rating:             rawRating, // weekly avg in weekly mode, lifetime otherwise
			CurrentStreak:      streak,    // weekly days-spoken in weekly mode
			Minutes:            minutes,
			Sessions:           sessions,
			Referrals:          referrals,
			Score:              score,
			IsPremium:          u.IsPremium,
			PinnedSpeakingBand: u.PinnedSpeakingBand,
			// is_current_user is restamped per request after cache hit.
		})
	}

	// 5. Sort by composite score descending.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Score > entries[j].Score
	})
	// Cache the top 100 (the absolute max anyone can request) so all
	// callers share the same warm slice regardless of their `limit`.
	if len(entries) > 100 {
		entries = entries[:100]
	}
	for i := range entries {
		entries[i].Rank = i + 1
	}
	storeCachedLeaderboard(period, entries)

	stamped := stampCurrentUser(entries, currentUser)
	if len(stamped) > limit {
		stamped = stamped[:limit]
	}
	return utils.Success(c, stamped)
}

// stampCurrentUser returns a copy of `entries` with `IsCurrentUser` set
// for the matching row. We do this outside the cache because the cached
// payload is shared across all viewers.
func stampCurrentUser(entries []LeaderboardEntry, current *models.User) []LeaderboardEntry {
	out := make([]LeaderboardEntry, len(entries))
	copy(out, entries)
	if current == nil {
		return out
	}
	currentID := current.ID.String()
	for i := range out {
		if out[i].UserID == currentID {
			out[i].IsCurrentUser = true
		}
	}
	return out
}

// loadCachedLeaderboard fetches the cached entries for a period from
// Redis. Cache misses (or any error) just fall through to a fresh
// compute - never break the request.
func loadCachedLeaderboard(period string) ([]LeaderboardEntry, bool) {
	if database.Redis == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	raw, err := database.Redis.Get(ctx, "leaderboard:v2:"+period).Result()
	if err != nil || raw == "" {
		return nil, false
	}
	var entries []LeaderboardEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, false
	}
	return entries, true
}

// storeCachedLeaderboard writes the freshly-computed entries to Redis
// with a short TTL. Errors are swallowed - the cache is a perf
// optimization, not a source of truth.
func storeCachedLeaderboard(period string, entries []LeaderboardEntry) {
	if database.Redis == nil {
		return
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = database.Redis.Set(ctx, "leaderboard:v2:"+period, payload, leaderboardCacheTTL).Err()
}

// WeeklyLeaderboard is kept as a thin shim for the old route to avoid
// breaking deployed clients. New code should call Leaderboard with
// `period=weekly`.
func WeeklyLeaderboard(c *fiber.Ctx) error {
	c.Request().URI().QueryArgs().Set("period", "weekly")
	return Leaderboard(c)
}
