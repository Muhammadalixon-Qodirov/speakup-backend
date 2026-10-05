// forward is a one-off tool that forwards a single channel post to
// SpeakUp users via the existing Telegram bot. It mirrors cmd/broadcast
// but, instead of sending a freshly-composed photo, it FORWARDS an
// existing message from a channel (default @speakup_app, message 13) so
// recipients see the original post with its "Forwarded from" header.
//
// The bot must be a member of the source channel for forwarding to work.
//
// Usage:
//
//	forward --mode test                       # forward only to ADMIN_ALERT_CHAT_IDS
//	forward --mode broadcast --pause 1h       # forward to every reachable user
//	forward --channel @speakup_app --msgid 13 --mode test
//
// Modes:
//
//	test       → forward only to ADMIN_ALERT_CHAT_IDS, do NOT arm pause
//	broadcast  → forward to every reachable user, arm Redis pause
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

// BroadcastPauseKey mirrors bot.BroadcastPauseKey — we don't import the
// bot package because this tool runs without the full bot init.
const BroadcastPauseKey = "bot:broadcast_pause"

type userRow struct {
	TelegramID int64
}

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
		With().Timestamp().Logger()

	var (
		channel  = flag.String("channel", "@speakup_app", "source channel username (the bot must be a member)")
		msgIDsS  = flag.String("msgids", "13", "comma-separated message ids to forward as one album (e.g. 13,14)")
		mode     = flag.String("mode", "test", "test | broadcast")
		pauseStr = flag.String("pause", "1h", "duration of the Redis broadcast pause after a full broadcast")
	)
	flag.Parse()

	msgIDs := parseIntList(*msgIDsS)
	if len(msgIDs) == 0 {
		log.Fatal().Msg("--msgids is empty")
	}

	pauseDur, err := time.ParseDuration(*pauseStr)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid --pause duration")
	}

	// .env is optional — env vars may already be exported by the
	// surrounding container/shell.
	_ = godotenv.Load()

	botToken := mustEnv("BOT_TOKEN")
	redisURL := mustEnv("REDIS_URL")

	// ── Bot
	b, err := tele.NewBot(tele.Settings{Token: botToken, Verbose: false})
	if err != nil {
		log.Fatal().Err(err).Msg("bot init failed")
	}

	// ── Resolve the source channel to a numeric chat id. forwardMessage
	// needs the origin chat; ChatByUsername also confirms the bot can see
	// the channel at all before we fan anything out.
	srcChat, err := b.ChatByUsername(*channel)
	if err != nil {
		log.Fatal().Err(err).Str("channel", *channel).
			Msg("cannot resolve source channel — is the bot a member?")
	}
	log.Info().Str("channel", *channel).Int64("chat_id", srcChat.ID).
		Ints("msg_ids", msgIDs).Msg("source post resolved")

	// forwardOne forwards the whole album (all msgIDs) to a single
	// recipient via the forwardMessages Bot API method, so a multi-photo
	// post arrives grouped, not split into separate messages.
	forwardOne := func(tgID int64) error {
		_, err := b.Raw("forwardMessages", map[string]interface{}{
			"chat_id":      tgID,
			"from_chat_id": srcChat.ID,
			"message_ids":  msgIDs,
		})
		return err
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
		dbURL := mustEnv("DATABASE_URL")
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
		Msg("starting forward broadcast")

	// ── Arm pause BEFORE sending in broadcast mode (see cmd/broadcast for
	// the full rationale): any scheduler cron firing during the send loop
	// is silenced by SendWithRetry; the forwards below go out via raw
	// Bot.Forward and are unaffected. TTL is refreshed after the loop.
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
	sent, failed := 0, 0
	for i, tgID := range targets {
		err := forwardOne(tgID)
		if err != nil {
			failed++
			if !isPermanent(err) {
				// Quick second try for transient errors.
				time.Sleep(500 * time.Millisecond)
				if err2 := forwardOne(tgID); err2 == nil {
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

		// Telegram bulk limit: ~30 messages/sec documented, 25/sec safe.
		if (i+1)%25 == 0 {
			time.Sleep(1 * time.Second)
		}
		if (i+1)%100 == 0 {
			log.Info().Int("progress", i+1).Int("sent", sent).Int("failed", failed).Msg("…")
		}
	}

	log.Info().Int("sent", sent).Int("failed", failed).Int("total", len(targets)).Msg("forward broadcast done")

	// ── Refresh pause TTL to the requested duration, measured from end.
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

func parseIntList(raw string) []int {
	var out []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
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
