package handlers

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// SumPrizeBonusMinutes returns minutes from prize boxes the user opened
// inside the rolling 24 h window - used by the daily-limit calculator
// to add temporary bonus minutes on top of the base allowance. Computed
// in a single SUM() query so a power user with many opened prizes
// doesn't drag a Find() of all rows into Go memory just to count them.
func SumPrizeBonusMinutes(userID string) int {
	cutoff := time.Now().Add(-24 * time.Hour)
	var total int
	database.DB.Model(&models.Prize{}).
		Where("user_id = ? AND status = ? AND prize_type = ? AND opened_at >= ?",
			userID, "opened", "minutes", cutoff).
		Select("COALESCE(SUM(prize_value), 0)").
		Scan(&total)
	return total
}

// GetTodayPrize handles GET /users/me/prizes/today - returns the
// pending prize box ready to open today, or 204 if none.
func GetTodayPrize(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	// Make sure this user has prizes generated for the current month.
	_ = services.EnsurePrizesForMonth(user.ID.String())

	prize, err := services.GetTodayPrize(user.ID.String())
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, prize) // nil → JSON null
}

// ListMyPrizes handles GET /users/me/prizes - returns the user's prizes
// for the current month (past + present + future).
func ListMyPrizes(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	_ = services.EnsurePrizesForMonth(user.ID.String())

	prizes, err := services.ListUserPrizes(user.ID.String())
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, prizes)
}

// PrizeWinner is one row of the public "recent winners" ticker - real,
// recently-opened prize boxes. Only the first name is exposed (the
// leaderboard already shows names publicly, so this leaks nothing more)
// and the prize itself; never the user id or anything sensitive.
type PrizeWinner struct {
	Name       string    `json:"name"`
	PrizeType  string    `json:"prize_type"` // minutes | discount | premium
	PrizeValue int       `json:"prize_value"`
	OpenedAt   time.Time `json:"opened_at"`
}

// RecentPrizeWinners handles GET /prizes/recent - the last ~30 opened
// prize boxes, newest first, for the home-page winners ticker. This is
// genuine social proof (real wins), not fabricated, so users can trust
// the prize wheel actually pays out.
func RecentPrizeWinners(c *fiber.Ctx) error {
	type row struct {
		FirstName  string
		PrizeType  string
		PrizeValue int
		OpenedAt   time.Time
	}
	var rows []row
	database.DB.
		Table("prizes").
		Select("users.first_name, prizes.prize_type, prizes.prize_value, prizes.opened_at").
		Joins("JOIN users ON users.id = prizes.user_id").
		Where("prizes.status = ? AND prizes.prize_type IS NOT NULL AND prizes.opened_at IS NOT NULL", "opened").
		Where("users.is_banned = ? AND users.deleted_at IS NULL", false).
		Order("prizes.opened_at DESC").
		Limit(30).
		Scan(&rows)

	out := make([]PrizeWinner, 0, len(rows))
	for _, r := range rows {
		if r.FirstName == "" || r.PrizeValue <= 0 {
			continue
		}
		out = append(out, PrizeWinner{
			Name:       r.FirstName,
			PrizeType:  r.PrizeType,
			PrizeValue: r.PrizeValue,
			OpenedAt:   r.OpenedAt,
		})
	}
	return utils.Success(c, out)
}

// GetActiveDiscount handles GET /users/me/active-discount.
// Returns a summary of the user's stacked unused discount coupons:
// { total_percent, count, coupons }. Drives the discounted-price preview.
func GetActiveDiscount(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	summary, err := services.GetActiveDiscounts(user.ID.String())
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, summary)
}

// OpenPrize handles POST /users/me/prizes/:id/open - spins, applies the
// reward and returns the outcome.
func OpenPrize(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	id := c.Params("id")
	out, err := services.OpenPrize(id, user.ID.String())
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}

	return utils.Success(c, fiber.Map{
		"prize_type":  out.Type,
		"prize_value": out.Value,
	})
}

// GetStreakCalendar handles GET /users/me/streak-calendar?year=YYYY&month=MM
func GetStreakCalendar(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	now := time.Now()
	year := c.QueryInt("year", now.Year())
	month := c.QueryInt("month", int(now.Month()))
	if month < 1 || month > 12 {
		month = int(now.Month())
	}

	days, err := services.GetStreakCalendar(user.ID.String(), year, month)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, fiber.Map{
		"year":  year,
		"month": month,
		"days":  days,
	})
}
