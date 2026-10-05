// broadcast is a one-off tool that fans out the Uzbekhub announcement
// to every reachable SpeakUp user via the existing Telegram bot, then
// arms a Redis pause flag (bot:broadcast_pause) so the next hour of
// scheduler-driven reminders are muted by SendWithRetry. The flag
// expires automatically — no second pass needed to "unmute".
//
// Usage:
//
//	broadcast --photo /path/to/uzbekhub.png --mode test
//	broadcast --photo /path/to/uzbekhub.png --mode broadcast --pause 1h
//
// Modes:
//
//	test       → send only to ADMIN_ALERT_CHAT_IDS, do NOT arm pause
//	broadcast  → send to every reachable user, arm Redis pause
package main

import (
	"context"
	"flag"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	tele "gopkg.in/telebot.v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	// Caption is HTML-formatted: the whole body is bold, and the
	// "Uzbekhub - yangi avlod messenjeri" fragment is a hyperlink to the
	// Uzbekhub Telegram channel.
	captionHTML = `<b>Barcha kerakli funksiyalar endi bitta zamonaviy milliy ilovada jamlandi! <a href="https://t.me/uzbekhub_official">Uzbekhub - yangi avlod messenjeri</a></b>`

	// BroadcastPauseKey mirrors bot.BroadcastPauseKey — we don't import
	// the bot package because this tool runs without the full bot init.
	BroadcastPauseKey = "bot:broadcast_pause"
)

type userRow struct {
	TelegramID int64
}

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
		With().Timestamp().Logger()

	var (
		photoPath = flag.String("photo", "", "absolute path to the image to send")
		mode      = flag.String("mode", "test", "test | broadcast")
		pauseStr  = flag.String("pause", "1h", "duration of the Redis broadcast pause after a full broadcast")
	)
	flag.Parse()

	if *photoPath == "" {
		log.Fatal().Msg("--photo is required")
	}
	if _, err := os.Stat(*photoPath); err != nil {
		log.Fatal().Err(err).Msg("photo path not readable")
	}

	pauseDur, err := time.ParseDuration(*pauseStr)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid --pause duration")
	}

	// .env is optional — env vars may already be exported by the
	// surrounding container/shell.
	_ = godotenv.Load()

	botToken := mustEnv("BOT_TOKEN")
	dbURL := mustEnv("DATABASE_URL")
	redisURL := mustEnv("REDIS_URL")

	// ── Bot
	b, err := tele.NewBot(tele.Settings{Token: botToken, Verbose: false})
	if err != nil {
		log.Fatal().Err(err).Msg("bot init failed")
	}

	// ── Recipients
	var targets []int64
	switch *mode {
	case "test":
		targets = parseAdminIDs(os.Getenv("ADMIN_ALERT_CHAT_IDS"))
		if len(targets) == 0 {
			log.Fatal().Msg("ADMIN_ALERT_CHAT_IDS is empty — nothing to test against")
		}
	case "broadcast":
		db, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{})
		if err != nil {
			log.Fatal().Err(err).Msg("db open failed")
		}
		var rows []userRow
		if err := db.Raw(`
			SELECT telegram_id
			FROM users
			WHERE telegram_id != 0
			  AND is_banned = false
			  AND is_active = true
			  AND bot_blocked = false
			ORDER BY id
		`).Scan(&rows).Error; err != nil {
			log.Fatal().Err(err).Msg("user query failed")
		}
		for _, r := range rows {
			targets = append(targets, r.TelegramID)
		}
		if len(targets) == 0 {
			log.Fatal().Msg("no reachable users found")
		}
	default:
		log.Fatal().Str("mode", *mode).Msg("invalid --mode")
	}

	log.Info().
		Str("mode", *mode).
		Int("recipients", len(targets)).
		Msg("starting broadcast")

	// ── Arm pause BEFORE sending in broadcast mode. Setting the flag
	// first means any scheduler cron that fires during the ~30s send
	// loop is silenced by SendWithRetry — the ad goes out via raw
	// Bot.Send below and is unaffected. We refresh the TTL after the
	// loop so the 1h pause is measured from broadcast-end.
	var rdb *redis.Client
	if *mode == "broadcast" {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			log.Fatal().Err(err).Msg("redis url invalid")
		}
		rdb = redis.NewClient(opt)
		if err := rdb.Set(context.Background(), BroadcastPauseKey, "1", pauseDur+10*time.Minute).Err(); err != nil {
			log.Fatal().Err(err).Msg("failed to arm pre-broadcast pause")
		}
		log.Info().Msg("broadcast pause armed (pre-send)")
	}

	// ── Send loop
	photo := &tele.Photo{
		File:    tele.FromDisk(*photoPath),
		Caption: captionHTML,
	}

	sent, failed := 0, 0
	for i, tgID := range targets {
		recipient := &tele.User{ID: tgID}
		_, err := b.Send(recipient, photo, tele.ModeHTML)
		if err != nil {
			failed++
			if !isPermanent(err) {
				// Quick second try for transient errors.
				time.Sleep(500 * time.Millisecond)
				if _, err2 := b.Send(recipient, photo, tele.ModeHTML); err2 == nil {
					sent++
					failed--
					err = nil
				} else {
					log.Debug().Err(err2).Int64("tg_id", tgID).Msg("retry failed")
				}
			} else {
				log.Debug().Err(err).Int64("tg_id", tgID).Msg("permanent error, skipping")
			}
		} else {
			sent++
		}

		// Telegram bulk limit: ~30 messages/sec is the documented ceiling,
		// 25/sec is safe in practice. Sleep every 25 sends.
		if (i+1)%25 == 0 {
			time.Sleep(1 * time.Second)
		}
		if (i+1)%100 == 0 {
			log.Info().Int("progress", i+1).Int("sent", sent).Int("failed", failed).Msg("…")
		}
	}

	log.Info().Int("sent", sent).Int("failed", failed).Int("total", len(targets)).Msg("broadcast done")

	// ── Refresh pause TTL to the requested duration, measured from
	// broadcast-end. Pre-send we set it long; this trims it back to the
	// caller's exact 1h (or whatever --pause was) starting now.
	if *mode == "broadcast" && rdb != nil {
		ctx := context.Background()
		if err := rdb.Set(ctx, BroadcastPauseKey, "1", pauseDur).Err(); err != nil {
			log.Fatal().Err(err).Msg("failed to refresh broadcast pause flag")
		}
		log.Info().
			Dur("for", pauseDur).
			Time("until", time.Now().Add(pauseDur)).
			Msg("broadcast pause re-armed — SendWithRetry will be muted until expiry")
	}
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatal().Str("var", k).Msg("required env var missing")
	}
	return v
}

func parseAdminIDs(raw string) []int64 {
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func isPermanent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range []string{
		"blocked by the user",
		"user is deactivated",
		"chat not found",
		"chat was not found",
		"bot was kicked",
		"bot can't initiate conversation",
		"peer_id_invalid",
	} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}
