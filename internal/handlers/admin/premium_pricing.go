package admin

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// GetPremiumSettings - GET /admin/premium/settings
//
// Returns the price/card row plus the campaign that is live right now
// and a preview of what a user with no coupons currently pays, so the
// admin form can show the real outcome instead of asking the admin to
// do the percentage in their head.
func GetPremiumSettings(c *fiber.Ctx) error {
	settings, err := services.GetPremiumSettings()
	if err != nil {
		return utils.InternalError(c)
	}
	pricing, err := services.ResolvePremiumPricing("")
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, fiber.Map{
		"settings": settings,
		"pricing":  pricing,
		"themes":   models.CampaignThemes(),
	})
}

// UpdatePremiumSettings - PUT /admin/premium/settings
// Partial body; any omitted field keeps its current value.
func UpdatePremiumSettings(c *fiber.Ctx) error {
	var body struct {
		BasePriceUZS  *int    `json:"base_price_uzs"`
		CardNumber    *string `json:"card_number"`
		CardOwner     *string `json:"card_owner"`
		AdminUsername *string `json:"admin_username"`
	}
	if err := c.BodyParser(&body); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}

	settings, err := services.UpdatePremiumSettings(
		body.BasePriceUZS,
		body.CardNumber,
		body.CardOwner,
		body.AdminUsername,
		middleware.GetCurrentUser(c),
	)
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}
	return utils.Success(c, settings)
}

// ListPremiumCampaigns - GET /admin/premium/campaigns
func ListPremiumCampaigns(c *fiber.Ctx) error {
	rows, err := services.ListPremiumCampaigns()
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, rows)
}

// CreatePremiumCampaign - POST /admin/premium/campaigns
//
// Body:
//
//	{
//	  "title": "Navro'z sovg'asi",
//	  "reason": "Bahor bayrami munosabati bilan",
//	  "emoji": "🌱",
//	  "theme": "holiday",
//	  "discount_percent": 30,
//	  "starts_at": null,                      // null = darhol
//	  "ends_at": "2026-03-22T00:00:00Z",      // null = admin o'chirguncha
//	  "is_active": true,
//	  "stacks_with_coupons": false
//	}
func CreatePremiumCampaign(c *fiber.Ctx) error {
	var in services.CampaignInput
	if err := c.BodyParser(&in); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}
	campaign, err := services.CreatePremiumCampaign(in, middleware.GetCurrentUser(c))
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}
	return utils.Created(c, campaign)
}

// UpdatePremiumCampaign - PUT /admin/premium/campaigns/:id
// Partial body, same shape as create. `clear_starts_at` /
// `clear_ends_at` drop a date back to "no boundary".
func UpdatePremiumCampaign(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid id")
	}
	var in services.CampaignInput
	if err := c.BodyParser(&in); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}
	campaign, err := services.UpdatePremiumCampaign(id, in)
	if err != nil {
		if err == services.ErrCampaignNotFnd {
			return utils.NotFound(c, err.Error())
		}
		return utils.BadRequest(c, err.Error())
	}
	return utils.Success(c, campaign)
}

// DeletePremiumCampaign - DELETE /admin/premium/campaigns/:id
// Soft-delete, so a finished campaign can still be looked up from a
// grant that references the price it produced.
func DeletePremiumCampaign(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid id")
	}
	if err := services.DeletePremiumCampaign(id); err != nil {
		return utils.NotFound(c, err.Error())
	}
	return utils.SuccessMessage(c, "deleted")
}
