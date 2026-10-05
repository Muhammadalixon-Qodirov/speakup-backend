package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// GetPremiumPricing handles GET /premium/pricing.
//
// One call answers everything the purchase sheet needs: the price, the
// live campaign and its reason, the user's own coupons, and where to
// send the money. The nil-user branch is defensive only - the route
// sits behind AuthRequired - but it keeps the endpoint usable if it is
// ever moved to a public group.
func GetPremiumPricing(c *fiber.Ctx) error {
	userID := ""
	if user := middleware.GetCurrentUser(c); user != nil {
		userID = user.ID.String()
	}

	pricing, err := services.ResolvePremiumPricing(userID)
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, pricing)
}
