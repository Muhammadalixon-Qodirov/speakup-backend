package database

import (
	"time"

	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB is the global database connection - initialized once at startup.
var DB *gorm.DB

// ConnectPostgres opens a connection pool to PostgreSQL.
func ConnectPostgres() {
	cfg := &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	}

	if config.App.Debug {
		cfg.Logger = logger.Default.LogMode(logger.Info)
	}

	db, err := gorm.Open(postgres.Open(config.App.DatabaseURL), cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to PostgreSQL")
	}

	// Connection pool settings.
	//
	// MaxOpen / MaxIdle stay configurable via env. We additionally cap
	// the lifetime of every connection so that:
	//   - Idle connections don't sit forever (was: days-old idle conns
	//     visible in pg_stat_activity, blocking pg restart and slowly
	//     leaking server memory).
	//   - Long-lived connections rotate every 30 min, which lets a
	//     postgres restart / failover heal the pool within one cycle
	//     instead of needing an app restart.
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get underlying SQL DB")
	}
	sqlDB.SetMaxOpenConns(config.App.DatabasePoolSize + config.App.DatabaseMaxOverflow)
	sqlDB.SetMaxIdleConns(config.App.DatabasePoolSize)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	DB = db
	log.Info().Msg("PostgreSQL connected")
}

// AutoMigrate runs GORM auto-migration for all models.
func AutoMigrate(models ...interface{}) {
	if err := DB.AutoMigrate(models...); err != nil {
		log.Fatal().Err(err).Msg("Failed to auto-migrate database")
	}
	log.Info().Msg("Database migrated")
}
