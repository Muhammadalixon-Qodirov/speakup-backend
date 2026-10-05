package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
	"github.com/speak-up/backend/internal/ws"
)

// Dashboard handles GET /admin/dashboard
// Returns overall platform statistics.
func Dashboard(c *fiber.Ctx) error {
	var totalUsers, activeUsers, bannedUsers, premiumUsers int64
	var totalSessions, activeSessions int64
	var totalPayments, paidPayments int64
	var totalMinutes int64

	// User stats
	database.DB.Model(&models.User{}).Count(&totalUsers)
	database.DB.Model(&models.User{}).Where("is_active = ? AND is_banned = ?", true, false).Count(&activeUsers)
	database.DB.Model(&models.User{}).Where("is_banned = ?", true).Count(&bannedUsers)
	database.DB.Model(&models.User{}).Where("is_premium = ?", true).Count(&premiumUsers)

	// Session stats
	database.DB.Model(&models.Session{}).Count(&totalSessions)
	database.DB.Model(&models.Session{}).Where("status = ?", "active").Count(&activeSessions)

	// Total speaking minutes
	var result struct{ Total int64 }
	database.DB.Model(&models.User{}).Select("COALESCE(SUM(total_minutes), 0) as total").Scan(&result)
	totalMinutes = result.Total

	// Payment stats
	database.DB.Model(&models.Payment{}).Count(&totalPayments)
	database.DB.Model(&models.Payment{}).Where("status = ?", "paid").Count(&paidPayments)

	// Today's stats
	today := time.Now().Truncate(24 * time.Hour)
	var newUsersToday, sessionsToday int64
	database.DB.Model(&models.User{}).Where("created_at >= ?", today).Count(&newUsersToday)
	database.DB.Model(&models.Session{}).Where("created_at >= ?", today).Count(&sessionsToday)

	return utils.Success(c, fiber.Map{
		"users": fiber.Map{
			"total":    totalUsers,
			"active":   activeUsers,
			"banned":   bannedUsers,
			"premium":  premiumUsers,
			"online":   ws.H.OnlineCount(),
			"today":    newUsersToday,
		},
		"sessions": fiber.Map{
			"total":         totalSessions,
			"active":        activeSessions,
			"today":         sessionsToday,
			"total_minutes":  totalMinutes,
		},
		"payments": fiber.Map{
			"total": totalPayments,
			"paid":  paidPayments,
		},
	})
}
