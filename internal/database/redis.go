package database

import (
	"context"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
)

// Redis is the global Redis client - initialized once at startup.
var Redis *redis.Client

// ConnectRedis opens a connection to Redis.
func ConnectRedis() {
	opt, err := redis.ParseURL(config.App.RedisURL)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid REDIS_URL")
	}

	Redis = redis.NewClient(opt)

	// Ping to verify connection
	ctx := context.Background()
	if err := Redis.Ping(ctx).Err(); err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to Redis")
	}

	log.Info().Msg("Redis connected")
}
