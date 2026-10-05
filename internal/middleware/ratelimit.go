package middleware

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/utils"
)

// RateLimitMode controls behaviour when Redis is unavailable.
type RateLimitMode int

const (
	FailOpen   RateLimitMode = iota // Redis down → allow (default)
	FailClosed                       // Redis down → block (auth, payment)
)

// Lua script: atomic INCR + EXPIRE in a single Redis round-trip.
// KEYS[1] = rate limit key, ARGV[1] = window (seconds).
// Returns current count.
const rateLimitLuaScript = `
local current = redis.call('INCR', KEYS[1])
if current == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[1])
end
return current
`

var (
	rateLimitScript *redis.Script
	scriptOnce      sync.Once
)

func getRateLimitScript() *redis.Script {
	scriptOnce.Do(func() {
		rateLimitScript = redis.NewScript(rateLimitLuaScript)
	})
	return rateLimitScript
}

// RateLimit limits requests per user using Redis.
// maxRequests per windowSeconds. The bucket key is shared across the
// route group so this is the right middleware for global / per-API
// throttling.
func RateLimit(maxRequests int, windowSeconds int) fiber.Handler {
	return rateLimitWithKey("rate_limit", maxRequests, windowSeconds, FailOpen)
}

// RouteRateLimit returns a per-route rate limiter - different routes
// get their own Redis bucket so a burst on one expensive endpoint
// (e.g. OpenPrize) doesn't eat into the global allowance for the same
// user. The `name` should be a short, stable identifier for the route.
func RouteRateLimit(name string, maxRequests int, windowSeconds int) fiber.Handler {
	return rateLimitWithKey("rate_limit:"+name, maxRequests, windowSeconds, FailOpen)
}

// RouteRateLimitClosed is like RouteRateLimit but uses FailClosed mode -
// if Redis is down, the endpoint returns 503 instead of allowing through.
// Use for auth / OTP / payment endpoints where bypass = brute-force risk.
func RouteRateLimitClosed(name string, maxRequests int, windowSeconds int) fiber.Handler {
	return rateLimitWithKey("rate_limit:"+name, maxRequests, windowSeconds, FailClosed)
}

func rateLimitWithKey(prefix string, maxRequests int, windowSeconds int, mode RateLimitMode) fiber.Handler {
	script := getRateLimitScript()

	return func(c *fiber.Ctx) error {
		// Authenticated requests bucket per user_id; unauthenticated
		// fall back to the source IP. Telegram Mini App requests come
		// through the bot proxy so per-user is the only meaningful key.
		identifier := c.IP()
		if user := GetCurrentUser(c); user != nil {
			identifier = user.ID.String()
		}

		key := fmt.Sprintf("%s:%s", prefix, identifier)
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		// Atomic INCR + EXPIRE via Lua (eliminates the race where the
		// TTL never gets set on a concurrent first request).
		result, err := script.Run(ctx, database.Redis, []string{key}, windowSeconds).Result()
		if err != nil {
			log.Debug().Err(err).Str("path", c.Path()).Msg("rate limit: Redis error")
			if mode == FailClosed {
				return utils.Error(c, fiber.StatusServiceUnavailable, "Service temporarily unavailable")
			}
			return c.Next()
		}

		count, ok := result.(int64)
		if !ok {
			if mode == FailClosed {
				return utils.Error(c, fiber.StatusServiceUnavailable, "Service temporarily unavailable")
			}
			return c.Next()
		}

		// Informational headers so the frontend (and developers) can
		// see where they stand.
		c.Set("X-RateLimit-Limit", strconv.Itoa(maxRequests))
		remaining := maxRequests - int(count)
		if remaining < 0 {
			remaining = 0
		}
		c.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

		if count > int64(maxRequests) {
			c.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Duration(windowSeconds)*time.Second).Unix(), 10))
			return utils.Error(c, fiber.StatusTooManyRequests, "Too many requests, try again later")
		}

		return c.Next()
	}
}
