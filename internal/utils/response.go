package utils

import "github.com/gofiber/fiber/v2"

func Success(c *fiber.Ctx, data interface{}) error {
	return c.JSON(fiber.Map{
		"success": true,
		"data":    data,
	})
}

func SuccessMessage(c *fiber.Ctx, message string) error {
	return c.JSON(fiber.Map{
		"success": true,
		"message": message,
	})
}

func Created(c *fiber.Ctx, data interface{}) error {
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    data,
	})
}

func Error(c *fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{
		"success": false,
		"error":   message,
	})
}

// ErrorWithCode is Error + a stable machine-readable "code" field so the
// frontend can branch on specific error types (e.g. show a "bot blocked"
// modal instead of a generic toast). The code is part of the API
// contract - pick short SNAKE_CASE slugs and don't rename them casually.
func ErrorWithCode(c *fiber.Ctx, status int, code, message string) error {
	return c.Status(status).JSON(fiber.Map{
		"success": false,
		"code":    code,
		"error":   message,
	})
}

func BadRequest(c *fiber.Ctx, message string) error {
	return Error(c, fiber.StatusBadRequest, message)
}

func Unauthorized(c *fiber.Ctx, message string) error {
	return Error(c, fiber.StatusUnauthorized, message)
}

func Forbidden(c *fiber.Ctx, message string) error {
	return Error(c, fiber.StatusForbidden, message)
}

func NotFound(c *fiber.Ctx, message string) error {
	return Error(c, fiber.StatusNotFound, message)
}

func InternalError(c *fiber.Ctx) error {
	return Error(c, fiber.StatusInternalServerError, "Server xatosi")
}
