package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/utils"
)

// ListPrizes handles GET /admin/prizes?page=1&limit=20&type=&status=
// Returns opened (and optionally pending/expired) prize boxes with the
// winning user attached, so admins can audit the prize history - who
// won what, when.
func ListPrizes(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	prizeType := c.Query("type")       // minutes | discount | premium
	status := c.Query("status", "opened") // opened (default) | pending | expired | all

	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	type prizeRow struct {
		ID          uuid.UUID  `json:"id"`
		UserID      uuid.UUID  `json:"user_id"`
		AvailableOn time.Time  `json:"available_on"`
		Status      string     `json:"status"`
		PrizeType   *string    `json:"prize_type"`
		PrizeValue  *int       `json:"prize_value"`
		OpenedAt    *time.Time `json:"opened_at"`
		CreatedAt   time.Time  `json:"created_at"`
		FirstName   string     `json:"first_name"`
		LastName    *string    `json:"last_name"`
		Username    *string    `json:"username"`
		TelegramID  int64      `json:"telegram_id"`
		PhotoURL    *string    `json:"photo_url"`
	}

	q := database.DB.Table("prizes").
		Select(`
			prizes.id, prizes.user_id, prizes.available_on, prizes.status,
			prizes.prize_type, prizes.prize_value, prizes.opened_at, prizes.created_at,
			users.first_name, users.last_name, users.username, users.telegram_id, users.photo_url
		`).
		Joins("LEFT JOIN users ON users.id = prizes.user_id").
		Where("prizes.deleted_at IS NULL")

	if status != "all" {
		q = q.Where("prizes.status = ?", status)
	}
	if prizeType != "" {
		q = q.Where("prizes.prize_type = ?", prizeType)
	}

	countQ := database.DB.Table("prizes").Where("deleted_at IS NULL")
	if status != "all" {
		countQ = countQ.Where("status = ?", status)
	}
	if prizeType != "" {
		countQ = countQ.Where("prize_type = ?", prizeType)
	}
	var total int64
	countQ.Count(&total)

	var rows []prizeRow
	q.Order("COALESCE(prizes.opened_at, prizes.available_on) DESC").
		Offset(offset).
		Limit(limit).
		Scan(&rows)

	// Aggregate totals for a small summary card on the frontend.
	type typeCount struct {
		PrizeType  string `json:"prize_type"`
		Count      int64  `json:"count"`
		TotalValue int64  `json:"total_value"`
	}
	var byType []typeCount
	database.DB.Table("prizes").
		Select("prize_type, COUNT(*) as count, COALESCE(SUM(prize_value), 0) as total_value").
		Where("status = ? AND deleted_at IS NULL", "opened").
		Group("prize_type").
		Scan(&byType)

	return utils.Success(c, fiber.Map{
		"prizes":  rows,
		"by_type": byType,
		"pagination": fiber.Map{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"total_pages": (total + int64(limit) - 1) / int64(limit),
		},
	})
}
