package handlers

import (
	"github.com/gofiber/fiber/v2"

	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

// PartnerAlertsRequest accepts either or both fields. Pointers so the
// caller can update one without overwriting the other (PATCH-style).
type PartnerAlertsRequest struct {
	Enabled    *bool `json:"enabled"`
	PromptSeen *bool `json:"prompt_seen"`
}

// UpdatePartnerAlerts handles PUT /users/me/partner-alerts.
// Updates the opt-in flag for "someone is searching" Telegram pings
// and/or the "we've already shown the prompt" marker.
func UpdatePartnerAlerts(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req PartnerAlertsRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	updates := map[string]interface{}{}
	if req.Enabled != nil {
		updates["partner_alerts_enabled"] = *req.Enabled
		// Accepting or rejecting the offer counts as having seen it -
		// otherwise the modal would re-open the next time the user
		// hits an empty queue.
		updates["partner_alerts_prompt_seen"] = true
	}
	if req.PromptSeen != nil {
		updates["partner_alerts_prompt_seen"] = *req.PromptSeen
	}

	if len(updates) == 0 {
		return utils.BadRequest(c, "Hech narsa o'zgartirilmadi")
	}

	if err := database.DB.Model(&models.User{}).
		Where("id = ?", user.ID).
		Updates(updates).Error; err != nil {
		return utils.InternalError(c)
	}

	// Return the fresh user so the client can update its store atomically.
	var fresh models.User
	if err := database.DB.First(&fresh, "id = ?", user.ID).Error; err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, fiber.Map{
		"partner_alerts_enabled":     fresh.PartnerAlertsEnabled,
		"partner_alerts_prompt_seen": fresh.PartnerAlertsPromptSeen,
	})
}
