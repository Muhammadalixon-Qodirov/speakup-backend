package middleware

import (
	"crypto/subtle"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"
)

// TelegramWebhookAuth validates the X-Telegram-Bot-Api-Secret-Token
// header that Telegram sends with every webhook request when a
// secret_token was provided via setWebhook. If the expected secret is
// empty (not configured yet), the middleware logs a warning and allows
// the request through for backward compatibility.
func TelegramWebhookAuth(expectedSecret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if expectedSecret == "" {
			// Fail CLOSED. An unauthenticated /bot/webhook lets anyone
			// forge a Telegram update, and the bot's auth handler trusts
			// the sender ID in that payload to complete a web login -
			// i.e. account takeover from nothing but a Telegram ID.
			//
			// This used to `return c.Next()`. Nothing broke, so the
			// missing secret stayed unnoticed for weeks. Config
			// validation now refuses to boot production without it
			// (see config.validate); this is the second line of defence
			// for any environment that slips past.
			log.Error().Str("ip", c.IP()).
				Msg("webhook: TELEGRAM_WEBHOOK_SECRET not configured - refusing request")
			return c.SendStatus(503)
		}

		received := c.Get("X-Telegram-Bot-Api-Secret-Token")
		if received == "" {
			log.Warn().Str("ip", c.IP()).Msg("webhook: missing secret header")
			return c.SendStatus(401)
		}

		// Constant-time comparison to prevent timing attacks.
		if subtle.ConstantTimeCompare([]byte(expectedSecret), []byte(received)) != 1 {
			log.Warn().Str("ip", c.IP()).Msg("webhook: invalid secret")
			return c.SendStatus(401)
		}

		return c.Next()
	}
}
