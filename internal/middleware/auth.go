package middleware

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// AuthRequired is a Fiber middleware that:
// 1. Extracts Bearer token from Authorization header
// 2. Verifies JWT token
// 3. Loads user from DB
// 4. Puts user into c.Locals("user") for handlers to use
func AuthRequired() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Extract token from "Authorization: Bearer <token>"
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return utils.Unauthorized(c, "Avtorizatsiya sarlavhasi topilmadi")
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			return utils.Unauthorized(c, "Avtorizatsiya formati noto'g'ri")
		}
		token := parts[1]

		// Verify JWT + extract auth source
		userID, authSource, err := services.DecodeJWTWithSource(token)
		if err != nil {
			return utils.Unauthorized(c, "Token noto'g'ri yoki muddati tugagan")
		}

		// Load user from DB
		uid, err := uuid.Parse(userID)
		if err != nil {
			return utils.Unauthorized(c, "Token noto'g'ri")
		}

		var user models.User
		if err := database.DB.First(&user, "id = ?", uid).Error; err != nil {
			return utils.Unauthorized(c, "Foydalanuvchi topilmadi")
		}

		if user.IsBanned {
			return utils.Forbidden(c, "Akkaunt bloklangan")
		}

		// Touch last_active_at - actual streak counting now lives in
		// services/streak_service.go and only fires when a user crosses
		// the daily speaking threshold.
		touchLastActive(&user)

		// Lazy premium-expiry check. Cron handles the bulk daily sweep,
		// but a user opening the app between sweeps should already see
		// premium = false the moment their window passes.
		expirePremiumIfDue(&user)

		// Store user + auth source in context.
		c.Locals("user", &user)
		c.Locals("auth_source", authSource)
		return c.Next()
	}
}

// GetAuthSource returns the `src` JWT claim - "miniapp" | "web" | "".
func GetAuthSource(c *fiber.Ctx) string {
	src, _ := c.Locals("auth_source").(string)
	return src
}

// touchLastActive bumps `last_active_at` once per Tashkent calendar day.
// The actual streak counter is updated by services.RecordStreakIfQualified
// from the session-end path.
func touchLastActive(user *models.User) {
	now := time.Now()
	loc := time.FixedZone("UZT", 5*60*60)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	if user.LastActiveAt != nil && !user.LastActiveAt.Before(today) {
		return
	}
	user.LastActiveAt = &now
	database.DB.Model(user).Update("last_active_at", now)
}

// expirePremiumIfDue flips is_premium → false the moment the expiry
// window has passed. Hot path optimisation: the previous implementation
// ran an UPDATE on every authenticated request once the user expired
// (the in-memory `IsPremium` was reset but `WHERE is_premium = true`
// matched again next request). We now gate the UPDATE behind a Redis
// SET NX so each user gets at most one expiry write per hour, even if
// they make hundreds of requests in that window. The daily cron is the
// authoritative cleanup; this is just a freshness boost.
func expirePremiumIfDue(user *models.User) {
	if !user.IsPremium {
		return
	}
	if user.PremiumExpiresAt == nil {
		return
	}
	if !user.PremiumExpiresAt.Before(time.Now()) {
		return
	}
	// Update the in-memory user immediately so the response carries the
	// fresh value even when the DB write is throttled away.
	user.IsPremium = false

	if database.Redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	key := "premium_expire_check:" + user.ID.String()
	ok, err := database.Redis.SetNX(ctx, key, "1", time.Hour).Result()
	if err != nil || !ok {
		// Either Redis is down (fail open) or another request already
		// did the write inside the hour throttle window.
		return
	}
	database.DB.Model(&models.User{}).
		Where("id = ? AND is_premium = true AND premium_expires_at < ?", user.ID, time.Now()).
		Updates(map[string]interface{}{
			"is_premium":         false,
			"premium_expires_at": nil,
		})
}

// GetCurrentUser extracts the authenticated user from Fiber context.
// Use this in handlers: user := middleware.GetCurrentUser(c)
func GetCurrentUser(c *fiber.Ctx) *models.User {
	user, ok := c.Locals("user").(*models.User)
	if !ok {
		return nil
	}
	return user
}
