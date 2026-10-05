package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/utils"
)

// AdminRequired checks that the authenticated user has admin privileges.
// Must be used AFTER AuthRequired middleware.
func AdminRequired() fiber.Handler {
	return func(c *fiber.Ctx) error {
		user := GetCurrentUser(c)
		if user == nil {
			return utils.Unauthorized(c, "Not authenticated")
		}
		if !user.IsAdmin {
			return utils.Forbidden(c, "Admin access required")
		}
		return c.Next()
	}
}
