package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// ListPromoCodes - GET /admin/promo-codes
// Returns every promo code (newest first) for the admin table.
func ListPromoCodes(c *fiber.Ctx) error {
	codes, err := services.ListPromoCodes()
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, codes)
}

// CreatePromoCode - POST /admin/promo-codes
//
// Body:
//   {
//     "code": "MASTERN1",
//     "premium_days": 30,
//     "max_uses": 0,           // 0 = unlimited
//     "expires_at": "2026-12-31T23:59:59Z" // null = never
//     "is_active": true
//   }
func CreatePromoCode(c *fiber.Ctx) error {
	admin := middleware.GetCurrentUser(c)

	var body struct {
		Code        string     `json:"code"`
		PremiumDays int        `json:"premium_days"`
		MaxUses     int        `json:"max_uses"`
		ExpiresAt   *time.Time `json:"expires_at"`
		IsActive    *bool      `json:"is_active"`
	}
	if err := c.BodyParser(&body); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}
	if body.PremiumDays == 0 {
		body.PremiumDays = 30
	}
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}
	var createdBy *uuid.UUID
	if admin != nil {
		id := admin.ID
		createdBy = &id
	}

	promo, err := services.CreatePromoCode(
		body.Code,
		body.PremiumDays,
		body.MaxUses,
		body.ExpiresAt,
		active,
		createdBy,
	)
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}
	return utils.Created(c, promo)
}

// UpdatePromoCode - PUT /admin/promo-codes/:id
//
// Accepts a partial body - any non-nil field is patched. Use this
// to disable a code, change its day count, or extend its expiry.
func UpdatePromoCode(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid id")
	}

	var body struct {
		Code        *string    `json:"code"`
		PremiumDays *int       `json:"premium_days"`
		MaxUses     *int       `json:"max_uses"`
		ExpiresAt   *time.Time `json:"expires_at"`
		ClearExpiry bool       `json:"clear_expiry"`
		IsActive    *bool      `json:"is_active"`
	}
	if err := c.BodyParser(&body); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}

	updates := map[string]interface{}{}
	if body.Code != nil {
		updates["code"] = *body.Code
	}
	if body.PremiumDays != nil {
		if *body.PremiumDays < 1 || *body.PremiumDays > 3650 {
			return utils.BadRequest(c, "premium_days 1..3650 oralig'ida bo'lishi kerak")
		}
		updates["premium_days"] = *body.PremiumDays
	}
	if body.MaxUses != nil {
		if *body.MaxUses < 0 {
			return utils.BadRequest(c, "max_uses manfiy bo'la olmaydi")
		}
		updates["max_uses"] = *body.MaxUses
	}
	if body.ClearExpiry {
		updates["expires_at"] = nil
	} else if body.ExpiresAt != nil {
		updates["expires_at"] = *body.ExpiresAt
	}
	if body.IsActive != nil {
		updates["is_active"] = *body.IsActive
	}
	if len(updates) == 0 {
		return utils.BadRequest(c, "Nothing to update")
	}

	updated, err := services.UpdatePromoCode(id, updates)
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}
	return utils.Success(c, updated)
}

// DeletePromoCode - DELETE /admin/promo-codes/:id
// Soft-delete; redemptions remain so historical reports keep working.
func DeletePromoCode(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid id")
	}
	if err := services.DeletePromoCode(id); err != nil {
		return utils.NotFound(c, "Promo code not found")
	}
	return utils.SuccessMessage(c, "deleted")
}

// ListPromoRedemptions - GET /admin/promo-codes/:id/redemptions
//
// Lists who used a specific code (with the user preloaded) for the
// detail drawer.
func ListPromoRedemptions(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid id")
	}
	rows, err := services.ListRedemptionsForCode(id, c.QueryInt("limit", 100))
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, rows)
}
