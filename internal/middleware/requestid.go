package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RequestID adds a unique request ID to every request for tracing.
// Accessible via c.Locals("request_id") and X-Request-ID response header.
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		reqID := c.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		c.Locals("request_id", reqID)
		c.Set("X-Request-ID", reqID)
		return c.Next()
	}
}
