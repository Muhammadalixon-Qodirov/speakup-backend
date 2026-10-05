package handlers

import (
	"github.com/gofiber/fiber/v2"

	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
	"github.com/speak-up/backend/internal/ws"
)

// GetOnlineStats handles GET /users/online-stats.
// Returns the current online + queue counts for the home / speak page
// "🟢 N online" badge. Frontend gets the initial number from REST,
// then live updates via the WS partner_searching events.
func GetOnlineStats(c *fiber.Ctx) error {
	online := 0
	if ws.H != nil {
		online = ws.H.OnlineUserCount()
	}
	queue := services.QueueSize()
	return utils.Success(c, fiber.Map{
		"online":   online,
		"in_queue": queue,
	})
}
