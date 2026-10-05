package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
	"gorm.io/gorm"
)

// The premium ledger: every hand-granted subscription with its reason.
//
// The list and the totals are computed over the SAME filter, so the
// numbers at the top of the page always describe the rows underneath -
// a stats block that silently ignores the active filter is worse than
// no stats at all.

type grantKindStat struct {
	Kind      string `json:"kind"`
	Count     int64  `json:"count"`
	AmountUZS int64  `json:"amount_uzs"`
	Months    int64  `json:"months"`
}

// ListPremiumGrants handles
// GET /admin/premium-grants?kind=&user_id=&days=&limit=&offset=
func ListPremiumGrants(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := c.QueryInt("offset", 0)
	if offset < 0 {
		offset = 0
	}

	q := database.DB.Model(&models.PremiumGrant{})

	if kind := c.Query("kind"); kind != "" && kind != "all" {
		if !models.ValidGrantKind(kind) {
			return utils.BadRequest(c, "kind noto'g'ri")
		}
		q = q.Where("kind = ?", kind)
	}

	if rawUser := c.Query("user_id"); rawUser != "" {
		uid, err := uuid.Parse(rawUser)
		if err != nil {
			return utils.BadRequest(c, "user_id noto'g'ri")
		}
		q = q.Where("user_id = ?", uid)
	}

	// days=0 (or absent) means "all time" - the default for a ledger is
	// everything, not an arbitrary recent slice.
	if days := c.QueryInt("days", 0); days > 0 {
		q = q.Where("created_at >= ?", time.Now().AddDate(0, 0, -days))
	}

	var total int64
	q.Count(&total)

	// Totals per kind over the filtered set. Revenue is deliberately the
	// sum of the "sale" row only - a free grant has no amount and must
	// never inflate the month.
	stats := make([]grantKindStat, 0)
	q.Session(&gorm.Session{}).
		Select("kind, COUNT(*) as count, COALESCE(SUM(amount_uzs),0) as amount_uzs, COALESCE(SUM(months),0) as months").
		Group("kind").
		Scan(&stats)

	var revenue int64
	var salesCount, freeCount int64
	for _, s := range stats {
		if models.IsRevenue(s.Kind) {
			revenue += s.AmountUZS
			salesCount += s.Count
			continue
		}
		freeCount += s.Count
	}

	grants := make([]models.PremiumGrant, 0)
	q.Session(&gorm.Session{}).
		Preload("User").
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&grants)

	// This calendar month is the number an admin actually asks for, and
	// it stays unfiltered on purpose: "how are we doing this month" must
	// not change when someone clicks a kind chip.
	monthStart := time.Now()
	monthStart = time.Date(monthStart.Year(), monthStart.Month(), 1, 0, 0, 0, 0, monthStart.Location())
	var monthRevenue struct {
		Amount int64
		Count  int64
	}
	database.DB.Model(&models.PremiumGrant{}).
		Where("kind = ? AND created_at >= ?", models.KindSale, monthStart).
		Select("COALESCE(SUM(amount_uzs),0) as amount, COUNT(*) as count").
		Scan(&monthRevenue)

	return utils.Success(c, fiber.Map{
		"grants": grants,
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"stats": fiber.Map{
			"revenue_uzs":       revenue,
			"sales_count":       salesCount,
			"free_count":        freeCount,
			"by_kind":           stats,
			"month_revenue_uzs": monthRevenue.Amount,
			"month_sales_count": monthRevenue.Count,
		},
	})
}
