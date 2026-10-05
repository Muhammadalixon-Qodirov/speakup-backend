package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	tele "gopkg.in/telebot.v3"
	"gorm.io/gorm"
)

// Bot is the global Telegram bot instance.
var Bot *tele.Bot

// pendingSessions maps telegram user ID → session_token
// for new users waiting to share phone number.
var (
	pendingSessions   = map[int64]string{}
	pendingSessionsMu sync.Mutex
)

// InitBot creates the bot and registers handlers.
func InitBot() {
	if config.App.BotToken == "" {
		log.Warn().Msg("BOT_TOKEN not set, Telegram bot disabled")
		return
	}

	pref := tele.Settings{
		Token:   config.App.BotToken,
		Poller:  &tele.LongPoller{Timeout: 10 * time.Second},
		Verbose: false,
	}

	// Telegram connectivity from this host is occasionally flaky at boot
	// (DNS sometimes resolves to an unreachable IPv6 / a blocked IP), and
	// tele.NewBot does a getMe round trip. A single failure used to leave
	// Bot nil for the whole process lifetime - the webhook then returns
	// 200 for every update but never replies. Retry with backoff so a
	// transient blip can't permanently disable the bot.
	var b *tele.Bot
	var err error
	for attempt := 1; attempt <= 6; attempt++ {
		b, err = tele.NewBot(pref)
		if err == nil {
			break
		}
		log.Warn().Err(err).Int("attempt", attempt).Msg("Telegram bot init failed, retrying")
		time.Sleep(time.Duration(attempt) * 5 * time.Second)
	}
	if err != nil {
		log.Error().Err(err).Msg("Failed to create Telegram bot after retries")
		return
	}

	Bot = b
	Bot.Handle("/start", handleStart)
	Bot.Handle(tele.OnContact, handleContact)
	// my_chat_member: Telegram pushes this the instant a user blocks or
	// unblocks the bot. We mirror the state into users.bot_blocked so
	// auth can refuse blocked users with a clear unblock CTA.
	Bot.Handle(tele.OnMyChatMember, handleMyChatMember)
	// chat_member: somebody else's membership changed in a chat we
	// administer - i.e. a user joining or leaving the required
	// channel. This is the fast path for the subscription gate.
	Bot.Handle(tele.OnChatMember, handleChatMember)

	// Admin-only broadcast commands. Non-admins get no response — every
	// handler short-circuits silently when sender isn't in
	// ADMIN_ALERT_CHAT_IDS.
	Bot.Handle("/broadcast", HandleBroadcast)
	Bot.Handle("/confirm", HandleConfirm)
	Bot.Handle("/cancel", HandleCancel)
	Bot.Handle("/unbroadcast", HandleUnbroadcast)
	Bot.Handle("/broadcasts", HandleBroadcastsList)

	// Wire panic-recovery alerts: every panic the safego package
	// catches will now be fanned out to ADMIN_ALERT_CHAT_IDS via this
	// bot. Boot-time setup so it's ready before any background goroutine
	// can fire.
	InstallAlertHook()

	if !config.App.IsProduction() {
		safego.Go("bot/poller", Bot.Start)
		log.Info().Msg("Telegram bot started (polling mode)")
	} else {
		startWebhook()
		log.Info().Msg("Telegram bot started (webhook mode)")
	}

	if len(config.App.AdminAlertChatIDs) > 0 {
		// One-off boot ping so the operator immediately knows alerts
		// are wired and credentials work.
		env := config.App.AppEnv
		miniApp := config.App.MiniAppURL
		safego.Go("bootAlert", func() {
			AlertAdmin("speakup-api boot",
				fmt.Sprintf("Server started\nEnv: %s\nMini App: %s",
					env, miniApp))
		})
	}
}

func startWebhook() {
	if Bot == nil || config.App.WebhookURL == "" {
		return
	}
	webhook := &tele.Webhook{
		Listen: ":0",
		Endpoint: &tele.WebhookEndpoint{
			PublicURL: config.App.WebhookURL,
		},
		// chat_member is NOT sent unless it is asked for here. Without
		// it the only way to learn about a channel join is polling
		// getChatMember, which answers "left" for minutes after the
		// user actually joined - the cause of the "we cannot see your
		// subscription" complaints.
		AllowedUpdates: []string{"message", "callback_query", "my_chat_member", "chat_member"},
		// SecretToken MUST be sent on every registration.
		//
		// Telegram treats setWebhook as a full replacement: any parameter
		// you omit is reset. So a SetWebhook call without secret_token
		// silently DELETES the secret Telegram had been sending - and
		// because our middleware then rejects every unsigned update with
		// 401, the bot goes completely deaf on the next restart.
		//
		// This is not hypothetical: it happened during the 2026-08-01
		// deploy, minutes after the secret was first configured.
		SecretToken: config.App.WebhookSecret,
	}
	if err := Bot.SetWebhook(webhook); err != nil {
		log.Error().Err(err).Msg("Failed to set Telegram webhook")
		return
	}
	log.Info().
		Str("url", config.App.WebhookURL).
		Bool("secret_set", config.App.WebhookSecret != "").
		Msg("Telegram webhook set")
}

// WebhookHandler handles POST /bot/webhook from Telegram.
func WebhookHandler(c *fiber.Ctx) error {
	if Bot == nil {
		return c.JSON(fiber.Map{"ok": true})
	}
	var update tele.Update
	if err := json.Unmarshal(c.Body(), &update); err != nil {
		return c.JSON(fiber.Map{"ok": false})
	}
	Bot.ProcessUpdate(update)
	return c.JSON(fiber.Map{"ok": true})
}

// PendingRefTTL is how long a referral code sent via /start stays valid
// before it's discarded. Enough for the user to actually tap the Web App
// button and finish onboarding.
const PendingRefTTL = 1 * time.Hour

// StorePendingReferral records "this telegram user came via this code" in
// Redis. When they subsequently open the Mini App and /auth/telegram runs,
// UpsertUserWithRef looks the code up even though start_param isn't carried
// through a Web App button launch.
func StorePendingReferral(tgID int64, code string) {
	if code == "" {
		return
	}
	ctx := context.Background()
	key := fmt.Sprintf("pending_ref:%d", tgID)
	database.Redis.Set(ctx, key, code, PendingRefTTL)
}

// LookupPendingReferral reads (and deletes) a pending referral code for a
// given telegram user. Called from UpsertUserWithRef on first signup.
func LookupPendingReferral(tgID int64) string {
	ctx := context.Background()
	key := fmt.Sprintf("pending_ref:%d", tgID)
	code, err := database.Redis.GetDel(ctx, key).Result()
	if err != nil || code == "" {
		return ""
	}
	return code
}

// handleStart handles /start command.
//
//	/start auth_{token} → web login flow
//	/start ref_{code}   → referral landing page, shows Mini App button
//	/start              → Mini App button
func handleStart(c tele.Context) error {
	payload := c.Message().Payload

	if strings.HasPrefix(payload, "auth_") {
		return handleAuthStart(c, strings.TrimPrefix(payload, "auth_"))
	}

	// Referral landing - remember the code for this TG ID, then show the
	// usual Mini App button. The code is consumed on first successful
	// Mini App signup (UpsertUserWithRef looks it up).
	if strings.HasPrefix(payload, "ref_") {
		code := strings.TrimPrefix(payload, "ref_")
		if code != "" {
			StorePendingReferral(c.Sender().ID, code)
		}

		markup := &tele.ReplyMarkup{}
		btn := markup.WebApp("🎤 SpeakUp'ni ochish", &tele.WebApp{URL: config.App.MiniAppURL})
		markup.Inline(markup.Row(btn))

		return c.Send(
			"🎁 <b>Do'stingiz sizni taklif qildi!</b>\n\n"+
				"SpeakUp - ingliz tilini real odamlar bilan speaking qilib o'rganing.\n"+
				"Pastdagi tugmani bosing va ilovani oching. Birinchi kirishingiz avtomatik hisobga olinadi.",
			markup,
			tele.ModeHTML,
		)
	}

	// Room invite landing. The canonical teacher link is
	// t.me/<bot>?startapp=room_<CODE>, which opens the Mini App directly
	// and joins on login. This branch covers the ?start= form (a plain
	// chat link, which is what Telegram produces if someone shares the
	// bot rather than the app): we can't carry start_param through a
	// WebApp button, so the code is appended to the Mini App URL and the
	// frontend posts it to /rooms/join on open.
	if strings.HasPrefix(payload, "room_") {
		code := strings.TrimPrefix(payload, "room_")
		if code != "" {
			markup := &tele.ReplyMarkup{}
			btn := markup.WebApp("🎓 Roomga kirish", &tele.WebApp{
				URL: config.App.MiniAppURL + "?room=" + url.QueryEscape(code),
			})
			markup.Inline(markup.Row(btn))

			return c.Send(
				"🎓 <b>Sizni speaking roomga taklif qilishdi!</b>\n\n"+
					"Bu o'qituvchingizning yopiq roomi. Ichida navbat kutmaysiz - "+
					"\"Speak\" tugmasini bosasiz va tizim sizni guruhdoshlaringizdan "+
					"biri bilan juftlashtiradi.\n\n"+
					"Pastdagi tugmani bosing.",
				markup,
				tele.ModeHTML,
			)
		}
	}

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("speak-up ni ochish", &tele.WebApp{URL: config.App.MiniAppURL})
	markup.Inline(markup.Row(btn))

	return c.Send(
		"Salom! 👋 speak-up - ingliz tilini speaking orqali o'rganing.\n\n"+
			"Real odamlar bilan 1v1 suhbat qiling va tez o'sib boring!",
		markup,
	)
}

// handleAuthStart - web login via Telegram.
//
// Existing user (telegram_id in DB) → auto login, no phone needed.
// New user → ask for phone number via contact button.
// Hidden phone → still uses contact button (Telegram always allows sharing own contact).
func handleAuthStart(c tele.Context, sessionToken string) error {
	// 1. Validate session
	var session models.AuthOTPSession
	err := database.DB.
		Where("session_token = ? AND status = ?", sessionToken, "pending").
		First(&session).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.Send("❌ Kirish havolasi yaroqsiz yoki muddati o'tgan.\n\nIltimos, veb-saytda qayta urinib ko'ring.")
	}
	if err != nil {
		log.Error().Err(err).Msg("OTP session DB error")
		return c.Send("❌ Xatolik yuz berdi. Iltimos qayta urinib ko'ring.")
	}
	if session.IsExpired() {
		return c.Send("❌ Kirish havolasi muddati o'tgan.\n\nIltimos, veb-saytda qayta urinib ko'ring.")
	}

	sender := c.Sender()

	// 2. Check if user already exists
	var existingUser models.User
	err = database.DB.Where("telegram_id = ?", sender.ID).First(&existingUser).Error

	if err == nil {
		// ✅ User exists - auto login! Update profile data and complete session.
		database.DB.Model(&existingUser).Updates(map[string]interface{}{
			"first_name": sender.FirstName,
			"last_name":  strPtr(sender.LastName),
			"username":   strPtr(sender.Username),
		})

		database.DB.Model(&session).Updates(map[string]interface{}{
			"status":      "completed",
			"telegram_id": sender.ID,
		})

		log.Info().Int64("tg_id", sender.ID).Msg("Existing user auto-login via bot")
		return c.Send("✅ Muvaffaqiyatli kirdingiz! Saytga qaytishingiz mumkin.")
	}

	// 3. New user - ask for phone number
	pendingSessionsMu.Lock()
	pendingSessions[sender.ID] = sessionToken
	pendingSessionsMu.Unlock()

	markup := &tele.ReplyMarkup{ResizeKeyboard: true, OneTimeKeyboard: true}
	btn := markup.Contact("📱 Raqamni ulashish")
	markup.Reply(markup.Row(btn))

	return c.Send(
		"Ro'yxatdan o'tish uchun telefon raqamingizni ulashing. 👇",
		markup,
	)
}

// handleContact - new user registration via phone sharing.
func handleContact(c tele.Context) error {
	if c.Message().Contact == nil {
		return nil
	}

	senderID := c.Sender().ID

	pendingSessionsMu.Lock()
	sessionToken, ok := pendingSessions[senderID]
	if ok {
		delete(pendingSessions, senderID)
	}
	pendingSessionsMu.Unlock()

	removeKeyboard := &tele.ReplyMarkup{RemoveKeyboard: true}

	if !ok {
		return c.Send(
			"❌ Aktiv kirish sessiyasi topilmadi.\n\nIltimos, veb-saytdan qayta urinib ko'ring.",
			removeKeyboard,
		)
	}

	// Find and validate session
	var session models.AuthOTPSession
	err := database.DB.
		Where("session_token = ? AND status = ?", sessionToken, "pending").
		First(&session).Error

	if errors.Is(err, gorm.ErrRecordNotFound) || session.IsExpired() {
		return c.Send("❌ Kirish sessiyasi muddati o'tgan.\n\nIltimos, veb-saytdan qayta urinib ko'ring.", removeKeyboard)
	}
	if err != nil {
		log.Error().Err(err).Msg("OTP session DB error on contact")
		return c.Send("❌ Xatolik yuz berdi.", removeKeyboard)
	}

	// Create new user
	phone := normalizePhone(c.Message().Contact.PhoneNumber)
	sender := c.Sender()

	newUser := models.User{
		Phone:      strPtr(phone),
		TelegramID: senderID,
		FirstName:  sender.FirstName,
		LastName:   strPtr(sender.LastName),
		Username:   strPtr(sender.Username),
	}

	// Check if phone already exists
	var existingUser models.User
	if database.DB.Where("phone = ?", phone).First(&existingUser).Error == nil {
		// Phone exists - link telegram_id
		database.DB.Model(&existingUser).Updates(map[string]interface{}{
			"telegram_id": senderID,
			"first_name":  sender.FirstName,
			"last_name":   strPtr(sender.LastName),
			"username":    strPtr(sender.Username),
		})
	} else if database.DB.Where("telegram_id = ?", senderID).First(&existingUser).Error == nil {
		// TG exists - update phone
		database.DB.Model(&existingUser).Update("phone", phone)
	} else {
		// Completely new user
		if err := database.DB.Create(&newUser).Error; err != nil {
			log.Error().Err(err).Msg("Failed to create user")
			return c.Send("❌ Ro'yxatdan o'tishda xatolik.", removeKeyboard)
		}
	}

	// Complete session
	database.DB.Model(&session).Updates(map[string]interface{}{
		"status":      "completed",
		"telegram_id": senderID,
	})

	log.Info().Int64("tg_id", senderID).Str("phone", phone).Msg("New user registered via bot")
	return c.Send("✅ Ro'yxatdan o'tdingiz! Saytga qaytishingiz mumkin.", removeKeyboard)
}

func normalizePhone(phone string) string {
	re := regexp.MustCompile(`\D`)
	digits := re.ReplaceAllString(phone, "")
	if len(digits) == 9 {
		digits = "998" + digits
	}
	if len(digits) == 10 && digits[0] == '0' {
		digits = "998" + digits[1:]
	}
	if len(digits) < 7 {
		return ""
	}
	return digits
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// handleMyChatMember fires whenever a user blocks or unblocks the bot.
// Telegram sends a ChatMemberUpdated with:
//
//	NewChatMember.Role = "kicked"  → user blocked
//	NewChatMember.Role = "member"  → user unblocked (usually after /start)
//
// We mirror that into users.bot_blocked so the auth middleware can refuse
// logins from blocked accounts with a clear "unblock the bot first" modal.
// handleChatMember mirrors a channel join/leave into the subscription
// cache the moment Telegram tells us about it.
//
// Polling getChatMember is not enough on its own: the Bot API keeps
// reporting "left" for one to three minutes after a join, which users
// meet as the app flatly refusing to believe them. This push arrives at
// once, so the gate lifts before they have finished being annoyed.
func handleChatMember(c tele.Context) error {
	upd := c.Update().ChatMember
	if upd == nil || upd.NewChatMember == nil || upd.NewChatMember.User == nil {
		return nil
	}

	// Only the channel the gate is about. The bot may be an admin
	// elsewhere, and those updates mean nothing here.
	required := strings.TrimPrefix(config.App.RequiredChannel, "@")
	if required == "" || !strings.EqualFold(upd.Chat.Username, required) {
		return nil
	}

	tgID := upd.NewChatMember.User.ID
	if tgID == 0 {
		return nil
	}

	subscribed := MembershipRoleSubscribed(string(upd.NewChatMember.Role))

	log.Info().
		Int64("tg_id", tgID).
		Str("role", string(upd.NewChatMember.Role)).
		Bool("subscribed", subscribed).
		Msg("chat_member: channel membership changed")

	if OnChannelMembership != nil {
		OnChannelMembership(tgID, subscribed)
	}
	return nil
}

func handleMyChatMember(c tele.Context) error {
	upd := c.Update().MyChatMember
	if upd == nil || upd.NewChatMember == nil {
		return nil
	}

	// For a 1-on-1 private chat the chat ID equals the user's Telegram ID.
	// Sender is nominally "who kicked the bot" but may be empty on some
	// delivery paths - fall back to Chat.ID.
	tgID := upd.Chat.ID
	if tgID == 0 && upd.Sender != nil {
		tgID = upd.Sender.ID
	}
	if tgID == 0 {
		return nil
	}

	newStatus := string(upd.NewChatMember.Role)
	blocked := newStatus == "kicked"

	// Only write if the state actually changed so we don't thrash the row.
	res := database.DB.Model(&models.User{}).
		Where("telegram_id = ? AND bot_blocked != ?", tgID, blocked).
		Update("bot_blocked", blocked)
	if res.Error != nil {
		log.Warn().Err(res.Error).Int64("tg_id", tgID).Msg("my_chat_member: DB update failed")
		return nil
	}
	if res.RowsAffected > 0 {
		log.Info().
			Int64("tg_id", tgID).
			Bool("bot_blocked", blocked).
			Str("new_status", newStatus).
			Msg("bot block state changed")
	}
	return nil
}

// MarkBotBlocked sets bot_blocked=true for the user with the given
// Telegram ID. Called from send.go when a permanent "blocked by user"
// error comes back - it's the fallback for the rare case Telegram didn't
// deliver the my_chat_member webhook (e.g. the user blocked us before we
// subscribed to that update type, or a webhook was dropped).
func MarkBotBlocked(telegramID int64) {
	if telegramID == 0 {
		return
	}
	database.DB.Model(&models.User{}).
		Where("telegram_id = ? AND bot_blocked = ?", telegramID, false).
		Update("bot_blocked", true)
}

// MarkBotUnblocked clears bot_blocked. Useful for admin tooling / tests;
// in normal flow the my_chat_member webhook handles this automatically.
func MarkBotUnblocked(telegramID int64) {
	if telegramID == 0 {
		return
	}
	database.DB.Model(&models.User{}).
		Where("telegram_id = ? AND bot_blocked = ?", telegramID, true).
		Update("bot_blocked", false)
}
