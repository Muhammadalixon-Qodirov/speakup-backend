package scheduler

import (
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/ws"
)

var c *cron.Cron

// Start initializes and starts the background scheduler.
func Start() {
	c = cron.New()

	// Daily at 00:05 UTC - deactivate expired premium subscriptions
	c.AddFunc("5 0 * * *", func() {
		safego.Run("cron/checkPremiumExpiry", checkPremiumExpiry)
	})

	// Daily at 20:05 UTC = 01:05 Tashkent - recompute every user's
	// current_streak so broken streaks actually flip to 0 (the on-write
	// recompute only fires when a user qualifies, so an inactive user's
	// stale streak would otherwise linger forever).
	c.AddFunc("5 20 * * *", func() {
		safego.Run("cron/recomputeAllStreaks", func() {
			n, err := services.RecomputeAllStreaks()
			if err != nil {
				log.Warn().Err(err).Msg("recomputeAllStreaks failed")
				return
			}
			log.Info().Int("users", n).Msg("Streaks recomputed")
		})
	})

	// Daily at 04:00 UTC (09:00 UZT) - ertalab streak reminder
	c.AddFunc("0 4 * * *", func() {
		safego.Run("cron/sendStreakReminders", sendStreakReminders)
	})

	// Daily at 10:00 UTC (15:00 UZT) - remind inactive users (3+ kun)
	c.AddFunc("0 10 * * *", func() {
		safego.Run("cron/sendInactivityReminders", sendInactivityReminders)
	})

	// Daily at 13:00 UTC (18:00 UZT) - kechqurun "X kishi online" eslatmasi
	c.AddFunc("0 13 * * *", func() {
		safego.Run("cron/sendOnlineReminders", sendOnlineReminders)
	})

	// Monday at 05:00 UTC (10:00 UZT) - haftalik hisobot
	c.AddFunc("0 5 * * 1", func() {
		safego.Run("cron/sendWeeklyReports", sendWeeklyReports)
	})

	// Sunday at 19:05 UTC = Monday 00:05 UZT - weekly leaderboard prize
	// snapshots last week's Mon-Sun (Tashkent) top 3 and awards +10 min
	// bonus. Runs 5 min after the board's weekly window resets so all
	// in-flight session-end writes from Sunday 23:59 have landed.
	c.AddFunc("5 19 * * 0", func() {
		safego.Run("cron/awardWeeklyLeaderboardPrizes", awardWeeklyLeaderboardPrizes)
	})

	// Daily at 14:00 UTC (19:00 UZT) - leaderboard challenge
	c.AddFunc("0 14 * * *", func() {
		safego.Run("cron/sendLeaderboardChallenges", sendLeaderboardChallenges)
	})

	// Monday at 02:00 UTC (07:00 UZT) - weekly self-learning cleanup of
	// Vocabulary Sprint topic words: the LLM marks accumulated words as
	// on-topic (trusted) or clearly off-topic (rejected).
	c.AddFunc("0 2 * * 1", func() {
		safego.Run("cron/cleanupTopicWords", services.CleanupTopicWords)
	})

	// Monday at 02:30 UTC - weekly self-learning cleanup of Synonym Duel
	// candidate answers: the LLM confirms genuine synonyms/antonyms players
	// typed that weren't in the bank, so the accepted set grows over time.
	c.AddFunc("30 2 * * 1", func() {
		safego.Run("cron/cleanupSynonymCandidates", services.CleanupSynonymCandidates)
	})

	// Daily at 03:00 UTC - top up the Dictation Race sentence pool with AI-
	// generated sentences while it's below target, so content stays fresh.
	c.AddFunc("0 3 * * *", func() {
		safego.Run("cron/replenishDictation", services.ReplenishDictation)
	})

	// Daily at 03:30 UTC - top up the read-aloud pronunciation pool. Half an
	// hour after the dictation job so the two never hammer the LLM keys at the
	// same time, and in the same quiet window, since a filling run makes
	// dozens of generation calls.
	c.AddFunc("30 3 * * *", func() {
		safego.Run("cron/replenishPronunciation", services.ReplenishPronunciation)
	})

	// Hourly - render reference audio for passages that still lack it. Hourly
	// rather than daily because it is capped per run, so a freshly filled pool
	// needs several passes, and a passage without audio is half an exercise.
	c.AddFunc("15 * * * *", func() {
		safego.Run("cron/renderPronunciationAudio", services.RenderPendingReferences)
	})

	// Every minute - scheduled speaking appointments. A minute is the
	// smallest unit a user can pick, so anything coarser would send the
	// "starting now" ping after the window had already begun.
	c.AddFunc("* * * * *", func() {
		safego.Run("cron/sweepScheduledSessions", sweepScheduledSessions)
	})

	c.Start()
	log.Info().Msg("Background scheduler started")
}

// Stop gracefully stops the scheduler.
func Stop() {
	if c != nil {
		c.Stop()
	}
}

// checkPremiumExpiry deactivates users whose premium has expired.
func checkPremiumExpiry() {
	now := time.Now()
	result := database.DB.
		Model(&models.User{}).
		Where("is_premium = ? AND premium_expires_at < ?", true, now).
		Updates(map[string]interface{}{"is_premium": false})

	if result.RowsAffected > 0 {
		log.Info().Int64("count", result.RowsAffected).Msg("Expired premium subscriptions deactivated")
	}
}

// sendStreakReminders - ertalab 09:00 UZT da streak eslatmasi.
// Oxirgi 3 kun ichida faol bo'lgan userlarga yuboriladi.
func sendStreakReminders() {
	cutoff := time.Now().AddDate(0, 0, -3)

	var users []models.User
	database.DB.
		Where("is_banned = ? AND is_active = ? AND bot_blocked = ? AND telegram_id != 0", false, true, false).
		Where("last_active_at IS NOT NULL AND last_active_at > ?", cutoff).
		Find(&users)

	sent := 0
	for _, u := range users {
		// Online userlarga yubormaslik
		if ws.H != nil && ws.H.GetClientByUserID(u.ID.String()) != nil {
			continue
		}
		if bot.SendStreakReminder(u.TelegramID, u.CurrentStreak) {
			sent++
		}
		if sent%25 == 0 && sent > 0 {
			time.Sleep(1 * time.Second)
		}
	}

	if sent > 0 {
		log.Info().Int("count", sent).Msg("Streak reminders sent")
	}
}

// sendInactivityReminders - 3+ kun kirmagan userlarga eslatma.
func sendInactivityReminders() {
	cutoff3 := time.Now().AddDate(0, 0, -3)
	cutoff30 := time.Now().AddDate(0, 0, -30)

	var users []models.User
	database.DB.
		Where("is_banned = ? AND is_active = ? AND bot_blocked = ? AND telegram_id != 0", false, true, false).
		Where("last_active_at IS NOT NULL AND last_active_at < ? AND last_active_at > ?", cutoff3, cutoff30).
		Find(&users)

	sent := 0
	for _, u := range users {
		daysInactive := int(time.Since(*u.LastActiveAt).Hours() / 24)
		if bot.SendInactivityReminder(u.TelegramID, daysInactive) {
			sent++
		}
		if sent%25 == 0 && sent > 0 {
			time.Sleep(1 * time.Second)
		}
	}

	if sent > 0 {
		log.Info().Int("count", sent).Msg("Inactivity reminders sent")
	}
}

// sendOnlineReminders - kechqurun 18:00 UZT da "hozir X kishi online".
// Faqat oxirgi 2 kun ichida faol lekin hozir offline userlarga.
func sendOnlineReminders() {
	onlineCount := 0
	if ws.H != nil {
		onlineCount = ws.H.OnlineCount()
	}
	// Kamida 3 kishi online bo'lsagina yuboramiz
	if onlineCount < 3 {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -2)

	var users []models.User
	database.DB.
		Where("is_banned = ? AND is_active = ? AND bot_blocked = ? AND telegram_id != 0", false, true, false).
		Where("last_active_at IS NOT NULL AND last_active_at > ?", cutoff).
		Find(&users)

	sent := 0
	for _, u := range users {
		// Online userlarga yubormaslik
		if ws.H != nil && ws.H.GetClientByUserID(u.ID.String()) != nil {
			continue
		}
		if bot.SendOnlineReminder(u.TelegramID, onlineCount) {
			sent++
		}
		if sent%25 == 0 && sent > 0 {
			time.Sleep(1 * time.Second)
		}
	}

	if sent > 0 {
		log.Info().Int("count", sent).Int("online", onlineCount).Msg("Online reminders sent")
	}
}

// sendWeeklyReports - har dushanba 10:00 UZT da haftalik hisobot.
// Oxirgi 14 kun ichida faol bo'lgan userlarga yuboriladi.
//
// Previously this ran 2×N DB queries (one COUNT + one SUM per user).
// Now a single aggregated query fetches all stats, and a second query
// builds the leaderboard rank map. Total: 2 queries regardless of user
// count.
func sendWeeklyReports() {
	weekAgo := time.Now().AddDate(0, 0, -7)

	// Single aggregated query: per-user weekly stats.
	type weeklyRow struct {
		UserID        string
		TelegramID    int64
		CurrentStreak int
		SessionCount  int64
		TotalMinutes  int64
	}
	var rows []weeklyRow
	database.DB.Raw(`
		SELECT
			u.id AS user_id,
			u.telegram_id,
			u.current_streak,
			COUNT(s.id) AS session_count,
			COALESCE((SUM(s.duration_seconds) / 60)::bigint, 0) AS total_minutes
		FROM users u
		LEFT JOIN sessions s ON (s.user1_id = u.id OR s.user2_id = u.id)
			AND s.status = 'ended'
			AND s.started_at > ?
		WHERE u.is_banned = false
			AND u.is_active = true
			AND u.bot_blocked = false
			AND u.telegram_id != 0
			AND u.last_active_at IS NOT NULL
			AND u.last_active_at > NOW() - INTERVAL '14 days'
		GROUP BY u.id, u.telegram_id, u.current_streak
		HAVING COUNT(s.id) > 0 OR u.current_streak > 0
	`, weekAgo).Scan(&rows)

	if len(rows) == 0 {
		return
	}

	// Leaderboard ranks (top 100).
	var leaderboard []struct {
		ID           string
		TotalMinutes int
	}
	database.DB.Raw(`
		SELECT u.id, COALESCE((SUM(s.duration_seconds)/60)::bigint, 0) as total_minutes
		FROM users u
		LEFT JOIN sessions s ON (s.user1_id = u.id OR s.user2_id = u.id)
			AND s.status = 'ended' AND s.started_at > ?
		GROUP BY u.id
		HAVING COALESCE(SUM(s.duration_seconds), 0) >= 60
		ORDER BY total_minutes DESC
		LIMIT 100
	`, weekAgo).Scan(&leaderboard)

	ranks := make(map[string]int, len(leaderboard))
	for i, entry := range leaderboard {
		ranks[entry.ID] = i + 1
	}

	log.Info().Int("recipients", len(rows)).Msg("Sending weekly reports")

	sent := 0
	for _, r := range rows {
		rank := ranks[r.UserID]
		if bot.SendWeeklyReport(r.TelegramID, int(r.SessionCount), int(r.TotalMinutes), r.CurrentStreak, rank) {
			sent++
		}
		// Telegram rate limit: ~20 msg/sec is safe.
		if sent%25 == 0 && sent > 0 {
			time.Sleep(1 * time.Second)
		}
	}

	if sent > 0 {
		log.Info().Int("count", sent).Msg("Weekly reports sent")
	}
}

// sendLeaderboardChallenges - leaderboard top 50 dagi userlarga
// "#X o'rindasiz, yana Y daqiqa = #Z o'rin" eslatmasi.
func sendLeaderboardChallenges() {
	weekAgo := time.Now().AddDate(0, 0, -7)

	var entries []struct {
		ID           string
		TelegramID   int64
		TotalMinutes int
	}
	database.DB.Raw(`
		SELECT u.id, u.telegram_id, COALESCE((SUM(s.duration_seconds)/60)::bigint, 0) as total_minutes
		FROM users u
		LEFT JOIN sessions s ON (s.user1_id = u.id OR s.user2_id = u.id)
			AND s.status = 'ended' AND s.started_at > ?
		WHERE u.is_banned = false AND u.is_active = true AND u.bot_blocked = false AND u.telegram_id != 0
		GROUP BY u.id, u.telegram_id
		HAVING COALESCE(SUM(s.duration_seconds), 0) >= 60
		ORDER BY total_minutes DESC
		LIMIT 50
	`, weekAgo).Scan(&entries)

	if len(entries) < 3 {
		return // Juda kam user - challenge yuborish ma'nosiz
	}

	sent := 0
	for i, entry := range entries {
		rank := i + 1
		// Birinchi o'rindagi userga yubormaslik (allaqachon top)
		if rank == 1 {
			continue
		}
		// Online userlarga yubormaslik
		if ws.H != nil && ws.H.GetClientByUserID(entry.ID) != nil {
			continue
		}

		// Undan oldingi user bilan farq
		prevMinutes := entries[i-1].TotalMinutes
		diff := prevMinutes - entry.TotalMinutes
		if diff <= 0 {
			diff = 1
		}

		nextRank := rank - 1
		if bot.SendLeaderboardChallenge(entry.TelegramID, rank, nextRank, diff) {
			sent++
		}
		if sent%25 == 0 && sent > 0 {
			time.Sleep(1 * time.Second)
		}
	}

	if sent > 0 {
		log.Info().Int("count", sent).Msg("Leaderboard challenges sent")
	}
}

// awardWeeklyLeaderboardPrizes - cron job that runs every Monday 00:05
// Tashkent time (= Sunday 19:05 UTC). Snapshots the previous Monday→
// Sunday top 3 and awards each winner a +10-minute bonus via the
// WeeklyLeaderboardAward table. Idempotent: the unique (user_id,
// week_start) constraint means a double-run is harmless.
func awardWeeklyLeaderboardPrizes() {
	awards := services.AwardWeeklyLeaderboardPrizes()
	if len(awards) == 0 {
		return
	}

	// Notify every fresh winner via the bot. Already-notified awards
	// (from a previous partial run) have the flag set - skip them.
	for i := range awards {
		award := &awards[i]
		var user models.User
		if err := database.DB.First(&user, "id = ?", award.UserID).Error; err != nil {
			continue
		}
		if bot.SendWeeklyWinnerNotif(user.TelegramID, award.Rank, award.Minutes, services.WeeklyLeaderboardBonusAITests) {
			database.DB.Model(award).Update("notified", true)
		}
	}

	log.Info().Int("winners", len(awards)).Msg("Weekly leaderboard prizes awarded")
}

// sweepScheduledSessions drives every time-based transition of a
// speaking appointment: the fifteen-minute warning, the "it's time"
// ping, and closing the window on the ones nobody walked into.
//
// Runs once a minute and is written to be safe if a tick is missed or
// runs twice: each notification is stamped in the row before it is sent,
// and the expiry pass only touches rows whose window has already closed.
func sweepScheduledSessions() {
	now := time.Now()

	// 1. "Starts in 15 minutes."
	for _, s := range services.DueForReminder(now) {
		minutes := int(s.ScheduledAt.Sub(now).Minutes())
		if minutes < 1 {
			minutes = 1
		}
		// Stamp first: a Telegram timeout must not turn into the same
		// reminder every minute until the appointment starts.
		services.MarkReminded(s.ID)
		notifyBothScheduled(&s, func(recipient, partner *models.User) {
			bot.SendScheduleReminder(recipient.TelegramID, partner.DisplayName(), minutes)
		})
		pushScheduledUpdate(&s, "reminder")
	}

	// 2. "It's time - you have ten minutes."
	for _, s := range services.DueForStart(now) {
		services.MarkStartNotified(s.ID)
		notifyBothScheduled(&s, func(recipient, partner *models.User) {
			bot.SendScheduleStarting(recipient.TelegramID, partner.DisplayName())
		})
		// The card has to flip to its joinable state for anyone who
		// already has the app open, without them refreshing.
		pushScheduledUpdate(&s, "open")
	}

	// 3. Close the window on the ones nobody kept.
	for _, s := range services.ExpireStaleScheduled(now) {
		pushScheduledUpdate(&s, "missed")
	}
}

// notifyBothScheduled runs fn once per participant, handing it that
// person's own record and their partner's, so message text never has to
// work out which side it is addressing.
func notifyBothScheduled(s *models.ScheduledSession, fn func(recipient, partner *models.User)) {
	if s.User1 == nil || s.User2 == nil {
		return
	}
	if s.User1.TelegramID != 0 {
		fn(s.User1, s.User2)
	}
	if s.User2.TelegramID != 0 {
		fn(s.User2, s.User1)
	}
}

func pushScheduledUpdate(s *models.ScheduledSession, reason string) {
	payload := map[string]interface{}{
		"reason": reason,
		"id":     s.ID.String(),
	}
	ws.SendToUser(s.User1ID.String(), "scheduled_update", payload)
	ws.SendToUser(s.User2ID.String(), "scheduled_update", payload)
}
