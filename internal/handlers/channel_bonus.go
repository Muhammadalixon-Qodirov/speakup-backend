package handlers

import (
	"errors"

	"github.com/gofiber/fiber/v2"

	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// GetChannelBonusStatus handles GET /users/me/channel-bonus.
// Frontend polls this to decide whether to render the subscribe card.
func GetChannelBonusStatus(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	return utils.Success(c, services.GetChannelBonusStatus(user.ID))
}

// ClaimChannelBonus handles POST /users/me/channel-bonus/claim.
// Verifies the user is subscribed to @speakup_app then grants +10 min.
// Returns a typed error code so the UI can show the right toast.
func ClaimChannelBonus(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	status, err := services.ClaimChannelBonus(user)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrChannelBonusAlreadyClaimed):
			return utils.ErrorWithCode(c, fiber.StatusConflict,
				"ALREADY_CLAIMED", "Siz allaqachon olgansiz")
		case errors.Is(err, services.ErrNotSubscribed):
			return utils.ErrorWithCode(c, fiber.StatusForbidden,
				"NOT_SUBSCRIBED", "Avval kanalga obuna bo'ling")
		case errors.Is(err, services.ErrChannelCheckFailed):
			return utils.ErrorWithCode(c, fiber.StatusServiceUnavailable,
				"CHECK_FAILED", "Tekshirib bo'lmadi, qayta urinib ko'ring")
		}
		return utils.InternalError(c)
	}

	return utils.Success(c, status)
}
