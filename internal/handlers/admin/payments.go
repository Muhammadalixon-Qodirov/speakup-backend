package admin

import (
	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

// ListPayments handles GET /admin/payments?page=1&limit=20&status=&provider=
func ListPayments(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	status := c.Query("status")     // pending, paid, failed
	provider := c.Query("provider") // payme, click

	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	query := database.DB.Model(&models.Payment{}).Preload("User")

	if status != "" {
		query = query.Where("status = ?", status)
	}
	if provider != "" {
		query = query.Where("provider = ?", provider)
	}

	var total int64
	query.Count(&total)

	var payments []models.Payment
	query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&payments)

	// Revenue stats
	var totalRevenue struct{ Total int64 }
	database.DB.Model(&models.Payment{}).
		Where("status = ?", "paid").
		Select("COALESCE(SUM(amount_tiyin), 0) as total").
		Scan(&totalRevenue)

	return utils.Success(c, fiber.Map{
		"payments": payments,
		"revenue": fiber.Map{
			"total_tiyin": totalRevenue.Total,
			"total_uzs":   totalRevenue.Total / 100,
		},
		"pagination": fiber.Map{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"total_pages": (total + int64(limit) - 1) / int64(limit),
		},
	})
}
