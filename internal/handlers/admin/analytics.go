package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/utils"
)

// HourlyActivity handles GET /admin/analytics/hourly?date=2026-04-16
// Soatlik aktivlik - qaysi soatda nechta session bo'lgan.
func HourlyActivity(c *fiber.Ctx) error {
	dateStr := c.Query("date", time.Now().Format("2006-01-02"))

	type HourRow struct {
		Hour     int `json:"hour"`
		Sessions int `json:"sessions"`
		Users    int `json:"users"`
	}

	var rows []HourRow
	database.DB.Raw(`
		SELECT
			EXTRACT(HOUR FROM created_at AT TIME ZONE 'Asia/Tashkent') AS hour,
			COUNT(*) AS sessions,
			COUNT(DISTINCT user1_id) + COUNT(DISTINCT user2_id) AS users
		FROM sessions
		WHERE DATE(created_at AT TIME ZONE 'Asia/Tashkent') = ?
		AND deleted_at IS NULL
		GROUP BY hour
		ORDER BY hour
	`, dateStr).Scan(&rows)

	// 24 soatlik to'liq array (bo'sh soatlar 0 bilan)
	hourly := make([]HourRow, 24)
	for i := 0; i < 24; i++ {
		hourly[i] = HourRow{Hour: i}
	}
	for _, r := range rows {
		if r.Hour >= 0 && r.Hour < 24 {
			hourly[r.Hour] = r
		}
	}

	// Peak soat
	peakHour := 0
	peakSessions := 0
	for _, h := range hourly {
		if h.Sessions > peakSessions {
			peakSessions = h.Sessions
			peakHour = h.Hour
		}
	}

	return utils.Success(c, fiber.Map{
		"date":         dateStr,
		"hourly":       hourly,
		"peak_hour":    peakHour,
		"peak_sessions": peakSessions,
	})
}

// DailyActivity handles GET /admin/analytics/daily?days=30
// Kunlik aktivlik - oxirgi N kun.
func DailyActivity(c *fiber.Ctx) error {
	days := c.QueryInt("days", 30)
	if days > 90 {
		days = 90
	}

	type DayRow struct {
		Date     string `json:"date"`
		Sessions int    `json:"sessions"`
		Users    int    `json:"users"`
		Minutes  int    `json:"minutes"`
		NewUsers int    `json:"new_users"`
	}

	var rows []DayRow
	database.DB.Raw(`
		SELECT
			DATE(s.created_at AT TIME ZONE 'Asia/Tashkent') AS date,
			COUNT(*) AS sessions,
			COUNT(DISTINCT s.user1_id) + COUNT(DISTINCT s.user2_id) AS users,
			COALESCE((SUM(s.duration_seconds) / 60)::bigint, 0) AS minutes
		FROM sessions s
		WHERE s.created_at >= NOW() - INTERVAL '1 day' * ?
		AND s.deleted_at IS NULL AND s.status = 'ended'
		GROUP BY date
		ORDER BY date
	`, days).Scan(&rows)

	// Yangi userlar (kunlik)
	type NewUserRow struct {
		Date  string `json:"date"`
		Count int    `json:"count"`
	}
	var newUsers []NewUserRow
	database.DB.Raw(`
		SELECT
			DATE(created_at AT TIME ZONE 'Asia/Tashkent') AS date,
			COUNT(*) AS count
		FROM users
		WHERE created_at >= NOW() - INTERVAL '1 day' * ?
		AND deleted_at IS NULL
		GROUP BY date
		ORDER BY date
	`, days).Scan(&newUsers)

	nuMap := make(map[string]int)
	for _, nu := range newUsers {
		nuMap[nu.Date] = nu.Count
	}
	for i := range rows {
		rows[i].NewUsers = nuMap[rows[i].Date]
	}

	// Summary
	totalSessions := 0
	totalMinutes := 0
	totalNewUsers := 0
	for _, r := range rows {
		totalSessions += r.Sessions
		totalMinutes += r.Minutes
		totalNewUsers += r.NewUsers
	}

	return utils.Success(c, fiber.Map{
		"days":           days,
		"daily":          rows,
		"total_sessions": totalSessions,
		"total_minutes":  totalMinutes,
		"total_new_users": totalNewUsers,
		"avg_daily_sessions": func() int {
			if len(rows) == 0 {
				return 0
			}
			return totalSessions / len(rows)
		}(),
	})
}

// WeeklyActivity handles GET /admin/analytics/weekly?weeks=12
// Haftalik aktivlik.
func WeeklyActivity(c *fiber.Ctx) error {
	weeks := c.QueryInt("weeks", 12)
	if weeks > 52 {
		weeks = 52
	}

	type WeekRow struct {
		Week     string `json:"week"`
		Sessions int    `json:"sessions"`
		Users    int    `json:"users"`
		Minutes  int    `json:"minutes"`
		NewUsers int    `json:"new_users"`
	}

	var rows []WeekRow
	database.DB.Raw(`
		SELECT
			TO_CHAR(DATE_TRUNC('week', s.created_at AT TIME ZONE 'Asia/Tashkent'), 'YYYY-MM-DD') AS week,
			COUNT(*) AS sessions,
			COUNT(DISTINCT s.user1_id) + COUNT(DISTINCT s.user2_id) AS users,
			COALESCE((SUM(s.duration_seconds) / 60)::bigint, 0) AS minutes
		FROM sessions s
		WHERE s.created_at >= NOW() - INTERVAL '1 week' * ?
		AND s.deleted_at IS NULL AND s.status = 'ended'
		GROUP BY week
		ORDER BY week
	`, weeks).Scan(&rows)

	var newUsers []struct {
		Week  string `json:"week"`
		Count int    `json:"count"`
	}
	database.DB.Raw(`
		SELECT
			TO_CHAR(DATE_TRUNC('week', created_at AT TIME ZONE 'Asia/Tashkent'), 'YYYY-MM-DD') AS week,
			COUNT(*) AS count
		FROM users
		WHERE created_at >= NOW() - INTERVAL '1 week' * ?
		AND deleted_at IS NULL
		GROUP BY week
		ORDER BY week
	`, weeks).Scan(&newUsers)

	nuMap := make(map[string]int)
	for _, nu := range newUsers {
		nuMap[nu.Week] = nu.Count
	}
	for i := range rows {
		rows[i].NewUsers = nuMap[rows[i].Week]
	}

	return utils.Success(c, fiber.Map{
		"weeks":  weeks,
		"weekly": rows,
	})
}

// UserRetention handles GET /admin/analytics/retention
// Nechta user qaytib keladi - cohort analysis.
func UserRetention(c *fiber.Ctx) error {
	type RetentionRow struct {
		Period       string  `json:"period"`
		TotalUsers   int     `json:"total_users"`
		ActiveUsers  int     `json:"active_users"`
		RetentionPct float64 `json:"retention_pct"`
	}

	var results []RetentionRow

	// Bugun
	var todayActive int64
	var totalUsers int64
	database.DB.Raw(`SELECT COUNT(DISTINCT id) FROM users WHERE last_active_at >= NOW() - INTERVAL '1 day' AND deleted_at IS NULL`).Scan(&todayActive)
	database.DB.Raw(`SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`).Scan(&totalUsers)

	if totalUsers > 0 {
		results = append(results, RetentionRow{"today", int(totalUsers), int(todayActive), float64(todayActive) / float64(totalUsers) * 100})
	}

	// Oxirgi 7 kun
	var week7 int64
	database.DB.Raw(`SELECT COUNT(DISTINCT id) FROM users WHERE last_active_at >= NOW() - INTERVAL '7 days' AND deleted_at IS NULL`).Scan(&week7)
	if totalUsers > 0 {
		results = append(results, RetentionRow{"7_days", int(totalUsers), int(week7), float64(week7) / float64(totalUsers) * 100})
	}

	// Oxirgi 30 kun
	var month30 int64
	database.DB.Raw(`SELECT COUNT(DISTINCT id) FROM users WHERE last_active_at >= NOW() - INTERVAL '30 days' AND deleted_at IS NULL`).Scan(&month30)
	if totalUsers > 0 {
		results = append(results, RetentionRow{"30_days", int(totalUsers), int(month30), float64(month30) / float64(totalUsers) * 100})
	}

	return utils.Success(c, fiber.Map{
		"retention":   results,
		"total_users": totalUsers,
	})
}

// TopUsers handles GET /admin/analytics/top-users?period=week&limit=10
// Eng aktiv userlar.
func TopUsers(c *fiber.Ctx) error {
	period := c.Query("period", "week")
	limit := c.QueryInt("limit", 10)
	if limit > 50 {
		limit = 50
	}

	interval := "7 days"
	if period == "month" {
		interval = "30 days"
	} else if period == "all" {
		interval = "10 years"
	}

	type TopUser struct {
		UserID     string `json:"user_id"`
		Name       string `json:"name"`
		Sessions   int    `json:"sessions"`
		Minutes    int    `json:"minutes"`
		Level      string `json:"level"`
		IsPremium  bool   `json:"is_premium"`
	}

	var rows []TopUser
	database.DB.Raw(`
		SELECT
			u.id AS user_id,
			u.first_name AS name,
			COUNT(s.id) AS sessions,
			COALESCE((SUM(s.duration_seconds) / 60)::bigint, 0) AS minutes,
			COALESCE(u.level, '') AS level,
			u.is_premium
		FROM users u
		JOIN sessions s ON (s.user1_id = u.id OR s.user2_id = u.id)
		WHERE s.created_at >= NOW() - INTERVAL '`+interval+`'
		AND s.deleted_at IS NULL AND s.status = 'ended'
		AND u.deleted_at IS NULL
		GROUP BY u.id, u.first_name, u.level, u.is_premium
		ORDER BY sessions DESC
		LIMIT ?
	`, limit).Scan(&rows)

	return utils.Success(c, fiber.Map{
		"period":    period,
		"top_users": rows,
	})
}
