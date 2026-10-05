package handlers

import (
	"github.com/gofiber/fiber/v2"

	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// channelStatus is what the Mini App reads to decide whether to show the
// blocking "subscribe first" screen.
type channelStatus struct {
	Required   bool   `json:"required"`   // is the gate on at all
	Subscribed bool   `json:"subscribed"` // may this user proceed
	Channel    string `json:"channel"`    // without the leading "@"
}

// GetChannelStatus handles GET /users/me/channel-status.
// Cached read - safe to call on every app open.
func GetChannelStatus(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	return utils.Success(c, channelStatus{
		Required:   services.ChannelGateEnabled(),
		Subscribed: services.IsChannelSubscribed(user.TelegramID),
		Channel:    services.RequiredChannelUsername(),
	})
}

// CheckChannelStatus handles POST /users/me/channel-status/check — the
// "I've subscribed, let me in" button. Skips the cache so the user gets an
// answer immediately after joining.
//
// A Telegram failure is reported as its own error code rather than as
// "not subscribed", so the UI can tell the user to retry instead of
// wrongly accusing them of not having joined.
func CheckChannelStatus(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	subscribed, err := services.CheckChannelSubscriptionFresh(user.TelegramID)
	if err != nil {
		return utils.ErrorWithCode(c, fiber.StatusServiceUnavailable,
			"CHECK_FAILED", "Tekshirib bo'lmadi, qayta urinib ko'ring")
	}

	return utils.Success(c, channelStatus{
		Required:   services.ChannelGateEnabled(),
		Subscribed: subscribed,
		Channel:    services.RequiredChannelUsername(),
	})
}
