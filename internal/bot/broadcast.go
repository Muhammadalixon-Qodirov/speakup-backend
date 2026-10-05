// Admin-driven broadcasts. Lets the operator reply to any message in
// their bot chat with /broadcast, then /confirm, and have it copied to
// every reachable user — preserving the exact formatting, media and
// captions of the source message (Telegram's copyMessage API, no
// "forwarded from" tag). Each fan-out's (chat_id, message_id) tuples
// are recorded in Redis so /unbroadcast can pull the messages back if
// the operator notices a mistake.
//
// Access control: every handler short-circuits unless the sender's
// Telegram ID is in ADMIN_ALERT_CHAT_IDS. Non-admins see nothing.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	tele "gopkg.in/telebot.v3"
)

const (
	pendingBroadcastTTL = 5 * time.Minute
	broadcastLogTTL     = 7 * 24 * time.Hour
	broadcastPauseAfter = 1 * time.Hour

	broadcastRecentKey = "bot:bcast_recent"
)

type pendingBroadcast struct {
	SourceChatID int64 `json:"source_chat_id"`
	SourceMsgID  int   `json:"source_msg_id"`
}

func isAdmin(tgID int64) bool {
	for _, a := range config.App.AdminAlertChatIDs {
		if a == tgID {
			return true
		}
	}
	return false
}

func pendingBroadcastKey(adminID int64) string {
	return fmt.Sprintf("bot:pending_bcast:%d", adminID)
}

func broadcastLogKey(broadcastID string) string {
	return fmt.Sprintf("bot:bcast_log:%s", broadcastID)
}

// HandleBroadcast — admin replies to a message with /broadcast.
//
// Targeting (read from the /broadcast payload):
//
//	/broadcast            → all reachable users (asks for /confirm)
//	/broadcast me         → only the admin (immediate, no /confirm)
//	/broadcast <tg_id>    → that exact Telegram numeric ID (immediate)
//	/broadcast @username  → looks up users.username, sends to their TG ID
//
// Single-target sends skip the /confirm gate (they're test sends, the
// blast radius is one chat) but still get recorded in the broadcast
// log so /unbroadcast can pull them back.
func HandleBroadcast(c tele.Context) error {
	sender := c.Sender()
	if sender == nil || !isAdmin(sender.ID) {
		return nil
	}
	m := c.Message()
	if m == nil || m.ReplyTo == nil {
		return c.Send(
			"⚠️ /broadcast yuborilishi uchun avval boshqa xabarga <b>reply</b> qiling.\n\n"+
				"Foydalanish:\n"+
				"  <code>/broadcast</code> — hammaga (tasdiq so'raydi)\n"+
				"  <code>/broadcast me</code> — faqat sizga (test)\n"+
				"  <code>/broadcast 12345678</code> — Telegram IDga\n"+
				"  <code>/broadcast @username</code> — username orqali",
			tele.ModeHTML)
	}

	target := strings.TrimSpace(m.Payload)
	if target != "" && strings.ToLower(target) != "all" {
		return sendSingleBroadcast(c, m.Chat.ID, m.ReplyTo.ID, target, sender.ID)
	}

	pending := pendingBroadcast{
		SourceChatID: m.Chat.ID,
		SourceMsgID:  m.ReplyTo.ID,
	}
	data, _ := json.Marshal(pending)
	ctx := context.Background()
	if err := database.Redis.Set(ctx, pendingBroadcastKey(sender.ID), data, pendingBroadcastTTL).Err(); err != nil {
		log.Error().Err(err).Msg("broadcast: failed to store pending")
		return c.Send("❌ Redis xatosi. Qayta urinib ko'ring.")
	}

	var count int64
	database.DB.Model(&models.User{}).
		Where("telegram_id != 0 AND is_banned = false AND is_active = true AND bot_blocked = false").
		Count(&count)

	return c.Send(fmt.Sprintf(
		"📢 <b>Broadcast tayyor</b>\n\n"+
			"Reply qilingan xabar <b>%d ta</b> foydalanuvchiga aynan shu formatda (video, matn, button — hammasi) yuboriladi.\n\n"+
			"5 daqiqa ichida tasdiqlang:\n"+
			"  /confirm — yuborish\n"+
			"  /cancel — bekor qilish",
		count,
	), tele.ModeHTML)
}

// resolveTarget converts the /broadcast payload into a Telegram user
// ID. "me" maps to the admin who issued the command; a bare number is
// treated as the Telegram ID directly; an @handle is looked up in
// users.username.
func resolveTarget(target string, adminID int64) (int64, error) {
	switch strings.ToLower(target) {
	case "me", "self":
		return adminID, nil
	}
	if tgID, err := strconv.ParseInt(target, 10, 64); err == nil && tgID != 0 {
		return tgID, nil
	}
	if strings.HasPrefix(target, "@") {
		uname := strings.TrimPrefix(target, "@")
		var user models.User
		if err := database.DB.
			Where("LOWER(username) = LOWER(?)", uname).
			First(&user).Error; err != nil {
			return 0, fmt.Errorf("@%s — bunday foydalanuvchi topilmadi", uname)
		}
		if user.TelegramID == 0 {
			return 0, fmt.Errorf("@%s ning Telegram ID si yo'q", uname)
		}
		return user.TelegramID, nil
	}
	return 0, fmt.Errorf("noto'g'ri target: %q. Foydalanish: me | <tg_id> | @username | (bo'sh = hammaga)", target)
}

// sendSingleBroadcast copies the replied-to message to one target —
// the "test send" path. Logs (chat, msg_id) so /unbroadcast can roll
// it back. No pause is armed for a single-target send.
func sendSingleBroadcast(c tele.Context, sourceChatID int64, sourceMsgID int, targetSpec string, adminID int64) error {
	targetID, err := resolveTarget(targetSpec, adminID)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ %s", err.Error()))
	}

	src := &tele.Message{
		ID:   sourceMsgID,
		Chat: &tele.Chat{ID: sourceChatID},
	}
	copied, err := Bot.Copy(&tele.User{ID: targetID}, src)
	if err != nil {
		if isBlockedByUserError(err) {
			MarkBotBlocked(targetID)
		}
		return c.Send(fmt.Sprintf("❌ Yuborib bo'lmadi: %s", err.Error()))
	}

	broadcastID := fmt.Sprintf("test-%d", time.Now().Unix())
	ctx := context.Background()
	logKey := broadcastLogKey(broadcastID)
	database.Redis.HSet(ctx, logKey,
		strconv.FormatInt(targetID, 10),
		strconv.Itoa(copied.ID))
	database.Redis.Expire(ctx, logKey, broadcastLogTTL)
	database.Redis.ZAdd(ctx, broadcastRecentKey, redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: broadcastID,
	})
	database.Redis.ZRemRangeByRank(ctx, broadcastRecentKey, 0, -51)

	return c.Send(fmt.Sprintf(
		"✅ <b>Test xabar yuborildi</b>\n\n"+
			"  Target: <code>%d</code>\n"+
			"  Broadcast ID: <code>%s</code>\n\n"+
			"O'chirish: <code>/unbroadcast %s</code>\n"+
			"Hammasiga yuborish: o'sha asl xabarga reply qilib <code>/broadcast</code>",
		targetID, broadcastID, broadcastID,
	), tele.ModeHTML)
}

// HandleConfirm executes the most recent pending broadcast for the
// sender. The send loop runs in a goroutine so the handler can return
// immediately — Telegram retries the webhook if we sit on it.
func HandleConfirm(c tele.Context) error {
	sender := c.Sender()
	if sender == nil || !isAdmin(sender.ID) {
		return nil
	}
	ctx := context.Background()
	raw, err := database.Redis.Get(ctx, pendingBroadcastKey(sender.ID)).Result()
	if err == redis.Nil || raw == "" {
		return c.Send("⚠️ Pending broadcast yo'q. Avval xabarga reply qilib /broadcast yozing.")
	}
	if err != nil {
		log.Error().Err(err).Msg("broadcast: failed to read pending")
		return c.Send("❌ Redis xatosi. Qayta urinib ko'ring.")
	}
	var p pendingBroadcast
	if jerr := json.Unmarshal([]byte(raw), &p); jerr != nil {
		return c.Send("❌ Pending broadcast korrupt. Qayta /broadcast bering.")
	}
	// Atomic clear — duplicate /confirm taps won't re-broadcast.
	database.Redis.Del(ctx, pendingBroadcastKey(sender.ID))

	var users []models.User
	database.DB.
		Where("telegram_id != 0 AND is_banned = false AND is_active = true AND bot_blocked = false").
		Order("id").
		Find(&users)
	if len(users) == 0 {
		return c.Send("⚠️ Userlar topilmadi.")
	}

	broadcastID := strconv.FormatInt(time.Now().Unix(), 10)
	c.Send(fmt.Sprintf(
		"⏳ <b>%d</b> foydalanuvchiga yuborilmoqda…\n"+
			"Taxminan ~3 daqiqa.\n"+
			"Broadcast ID: <code>%s</code>",
		len(users), broadcastID,
	), tele.ModeHTML)

	adminID := sender.ID
	safego.Go("bcast/"+broadcastID, func() {
		runAdminBroadcast(p, users, broadcastID, adminID)
	})
	return nil
}

func runAdminBroadcast(p pendingBroadcast, users []models.User, broadcastID string, adminID int64) {
	ctx := context.Background()

	// Arm pause BEFORE so any scheduler cron firing during the ~3 min
	// fan-out is silenced by SendWithRetry. Refreshed to exactly the
	// configured duration once the loop finishes.
	database.Redis.Set(ctx, BroadcastPauseKey, "1", broadcastPauseAfter+10*time.Minute)

	sourceMsg := &tele.Message{
		ID:   p.SourceMsgID,
		Chat: &tele.Chat{ID: p.SourceChatID},
	}

	logKey := broadcastLogKey(broadcastID)
	sent, failed := 0, 0
	for i, u := range users {
		recipient := &tele.User{ID: u.TelegramID}
		copied, err := Bot.Copy(recipient, sourceMsg)
		if err != nil {
			// Mirror blocked-by-user into users.bot_blocked so future
			// auth attempts can show the "unblock the bot" CTA, same
			// as SendWithRetry does.
			if isBlockedByUserError(err) {
				MarkBotBlocked(u.TelegramID)
			}
			if !isPermanentTelegramError(err) {
				time.Sleep(500 * time.Millisecond)
				if c2, err2 := Bot.Copy(recipient, sourceMsg); err2 == nil {
					copied = c2
					err = nil
				}
			}
			if err != nil {
				failed++
			}
		}
		if err == nil && copied != nil && copied.ID != 0 {
			database.Redis.HSet(ctx, logKey,
				strconv.FormatInt(u.TelegramID, 10),
				strconv.Itoa(copied.ID))
			sent++
		}
		if (i+1)%25 == 0 {
			time.Sleep(1 * time.Second)
		}
	}

	database.Redis.Expire(ctx, logKey, broadcastLogTTL)
	database.Redis.ZAdd(ctx, broadcastRecentKey, redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: broadcastID,
	})
	// Keep the index modest — last 50 broadcasts.
	database.Redis.ZRemRangeByRank(ctx, broadcastRecentKey, 0, -51)

	// Refresh pause to exactly the configured window from broadcast-end.
	database.Redis.Set(ctx, BroadcastPauseKey, "1", broadcastPauseAfter)

	Bot.Send(&tele.User{ID: adminID}, fmt.Sprintf(
		"✅ <b>Broadcast tugadi</b>\n\n"+
			"  ID: <code>%s</code>\n"+
			"  Yuborilgan: <b>%d</b>\n"+
			"  Xato: <b>%d</b>\n\n"+
			"Hammasini o'chirib tashlash:\n  <code>/unbroadcast %s</code>\n\n"+
			"Bot 1 soatga jim qilindi (boshqa avtomatik xabarlar yetkazilmaydi).",
		broadcastID, sent, failed, broadcastID,
	), tele.ModeHTML)
}

// HandleCancel clears a pending broadcast without sending anything.
func HandleCancel(c tele.Context) error {
	sender := c.Sender()
	if sender == nil || !isAdmin(sender.ID) {
		return nil
	}
	n, _ := database.Redis.Del(context.Background(), pendingBroadcastKey(sender.ID)).Result()
	if n == 0 {
		return c.Send("Pending broadcast yo'q edi.")
	}
	return c.Send("✅ Bekor qilindi.")
}

// HandleUnbroadcast pulls a previous broadcast back — calls
// Telegram's deleteMessage on every (chat, message) recorded in the
// broadcast log. Telegram only allows bots to delete messages they
// sent within 48 hours, so older broadcasts may partially fail.
func HandleUnbroadcast(c tele.Context) error {
	sender := c.Sender()
	if sender == nil || !isAdmin(sender.ID) {
		return nil
	}
	args := strings.TrimSpace(c.Message().Payload)
	if args == "" {
		return c.Send("Foydalanish: <code>/unbroadcast &lt;id&gt;</code>\nOxirgi broadcastlar: /broadcasts", tele.ModeHTML)
	}

	ctx := context.Background()
	logKey := broadcastLogKey(args)
	entries, err := database.Redis.HGetAll(ctx, logKey).Result()
	if err != nil || len(entries) == 0 {
		return c.Send(fmt.Sprintf("Broadcast <code>%s</code> topilmadi yoki muddati o'tgan (7 kun).", args), tele.ModeHTML)
	}

	c.Send(fmt.Sprintf(
		"⏳ <b>%d</b> ta xabar o'chirilmoqda…\nTelegram bot 48 soatdan eski xabarlarni o'chira olmaydi — bir qismi muvaffaqiyatsiz bo'lishi mumkin.",
		len(entries),
	), tele.ModeHTML)

	broadcastID := args
	adminID := sender.ID
	safego.Go("unbcast/"+broadcastID, func() {
		runAdminUnbroadcast(entries, broadcastID, adminID)
	})
	return nil
}

func runAdminUnbroadcast(entries map[string]string, broadcastID string, adminID int64) {
	ctx := context.Background()
	deleted, failed := 0, 0
	i := 0
	for chatIDStr, msgIDStr := range entries {
		chatID, _ := strconv.ParseInt(chatIDStr, 10, 64)
		msgID, _ := strconv.Atoi(msgIDStr)
		err := Bot.Delete(&tele.Message{
			ID:   msgID,
			Chat: &tele.Chat{ID: chatID},
		})
		if err == nil {
			deleted++
		} else {
			failed++
		}
		i++
		if i%25 == 0 {
			time.Sleep(1 * time.Second)
		}
	}

	// Once we've tried, drop the log and index entry. Even partially-
	// failed entries are gone from Telegram's perspective on whatever
	// chats accepted the delete — the residue is in chats we couldn't
	// reach (bot blocked, message too old, etc.) and we can't fix
	// those from here anyway.
	database.Redis.Del(ctx, broadcastLogKey(broadcastID))
	database.Redis.ZRem(ctx, broadcastRecentKey, broadcastID)

	Bot.Send(&tele.User{ID: adminID}, fmt.Sprintf(
		"✅ <b>O'chirish tugadi</b>\n\n"+
			"  Broadcast: <code>%s</code>\n"+
			"  O'chirildi: <b>%d</b>\n"+
			"  Xato: <b>%d</b>",
		broadcastID, deleted, failed,
	), tele.ModeHTML)
}

// HandleBroadcastsList shows the operator the last few broadcast IDs
// so they can pick one to /unbroadcast without remembering numbers.
func HandleBroadcastsList(c tele.Context) error {
	sender := c.Sender()
	if sender == nil || !isAdmin(sender.ID) {
		return nil
	}
	ctx := context.Background()
	res, err := database.Redis.ZRevRangeWithScores(ctx, broadcastRecentKey, 0, 9).Result()
	if err != nil || len(res) == 0 {
		return c.Send("Oxirgi broadcastlar yo'q.")
	}
	uzt := time.FixedZone("UZT", 5*60*60)
	var b strings.Builder
	b.WriteString("📋 <b>Oxirgi broadcastlar</b>\n\n")
	for _, z := range res {
		id, _ := z.Member.(string)
		ts := time.Unix(int64(z.Score), 0).In(uzt)
		count, _ := database.Redis.HLen(ctx, broadcastLogKey(id)).Result()
		b.WriteString(fmt.Sprintf("• <code>%s</code> — %s (%d xabar)\n", id, ts.Format("02.01 15:04"), count))
	}
	b.WriteString("\nO'chirish: <code>/unbroadcast &lt;id&gt;</code>")
	return c.Send(b.String(), tele.ModeHTML)
}
