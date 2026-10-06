package main

import (
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gofiber/contrib/websocket"
	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/games"
	"github.com/speak-up/backend/internal/handlers"
	"github.com/speak-up/backend/internal/handlers/admin"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/scheduler"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/ws"
)

func main() {
	// Pretty console logging for development
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}).With().Timestamp().Logger()

	// 1. Load config
	config.Load()

	// 2. Connect databases
	database.ConnectPostgres()
	database.ConnectRedis()

	// 3. Start WebSocket hub + Telegram bot + Scheduler
	ws.InitHub()
	bot.InitBot()
	scheduler.Start()

	// 4. Auto-migrate models
	database.AutoMigrate(
		&models.User{},
		&models.Session{},
		&models.SessionRating{},
		&models.ScheduledSession{},
		&models.WordBank{},
		&models.Payment{},
		&models.AuthOTPSession{},
		&models.StreakDay{},
		&models.Prize{},
		&models.FeedbackTicket{},
		&models.PromoCode{},
		&models.PromoRedemption{},
		&models.FullTestReport{},
		&models.SpeakingReport{},
		&models.GroqAPIKey{},
		&models.WeeklyLeaderboardAward{},
		&models.ChannelSubscriptionBonus{},
		&models.GameResult{},
		&models.TopicWord{},
		&models.SynonymCandidate{},
		&models.DictationSentence{},
		&models.Testimonial{},
		&models.Room{},
		&models.RoomMember{},
		&models.StudyCenter{},
		&models.PremiumGrant{},
		&models.Friendship{},
		&models.PremiumSettings{},
		&models.PremiumCampaign{},
		&models.PronunciationPassage{},
		&models.UserPassageSeen{},
		&models.UserPronunciationWord{},
	)

	// 4a. Encrypt any Groq API keys still sitting in the DB as plaintext.
	//
	// The encryption code and its migration shipped months ago but were
	// never switched on: ENCRYPTION_MASTER_KEY was unset and this
	// function was defined and then never called, so every key stayed at
	// encryption_version = 0. Calling it at boot makes the rollout
	// automatic - it is a no-op once every key is converted, and a no-op
	// when no master key is configured.
	if migrated, err := services.MigratePlainKeysToEncrypted(); err != nil {
		log.Error().Err(err).Msg("Groq key encryption migration failed")
	} else if migrated > 0 {
		log.Info().Int("keys", migrated).Msg("Groq API keys encrypted at rest")
	}

	// 4a-bis. Wire the games package to the WebSocket transport so it
	// can push events to clients without importing ws (avoids an import
	// cycle - same injection pattern as services.* below).
	// A channel join pushed by Telegram goes straight into the gate's
	// cache - the polled getChatMember answer lags minutes behind, which
	// is what made subscribed users bounce off the gate.
	bot.OnChannelMembership = services.ApplyChannelMembership

	// ...and, if that user has the Mini App open right now, the gate is
	// told to lift itself instead of waiting to be re-checked by hand.
	services.OnChannelUnlocked = func(tgID int64) {
		user, err := services.GetUserByTelegramID(tgID)
		if err != nil || user == nil {
			return
		}
		ws.SendToUser(user.ID.String(), "channel_subscribed", map[string]interface{}{
			"subscribed": true,
		})
	}

	games.Notify = ws.SendToUser
	// Story Chain duel: the LLM judges both players' continuations
	// (coherence/grammar/creativity) asynchronously so it never blocks gameplay.
	games.StoryJudgeFn = services.JudgeStory

	// Preload the mini-game dictionary + question banks off the request
	// path so the first game doesn't pay the one-time parse cost (~tens of
	// ms). Runs in the background so it never delays server startup.
	go games.Warm()

	// 4b. Wire referral + prize bonuses into the daily-limit service
	// (breaks the would-be import cycle: services → handlers). Each
	// active referral adds 2 minutes to the rolling 24h cap, no upper
	// bound. Recently-opened minute prize boxes also stack on top.
	// Note: the old "+10 min for subscribing to the channel" bonus is
	// deliberately NOT summed in any more. Channel subscription is now
	// mandatory for everyone (see services.IsChannelSubscribed), so paying
	// people for it would just hand the whole user base a permanent +10 and
	// silently turn the 10-minute free tier into 20.
	services.ReferralBonusFor = func(userID string) int {
		return handlers.CountActiveReferrals(userID)*2 +
			handlers.SumPrizeBonusMinutes(userID) +
			services.SumWeeklyLeaderboardBonus(userID)
	}
	// 4c. Inject the bot's pending-referral lookup so UpsertUserWithRef can
	// read codes that were stored via /start ref_X before the Mini App was
	// actually launched.
	services.PendingReferralLookup = bot.LookupPendingReferral

	// 4. Create Fiber app
	app := fiber.New(fiber.Config{
		AppName:      "speak-up API v1.0.0",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
		// 30 MB - comfortably fits a 2-3 minute opus recording from
		// the AI Speaking Test (POST /ai/check). Other endpoints
		// remain JSON-only and are far below this ceiling.
		BodyLimit: 30 * 1024 * 1024,
		// CRITICAL: without this, every request looks like it came from
		// 127.0.0.1 (the nginx loopback), so a per-IP rate limiter lumps
		// ALL real users into a single shared bucket. ProxyHeader makes
		// Fiber read the real client IP from nginx's X-Forwarded-For
		// header, and TrustedProxies whitelists who's allowed to set it.
		ProxyHeader:             fiber.HeaderXForwardedFor,
		EnableTrustedProxyCheck: true,
		TrustedProxies:          []string{"127.0.0.1", "::1"},
	})

	// 5. Global middleware. recover + request-id are cheap and must
	//    run for every request; logger + cors + rate limit are
	//    skipped for /ws and /health so those high-frequency hot
	//    paths never block on Redis or stdout.
	app.Use(recover.New())
	app.Use(middleware.RequestID())

	// Two different skip sets, because /uploads needs one but not the other.
	//
	// skipHotPaths - no logging, no rate limit. /uploads is static
	// partner-logo bytes: one session screen pulls several at once, so
	// they must not eat a user's API budget, and logging every image hit
	// is pure noise.
	//
	// skipCORS - only /ws and /health. /uploads is deliberately NOT here:
	// an <img> tag needs no CORS, but the moment the frontend reaches a
	// logo with fetch() or draws it to a canvas, a missing
	// Access-Control-Allow-Origin turns it into an opaque failure that is
	// miserable to debug from the browser side.
	skipHotPaths := func(c *fiber.Ctx) bool {
		p := c.Path()
		return p == "/ws" || p == "/health" || strings.HasPrefix(p, "/uploads/")
	}
	skipCORS := func(c *fiber.Ctx) bool {
		p := c.Path()
		return p == "/ws" || p == "/health"
	}

	app.Use(logger.New(logger.Config{
		Format:     "${time} | ${status} | ${latency} | ${method} ${path}\n",
		TimeFormat: "15:04:05",
		Next:       skipHotPaths,
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins:     joinOrigins(config.App.AllowedOrigins),
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET, POST, PUT, DELETE, OPTIONS",
		AllowCredentials: true,
		Next:             skipCORS,
	}))

	// 6. IP-level rate limit. Applies to everything EXCEPT /ws and
	//    /health. WebSocket upgrades are rare per session and already
	//    rate-limited implicitly by the max connections per user in
	//    the hub. /health is polled by nginx + the watchdog and must
	//    never be blocked by Redis latency. For authenticated API
	//    routes, a stricter per-user limit lives inside the `api`
	//    group below.
	globalLimiter := middleware.RateLimit(600, 60)
	app.Use(func(c *fiber.Ctx) error {
		if skipHotPaths(c) {
			return c.Next()
		}
		return globalLimiter(c)
	})

	// 6b. Uploaded partner-centre logos. Served by the app rather than
	// nginx so a deploy needs no web-server config change - the only
	// requirement is that UPLOAD_DIR is a Docker volume, otherwise every
	// rebuild wipes the logos.
	//
	// Browse:false is load-bearing: with it on, /uploads/ would list
	// every file in the directory.
	app.Static("/uploads", config.App.UploadDir, fiber.Static{
		Browse:        false,
		ByteRange:     true,
		CacheDuration: 24 * time.Hour,
		MaxAge:        86400,
	})

	// 7. Health check (no auth) - checks DB + Redis connectivity
	// AI Topics (public - auth kerak emas)
	app.Get("/ai/topics", handlers.GetAITopics)

	app.Get("/health", func(c *fiber.Ctx) error {
		dbOK := true
		redisOK := true

		// Check PostgreSQL
		sqlDB, err := database.DB.DB()
		if err != nil || sqlDB.Ping() != nil {
			dbOK = false
		}

		// Check Redis
		if err := database.Redis.Ping(c.Context()).Err(); err != nil {
			redisOK = false
		}

		status := "ok"
		code := 200
		if !dbOK || !redisOK {
			status = "degraded"
			code = 503
		}

		return c.Status(code).JSON(fiber.Map{
			"status":   status,
			"version":  "1.0.0",
			"postgres": dbOK,
			"redis":    redisOK,
			"online":   ws.H.OnlineCount(),
		})
	})

	// 7. Routes
	setupRoutes(app)

	// 8. Graceful shutdown
	safego.Go("http.Listen", func() {
		if err := app.Listen(":" + config.App.Port); err != nil {
			log.Fatal().Err(err).Msg("Server failed")
		}
	})

	log.Info().Str("port", config.App.Port).Msg("speak-up server running")

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("Shutting down...")

	// Stop accepting new HTTP requests first; in-flight ones get up to
	// 10 seconds to drain.
	if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
		log.Warn().Err(err).Msg("HTTP shutdown error")
	}

	// Then close the WebSocket hub so every live client receives a
	// `server_shutdown` message before their socket is killed.
	ws.H.Shutdown()

	// Stop the cron scheduler.
	scheduler.Stop()

	// Close Postgres pool - drains the connection pool cleanly so
	// in-flight transactions get a chance to commit/rollback.
	if sqlDB, err := database.DB.DB(); err == nil {
		if cerr := sqlDB.Close(); cerr != nil {
			log.Warn().Err(cerr).Msg("Postgres close error")
		}
	}

	// Close Redis last - leaderboard cache, rate limiters and idempotence
	// keys all live here.
	if database.Redis != nil {
		if cerr := database.Redis.Close(); cerr != nil {
			log.Warn().Err(cerr).Msg("Redis close error")
		}
	}

	log.Info().Msg("Shutdown complete")
}

func setupRoutes(app *fiber.App) {
	// ── Public routes (no auth) ──
	// Auth endpoints are the most attractive brute-force surface - every
	// one of them gets its own tight bucket on top of the IP-level
	// global limit so an attacker can't just rotate IPs cheaply.
	auth := app.Group("/auth")
	auth.Post("/telegram",
		middleware.RouteRateLimitClosed("auth_tg", 30, 60),
		handlers.TelegramAuth) // Mini App (initData)
	auth.Post("/request-code",
		middleware.RouteRateLimitClosed("auth_request", 10, 60),
		handlers.RequestCode) // Web → bot auth
	auth.Get("/check-session/:token",
		middleware.RouteRateLimitClosed("auth_check", 30, 60),
		handlers.CheckSession)

	// Telegram bot webhook - secret header validated when configured.
	app.Post("/bot/webhook",
		middleware.TelegramWebhookAuth(config.App.WebhookSecret),
		bot.WebhookHandler)

	// ── WebSocket endpoint ──
	app.Use("/ws", func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	})
	// Origin allowlist for the WebSocket upgrade.
	//
	// Without this the library defaults to Origins:["*"] - every origin
	// accepted. The empty string is included ON PURPOSE: browsers always
	// send an Origin header, non-browser clients (native apps, curl)
	// never do, and a non-browser client could forge any Origin it liked
	// anyway. So allowing "" costs nothing and keeps native clients
	// working, while a malicious *web page* - the only thing this header
	// can actually constrain - is still blocked.
	//
	// Defence in depth rather than a fix: the JWT travels in the query
	// string, so an attacker's page cannot read it and could not open an
	// authenticated socket regardless.
	wsOrigins := append([]string{""}, config.App.AllowedOrigins...)

	app.Get("/ws", websocket.New(func(conn *websocket.Conn) {
		// Authenticate via query param: /ws?token=xxx
		token := conn.Query("token")
		userID, err := services.DecodeJWT(token)
		if err != nil {
			conn.WriteJSON(map[string]string{"error": "Invalid token"})
			conn.Close()
			return
		}

		// Create client and start read/write pumps
		client := ws.NewClient(conn, userID)
		safego.Go("ws.WritePump", client.WritePump)
		client.ReadPump() // blocks until disconnect
	}, websocket.Config{Origins: wsOrigins}))

	// ── Protected routes (auth required + user-based rate limit) ──
	api := app.Group("", middleware.AuthRequired(), middleware.RateLimit(200, 60))

	// Users
	users := api.Group("/users")
	users.Get("/me", handlers.GetMyProfile)
	users.Put("/me", handlers.UpdateProfile)
	users.Put("/me/partner-alerts", handlers.UpdatePartnerAlerts)
	users.Put("/me/pinned-speaking", handlers.SetPinnedSpeaking)
	users.Get("/daily-usage", handlers.GetDailyUsage)
	users.Get("/online-stats", handlers.GetOnlineStats)
	users.Get("/me/feedback", handlers.GetMyFeedback)
	users.Get("/me/sessions", handlers.GetMySessions)
	users.Get("/me/referral", handlers.GetMyReferral)
	users.Get("/ice-config", handlers.GetIceConfig)

	// Streak calendar
	users.Get("/me/streak-calendar", handlers.GetStreakCalendar)

	// Prize boxes
	users.Get("/me/prizes", handlers.ListMyPrizes)
	users.Get("/me/prizes/today", handlers.GetTodayPrize)
	// Public winners ticker (real recent prize-box wins) for the home page.
	api.Get("/prizes/recent", handlers.RecentPrizeWinners)
	users.Get("/me/active-discount", handlers.GetActiveDiscount)

	// What premium costs right now: admin-set price, the live campaign
	// with its reason, and this user's own coupons folded in.
	api.Get("/premium/pricing", handlers.GetPremiumPricing)

	// Mandatory channel subscription gate. The Mini App reads
	// /channel-status on open and blocks itself when subscribed=false;
	// /channel-status/check backs the "I've joined, let me in" button and
	// skips the cache. The real enforcement is at the WebSocket queue -
	// these endpoints only drive the UI.
	users.Get("/me/channel-status", handlers.GetChannelStatus)
	users.Post("/me/channel-status/check",
		middleware.RouteRateLimit("check_channel_status", 10, 60),
		handlers.CheckChannelStatus)

	// Channel subscription bonus - one-time +10 min for joining @speakup_app.
	users.Get("/me/channel-bonus", handlers.GetChannelBonusStatus)
	users.Post("/me/channel-bonus/claim",
		middleware.RouteRateLimit("claim_channel_bonus", 5, 60),
		handlers.ClaimChannelBonus)
	users.Post("/me/prizes/:id/open",
		middleware.RouteRateLimit("open_prize", 10, 60),
		handlers.OpenPrize)

	// Feedback tickets - user side
	api.Post("/feedback",
		middleware.RouteRateLimit("create_feedback", 5, 60),
		handlers.CreateFeedback)
	users.Get("/me/feedback-tickets", handlers.ListMyFeedbackTickets)
	users.Get("/me/feedback-tickets/unread-count", handlers.UnreadFeedbackRepliesCount)

	// Testimonials (public reviews, admin-moderated)
	api.Get("/testimonials", handlers.GetPublicTestimonials)
	api.Post("/testimonials",
		middleware.RouteRateLimit("submit_testimonial", 5, 60),
		handlers.SubmitTestimonial)
	users.Get("/me/testimonial", handlers.GetMyTestimonial)

	// Promo code redemption - user side. Tight rate limit so a
	// guesser can't brute-force codes (max 5 attempts/min/user).
	api.Post("/promo-codes/redeem",
		middleware.RouteRateLimit("redeem_promo", 5, 60),
		handlers.RedeemPromoCode)

	// Sessions
	sessions := api.Group("/sessions")
	sessions.Get("/:id", handlers.GetSession)
	sessions.Post("/:id/end", handlers.EndSession)
	sessions.Post("/:id/rate", handlers.RateSession)

	// ── Friends ──
	// The only way in is having spoken to someone: there is no search
	// and no add-by-username, so a request always carries the context of
	// a real conversation.
	friends := api.Group("/friends")
	friends.Get("", handlers.ListFriends)
	friends.Get("/requests", handlers.ListFriendRequests)
	friends.Get("/with/:user_id", handlers.GetFriendshipWith)
	friends.Post("/requests",
		middleware.RouteRateLimit("friend_request", 20, 60),
		handlers.SendFriendRequest)
	friends.Post("/requests/:id/accept", handlers.AcceptFriendRequest)
	friends.Post("/requests/:id/decline", handlers.DeclineFriendRequest)
	friends.Delete("/:user_id", handlers.Unfriend)

	// ── Scheduled speaking ──
	// Booking is premium-only (the handler answers PREMIUM_REQUIRED so
	// the client can open the upgrade dialog); being invited, confirming
	// and joining are free for everyone.
	scheduled := api.Group("/scheduled")
	scheduled.Get("", handlers.ListScheduled)
	scheduled.Get("/history", handlers.ListScheduledHistory)
	scheduled.Post("",
		middleware.RouteRateLimit("create_scheduled", 20, 60),
		handlers.CreateScheduled)
	scheduled.Get("/:id", handlers.GetScheduled)
	scheduled.Post("/:id/confirm", handlers.ConfirmScheduled)
	scheduled.Post("/:id/decline", handlers.DeclineScheduled)
	scheduled.Delete("/:id", handlers.CancelScheduled)

	// ── Private teacher rooms ──
	// Rooms themselves are created by admins (see /admin/rooms below);
	// these are the student + teacher endpoints. The actual matching
	// happens over WebSocket (room_join_queue / room_start_round).
	rooms := api.Group("/rooms")
	rooms.Get("/mine", handlers.GetMyRooms)
	// /preview/:code is the other endpoint that takes a raw room code,
	// so it gets the same tight bucket as /join.
	rooms.Get("/preview/:code",
		middleware.RouteRateLimit("preview_room", 20, 60),
		handlers.PreviewRoom)
	// Tight bucket: /rooms/join is the only endpoint that takes a room
	// code, so it's the one an attacker would use to guess one.
	rooms.Post("/join",
		middleware.RouteRateLimit("join_room", 10, 60),
		handlers.JoinRoom)
	rooms.Get("/:id", handlers.GetRoom)
	rooms.Get("/:id/members", handlers.GetRoomMembers)
	// Owner administration of their OWN room. Creating rooms and raising
	// limits stay with the platform admin - see /admin/rooms.
	rooms.Put("/:id", handlers.UpdateRoom)
	rooms.Post("/:id/regenerate-code",
		middleware.RouteRateLimit("regen_room_code", 5, 60),
		handlers.RegenerateRoomCode)
	rooms.Get("/:id/sessions", handlers.GetRoomSessions)
	rooms.Get("/:id/report", handlers.GetRoomReport)
	// The teacher's register: today's attendance, streaks and AI bands.
	rooms.Get("/:id/students", handlers.GetRoomStudents)
	rooms.Get("/:id/students/:user_id", handlers.GetRoomStudent)
	rooms.Get("/:id/attendance", handlers.GetRoomAttendance)
	rooms.Post("/:id/leave", handlers.LeaveRoom)
	rooms.Delete("/:id/members/:user_id", handlers.RemoveRoomMember)

	// ── Partner learning centres ──
	// The centre's own admin edits its profile; every user can view an
	// approved centre's card and fetch the in-session banner rotation.
	// Creating and approving centres is admin-only (see /admin/centers).
	centers := api.Group("/centers")
	centers.Get("/mine", handlers.GetMyCenter)
	centers.Put("/mine", handlers.UpdateMyCenter)
	centers.Post("/mine/logo",
		middleware.RouteRateLimit("upload_center_logo", 10, 60),
		handlers.UploadCenterLogo)
	centers.Post("/mine/submit",
		middleware.RouteRateLimit("submit_center", 10, 60),
		handlers.SubmitCenterForReview)
	centers.Get("/:id", handlers.GetCenterProfile)
	centers.Post("/:id/click", handlers.TrackBannerClick)

	// In-session advertising rotation. Fetched once per session; the
	// client cycles through the returned list locally.
	api.Get("/session-banners", handlers.GetSessionBanners)

	// Word Bank
	wordbank := api.Group("/wordbank")
	wordbank.Get("", handlers.GetWords)
	wordbank.Post("", handlers.AddWord)
	wordbank.Put("/:id", handlers.UpdateWord)
	wordbank.Delete("/:id", handlers.DeleteWord)

	// Leaderboard
	leaderboard := api.Group("/leaderboard")
	leaderboard.Get("", handlers.Leaderboard)
	leaderboard.Get("/weekly", handlers.WeeklyLeaderboard)
	leaderboard.Get("/games", handlers.GameLeaderboard)

	// Payments are handled manually (admin contact + bank card transfer);
	// the admin grants premium via /admin/users/:id/grant-premium.

	// AI Speaking Test (alohida bo'lim - sessiya bilan bog'liq emas)
	ai := api.Group("/ai")
	ai.Post("/check", handlers.AICheck)
	ai.Get("/usage", handlers.GetAISpeakingUsage)
	ai.Get("/reports", handlers.GetMyAIReports)
	ai.Get("/reports/:id", handlers.GetAIReport)
	ai.Post("/improve", handlers.ImproveSpeech)

	// Full IELTS Speaking Test (Part 1 + 2 + 3)
	ai.Post("/full-test/start", handlers.StartFullTest)
	ai.Post("/full-test/:id/part2", handlers.SubmitPart2)
	ai.Post("/full-test/:id/part3", handlers.SubmitPart3)
	ai.Get("/full-test/:id", handlers.GetFullTest)
	ai.Get("/full-tests", handlers.ListFullTests)

	// Read-aloud pronunciation practice. Separate from /ai because the text is
	// known in advance, which is what makes per-sound feedback reliable enough
	// to show; /ai/check stays the free-speech path.
	pron := api.Group("/pronunciation")
	pron.Get("/options", handlers.GetPronunciationOptions)
	pron.Get("/usage", handlers.GetPronunciationUsage)
	pron.Get("/passage", handlers.GetPronunciationPassage)
	pron.Post("/attempt", handlers.SubmitPronunciationAttempt)
	pron.Get("/passage/:id/reference", handlers.GetPronunciationReference)

	// ── Admin routes (auth + admin required) ──
	adm := api.Group("/admin", middleware.AdminRequired())

	// Dashboard
	adm.Get("/dashboard", admin.Dashboard)

	// Analytics graphs (used by /admin/analytics page)
	adm.Get("/analytics/hourly", admin.HourlyActivity)
	adm.Get("/analytics/daily", admin.DailyActivity)
	adm.Get("/analytics/weekly", admin.WeeklyActivity)
	adm.Get("/analytics/retention", admin.UserRetention)
	adm.Get("/analytics/top-users", admin.TopUsers)

	// Users management
	adm.Get("/users", admin.ListUsers)
	adm.Get("/users/:id", admin.GetUser)
	adm.Put("/users/:id", admin.UpdateUser)
	adm.Post("/users/:id/ban", admin.BanUser)
	adm.Post("/users/:id/unban", admin.UnbanUser)
	adm.Post("/users/:id/toggle-premium", admin.TogglePremium)
	adm.Get("/users/:id/active-discount", admin.GetUserActiveDiscount)
	adm.Post("/users/:id/grant-premium",
		middleware.RouteRateLimit("grant_premium", 30, 60),
		admin.GrantPremium)
	adm.Post("/users/:id/revoke-premium", admin.RevokePremium)
	// The premium ledger: who was given premium, by whom and why.
	adm.Get("/premium-grants", admin.ListPremiumGrants)

	// Premium pricing: the price/card row, and the discount campaigns
	// an admin can start whenever they like.
	adm.Get("/premium/settings", admin.GetPremiumSettings)
	adm.Put("/premium/settings", admin.UpdatePremiumSettings)
	adm.Get("/premium/campaigns", admin.ListPremiumCampaigns)
	adm.Post("/premium/campaigns", admin.CreatePremiumCampaign)
	adm.Put("/premium/campaigns/:id", admin.UpdatePremiumCampaign)
	adm.Delete("/premium/campaigns/:id", admin.DeletePremiumCampaign)
	adm.Delete("/users/:id", admin.DeleteUser)

	// Sessions
	adm.Get("/sessions", admin.ListSessions)
	adm.Get("/sessions/:id", admin.GetSession)

	// Reports (low ratings / feedback)
	adm.Get("/reports", admin.ListReports)

	// IELTS Mocks (AI speaking tests done by users)
	adm.Get("/mocks", admin.ListMocks)
	adm.Get("/mocks/:kind/:id", admin.GetMock)

	// Feedback tickets - admin
	adm.Get("/feedback", handlers.AdminListFeedback)
	adm.Put("/feedback/:id", handlers.AdminUpdateFeedback)

	// Testimonials moderation
	adm.Get("/testimonials", handlers.AdminListTestimonials)
	adm.Put("/testimonials/:id", handlers.AdminModerateTestimonial)

	// Payments
	adm.Get("/payments", admin.ListPayments)

	// Prizes (who won what)
	adm.Get("/prizes", admin.ListPrizes)

	// Promo codes (admin CRUD + redemption history)
	adm.Get("/promo-codes", admin.ListPromoCodes)
	adm.Post("/promo-codes", admin.CreatePromoCode)
	adm.Put("/promo-codes/:id", admin.UpdatePromoCode)
	adm.Delete("/promo-codes/:id", admin.DeletePromoCode)
	adm.Get("/promo-codes/:id/redemptions", admin.ListPromoRedemptions)

	// Rooms (B2B: we open a room and assign it to a learning centre's
	// teacher - there is intentionally no self-service creation path).
	adm.Get("/rooms", admin.ListRooms)
	adm.Post("/rooms", admin.CreateRoom)
	adm.Put("/rooms/:id", admin.UpdateRoom)
	adm.Delete("/rooms/:id", admin.DeleteRoom)
	adm.Post("/rooms/:id/regenerate-code", admin.RegenerateRoomCode)
	adm.Get("/rooms/:id/members", admin.GetRoomMembers)
	adm.Get("/rooms/:id/sessions", admin.GetRoomSessions)
	adm.Put("/rooms/:id/center", admin.AttachRoomToCenter)

	// Partner centres: creation + content moderation. A centre never
	// signs itself up, and nothing it writes about itself reaches other
	// users' screens without passing through /moderate.
	adm.Get("/centers", admin.ListCenters)
	adm.Post("/centers", admin.CreateCenter)
	adm.Get("/centers/:id", admin.GetCenter)
	adm.Post("/centers/:id/moderate", admin.ModerateCenter)
	adm.Put("/centers/:id/contract", admin.UpdateCenterContract)

	adm.Get("/ai/keys", admin.ListGroqKeys)
	adm.Post("/ai/keys", admin.AddGroqKey)
	adm.Post("/ai/keys/test", admin.TestGroqKey)
	adm.Delete("/ai/keys/:id", admin.DeleteGroqKey)
	adm.Post("/ai/keys/:id/toggle", admin.ToggleGroqKey)
	adm.Post("/ai/keys/:id/reset", admin.ResetGroqKeyLimit)
}

func joinOrigins(origins []string) string {
	result := ""
	for i, o := range origins {
		if i > 0 {
			result += ", "
		}
		result += o
	}
	return result
}
