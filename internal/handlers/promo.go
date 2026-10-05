package handlers

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// RedeemPromoCode handles POST /promo-codes/redeem.
//
// Body: { "code": "MASTERN1" }
//
// Maps service-layer errors to friendly HTTP statuses so the
// frontend can show the right toast message.
func RedeemPromoCode(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := c.BodyParser(&body); err != nil {
		return utils.BadRequest(c, "Invalid body")
	}
	if strings.TrimSpace(body.Code) == "" {
		return utils.BadRequest(c, services.ErrPromoInvalid.Error())
	}

	result, err := services.RedeemPromoCode(user.ID, body.Code)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrPromoNotFound),
			errors.Is(err, services.ErrPromoInactive):
			return utils.Error(c, fiber.StatusNotFound, err.Error())
		case errors.Is(err, services.ErrPromoExpired),
			errors.Is(err, services.ErrPromoExhausted):
			return utils.Error(c, fiber.StatusGone, err.Error())
		case errors.Is(err, services.ErrPromoAlreadyClaimed):
			return utils.Error(c, fiber.StatusConflict, err.Error())
		case errors.Is(err, services.ErrPromoInvalid):
			return utils.BadRequest(c, err.Error())
		default:
			return utils.InternalError(c)
		}
	}

	return utils.Success(c, fiber.Map{
		"premium_days":       result.PremiumDays,
		"premium_expires_at": result.PremiumExpiresAt,
	})
}
