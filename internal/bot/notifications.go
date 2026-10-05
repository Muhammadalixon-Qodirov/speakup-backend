package bot

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	tele "gopkg.in/telebot.v3"
)

// Match invitation message templates, personalized format. Placeholders
// are indexed: %[1]s = recipient first name, %[2]s = partner first name,
// %[3]s = level label. Indexed form keeps the args stable even when a
// template reorders them for natural phrasing (e.g. "partner is waiting,
// recipient!" reads better with partner first).
//
// One template is picked uniformly per send. With 50+ variants and a
// 90-min cooldown, a user receives the same phrasing maybe once a week
// at heaviest - the stream feels hand-written.
//
// Style rules:
//   - ≤4 short lines; glance→tap, no walls of text
//   - Always include both names + level stamp
//   - End with a short action-oriented CTA
//   - Mix tones: greeting-led, partner-led, question-led, one-liner
//   - Vary emojis: no single emoji dominates the set
var matchInviteMessages = []string{
	// ── Greeting-led
	"👋 Salom <b>%[1]s</b>!\n\n<b>%[2]s</b> siz kabi speaking partner qidirmoqda\n📊 <b>%[3]s</b>\n\n⚡ 5 daqiqa vaqt bormi?",
	"Hey <b>%[1]s</b>! 👋\n<b>%[2]s</b> hozir onlayn\n📊 <b>%[3]s</b>\n\n⚡ Gaplashib olaylikmi?",
	"Assalomu alaykum, <b>%[1]s</b>!\n<b>%[2]s</b> hozir speaking uchun kutmoqda\n📊 <b>%[3]s</b>\n\n💬 Bir suhbat?",
	"Qalaysiz, <b>%[1]s</b>?\n<b>%[2]s</b> siz bilan speaking qilmoqchi\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
	"Ey <b>%[1]s</b>! ✋\n<b>%[2]s</b> speaking uchun bo'sh\n📊 <b>%[3]s</b>\n\n⚡ Ulanaylikmi?",

	// ── Partner-led (partner-first order)
	"📞 <b>%[2]s</b> sizni kutmoqda, <b>%[1]s</b>\n📊 <b>%[3]s</b>\n\n⚡ Hozir kirasizmi?",
	"🌟 Onlayn bo'lganlardan <b>%[2]s</b> sizni tanladi, <b>%[1]s</b>\n📊 <b>%[3]s</b>\n\n⚡ 5 daqiqa ajratasizmi?",
	"🎙 <b>%[2]s</b> sizga ulanishga tayyor, <b>%[1]s</b>\n📊 <b>%[3]s</b>\n\n⚡ Qo'shilasizmi?",
	"🤝 <b>%[2]s</b> siz bilan amaliyot qilmoqchi, <b>%[1]s</b>\n📊 <b>%[3]s</b>\n\n✨ Boshlaymizmi?",
	"💫 <b>%[2]s</b> sizni tanladi, <b>%[1]s</b>\n📊 <b>%[3]s</b>\n\n⚡ Ulasinmi?",

	// ── Recipient-first, partner-second
	"<b>%[1]s</b>, <b>%[2]s</b> sizni tanladi\n🎙 Hozir onlayn va speaking uchun tayyor\n📊 <b>%[3]s</b>\n\n⚡ Ulamanmi?",
	"<b>%[1]s</b> — <b>%[2]s</b> sizni kutmoqda\n📊 <b>%[3]s</b>\n\n⚡ Tayyormisiz?",
	"🎯 <b>%[1]s</b>, <b>%[2]s</b> sherik qidiryapti\n📊 <b>%[3]s</b>\n\n⚡ Kirsangiz ulaymiz",
	"<b>%[1]s</b> — <b>%[2]s</b> sizga mos keladi\n🎙 Speaking uchun tayyor\n📊 <b>%[3]s</b>\n\n⚡ Gaplashasizmi?",
	"⏱ <b>%[1]s</b>, <b>%[2]s</b> hozir bo'sh\n📊 <b>%[3]s</b>\n\n⚡ Tezkor sessiya?",
	"🎯 <b>%[1]s</b>, aniq daraja topildi\n<b>%[2]s</b> hozir kutmoqda\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
	"🔔 <b>%[1]s</b>, yangi speaking partner\n<b>%[2]s</b> hozir onlayn\n📊 <b>%[3]s</b>\n\n⚡ Kirsinmi?",

	// ── Light urgency (no fake FOMO)
	"<b>%[1]s</b>, kechiktirmaylik\n<b>%[2]s</b> boshqa sherik topib ketmasligi mumkin\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
	"🔥 <b>%[1]s</b>, <b>%[2]s</b> hozirgina onlayn chiqdi\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
	"⚡ <b>%[1]s</b> — <b>%[2]s</b> 1 sessiyaga tayyor\n📊 <b>%[3]s</b>\n\n🎤 Sinab ko'ramizmi?",
	"💫 <b>%[1]s</b> — <b>%[2]s</b> 5 daqiqani birga o'tkazmoqchi\n📊 <b>%[3]s</b>\n\n⚡ Qaraysizmi?",
	"⏰ <b>%[1]s</b>, <b>%[2]s</b> kutyapti — ko'p kuttirmang\n📊 <b>%[3]s</b>\n\n⚡ Ulasinmi?",

	// ── Casual / low-effort
	"☕ <b>%[1]s</b>, <b>%[2]s</b> bilan 15 daqiqalik speaking?\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
	"🎧 <b>%[1]s</b> — <b>%[2]s</b> ulanish uchun tayyor\n📊 <b>%[3]s</b>\n\n⚡ Ketdikmi?",
	"🌿 <b>%[1]s</b>, qisqa break?\n<b>%[2]s</b> speaking uchun kutib turibdi\n📊 <b>%[3]s</b>\n\n⚡ 5 daqiqa?",
	"<b>%[1]s</b>, bir daqiqa ajratsangiz bas\n<b>%[2]s</b> speaking uchun kutib turibdi\n📊 <b>%[3]s</b>\n\n⚡ Kirsinmi?",
	"🗨 <b>%[1]s</b>, 5 daqiqa speaking?\n<b>%[2]s</b> bo'sh va tayyor\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
	"<b>%[1]s</b>, aslida kuningiz 5 daqiqa ochilib ketishi mumkin\n<b>%[2]s</b> sherik bo'lishga tayyor\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",

	// ── Identity / aspiration
	"💪 <b>%[1]s</b>, <b>%[2]s</b> kabi har kuni amaliyot qilayotganlar tez o'sadi\n📊 <b>%[3]s</b>\n\n⚡ Qo'shilasizmi?",
	"🎓 <b>%[1]s</b>, <b>%[2]s</b> sessiyaga tayyor\n📊 <b>%[3]s</b>\n\n⚡ Bugungi vazifani bajaring",
	"🚀 <b>%[1]s</b>, <b>%[2]s</b> bilan speaking — tezroq o'sish yo'li\n📊 <b>%[3]s</b>\n\n⚡ Ulasinmi?",
	"🌟 <b>%[1]s</b>, <b>%[2]s</b> kabi speakerlar siz bilan gaplashishni xohlayapti\n📊 <b>%[3]s</b>\n\n⚡ Kirsinmi?",
	"🎯 <b>%[1]s</b>, maqsadga yaqinlashaylik\n<b>%[2]s</b> bilan 1 sessiya — yetarli boshlang'ich\n📊 <b>%[3]s</b>\n\n⚡ Qo'shilasizmi?",

	// ── Straight invitation
	"🎙 <b>%[1]s</b>, speaking sherik topildi\n<b>%[2]s</b> hozir kutmoqda\n📊 <b>%[3]s</b>\n\n⚡ Ulasinmi?",
	"🎤 <b>%[1]s</b>, mikrofon sizning qo'lingizda\n<b>%[2]s</b> eshitishga tayyor\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
	"💬 <b>%[1]s</b> — <b>%[2]s</b> bilan 1v1 speaking\n📊 <b>%[3]s</b>\n\n⚡ Kirsinmi?",
	"🎙 <b>%[1]s</b>, speaking mashg'ulotga chaqiruv\n<b>%[2]s</b> sizga ulanmoqchi\n📊 <b>%[3]s</b>\n\n⚡ Qabul qilasizmi?",
	"📍 <b>%[1]s</b>, hozir aynan <b>%[2]s</b> siz uchun mos sherik\n📊 <b>%[3]s</b>\n\n⚡ Qo'shilasizmi?",

	// ── Question-led
	"<b>%[1]s</b>, hozir vaqtingiz bormi?\n<b>%[2]s</b> speaking uchun tayyor\n📊 <b>%[3]s</b>\n\n🎤 Sinab ko'ramizmi?",
	"<b>%[1]s</b>, tayyor bo'lsangiz kiring\n<b>%[2]s</b> siz bilan amaliyot qilmoqchi\n📊 <b>%[3]s</b>\n\n🎙 Qo'shilaylikmi?",
	"<b>%[1]s</b>, qaytdingizmi? 😊\n<b>%[2]s</b> sizni kutmoqda\n📊 <b>%[3]s</b>\n\n⚡ Hozirmi?",
	"<b>%[1]s</b>, bugun 1 ta sessiyaga tayyormisiz?\n<b>%[2]s</b> tayyor turibdi\n📊 <b>%[3]s</b>\n\n⚡ Kirsinmi?",

	// ── One-liners / compact
	"🎈 <b>%[1]s</b>, <b>%[2]s</b> sizni kutyapti (<b>%[3]s</b>) — kirasizmi?",
	"✨ <b>%[1]s</b> — <b>%[2]s</b> tayyor (<b>%[3]s</b>). Boshlaymizmi?",
	"🎁 <b>%[1]s</b>, <b>%[2]s</b> siz uchun sherik (<b>%[3]s</b>). Qabul qilasizmi?",
	"🔑 <b>%[1]s</b>, <b>%[2]s</b> ulanish uchun tayyor (<b>%[3]s</b>). Ulasinmi?",

	// ── Warm / encouraging
	"🧡 <b>%[1]s</b>, <b>%[2]s</b> siz bilan gaplashishni xohlayapti\n📊 <b>%[3]s</b>\n\n⚡ Tayyormisiz?",
	"🌱 <b>%[1]s</b>, kichik qadam — katta natija\n<b>%[2]s</b> siz bilan boshlashga tayyor\n📊 <b>%[3]s</b>\n\n⚡ Kirsinmi?",
	"💡 <b>%[1]s</b>, bugungi amaliyot vaqti\n<b>%[2]s</b> sizni kutmoqda\n📊 <b>%[3]s</b>\n\n⚡ Kirsinmi?",
	"☀️ <b>%[1]s</b>, speaking amaliyoti uchun vaqt\n<b>%[2]s</b> tayyor\n📊 <b>%[3]s</b>\n\n⚡ Kirasizmi?",
}

// htmlEscape escapes the characters disallowed inside an HTML text node.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// SendMatchInvitation notifies a user that someone is looking for a speaking partner.
// The CTA opens the bot's Mini App directly - or falls back to the public web URL
// if MiniAppURL isn't configured.
//
// recipientName/senderName are displayed in the message - pass first names so the
// greeting reads naturally. Empty strings fall back to generic placeholders.
func SendMatchInvitation(telegramID int64, levelGroup, recipientName, senderName string) bool {
	if Bot == nil {
		return false
	}

	levelName := ""
	switch levelGroup {
	case "basic":
		levelName = "Basic (A1-A2)"
	case "independent":
		levelName = "Independent (B1-B2)"
	case "proficient":
		levelName = "Proficient (C1-C2)"
	default:
		levelName = "barcha darajalar"
	}

	you := strings.TrimSpace(recipientName)
	if you == "" {
		you = "do'stim"
	}
	partner := strings.TrimSpace(senderName)
	if partner == "" {
		partner = "Kimdir"
	}

	tmpl := matchInviteMessages[rand.Intn(len(matchInviteMessages))]
	text := fmt.Sprintf(tmpl, htmlEscape(you), htmlEscape(partner), levelName)

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("🎤 Hozir kirish →", &tele.WebApp{URL: webURL})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send match invitation")
		return false
	}
	return true
}

// SendMatchNotification notifies user that a match is ready.
func SendMatchNotification(telegramID int64, partnerName string, sessionID string) bool {
	if Bot == nil {
		return false
	}

	sessionURL := fmt.Sprintf("%s/speak/session/%s", config.App.MiniAppURL, sessionID)

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("Hozir kirish →", &tele.WebApp{URL: sessionURL})
	markup.Inline(markup.Row(btn))

	text := fmt.Sprintf("<b>%s</b> kutmoqda! Session boshlandi.", partnerName)

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil {
		log.Error().Err(err).Msg("Failed to send match notification")
		return false
	}
	return true
}

// SendInactivityReminder reminds inactive users to come back.
func SendInactivityReminder(telegramID int64, daysInactive int) bool {
	if Bot == nil {
		return false
	}

	text := fmt.Sprintf(
		"👋 Salom! Sizni %d kundan beri ko'rmadik.\n\n"+
			"Speaking qilmasangiz - unut bo'ladi! 🧠\n"+
			"Bugun 1 ta suhbat qiling va streak'ingizni qayta boshlang! 🔥",
		daysInactive,
	)

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("Qaytish →", &tele.WebApp{URL: config.App.MiniAppURL})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send inactivity reminder")
		return false
	}
	return true
}

// SendAdminReplyNotification notifies a user that the admin replied to
// one of their support tickets. Button opens the support page inside
// the Mini App.
func SendAdminReplyNotification(telegramID int64, reply string) bool {
	if Bot == nil {
		return false
	}

	// Keep the preview short so the Telegram message stays scannable.
	preview := strings.TrimSpace(reply)
	if len(preview) > 220 {
		preview = preview[:220] + "…"
	}

	text := "💬 <b>Admin sizga javob berdi</b>\n\n" +
		htmlEscape(preview) +
		"\n\n👇 To'liq javobni ilovada ko'ring."

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("📬 Javobni ochish", &tele.WebApp{URL: webURL + "/profile/support"})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send admin reply notification")
		return false
	}
	return true
}

// SendStreakReminder - ertalab streak eslatmasi.
// Streaki bor userlarga davom ettirishni, yo'qlarga boshlashni eslatadi.
func SendStreakReminder(telegramID int64, currentStreak int) bool {
	if Bot == nil {
		return false
	}

	var text string
	if currentStreak > 0 {
		text = fmt.Sprintf(
			"🔥 Sizda <b>%d kunlik streak</b> bor!\n\n"+
				"Bugun ham kamida 1 ta sessiya o'tkazing - streakingiz uzilmasin!\n\n"+
				"💪 Har kuni gaplashing - natija seziladi.",
			currentStreak,
		)
	} else {
		text = "☀️ Xayrli tong!\n\n" +
			"Bugun 1 ta speaking sessiya = yangi streak boshlanishi 🔥\n\n" +
			"Har kuni 15 daqiqa gaplashing - 1 haftada farqni sezasiz!"
	}

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("🎤 Sessiya boshlash", &tele.WebApp{URL: webURL})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil && !isPermanentTelegramError(err) {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send streak reminder")
	}
	return err == nil
}

// SendWeeklyReport - har dushanba haftalik hisobot.
func SendWeeklyReport(telegramID int64, sessions int, minutes int, streak int, rank int) bool {
	if Bot == nil {
		return false
	}

	rankText := ""
	if rank > 0 {
		rankText = fmt.Sprintf("\n🏆 Leaderboardda: <b>#%d</b> o'rin", rank)
	}

	text := fmt.Sprintf(
		"📊 <b>Haftalik hisobotingiz</b>\n\n"+
			"🗣 Sessiyalar: <b>%d ta</b>\n"+
			"⏱ Gaplashgan vaqt: <b>%d daqiqa</b>\n"+
			"🔥 Streak: <b>%d kun</b>%s\n\n"+
			"Yangi hafta - yangi maqsad! 💪",
		sessions, minutes, streak, rankText,
	)

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("📈 Batafsil ko'rish", &tele.WebApp{URL: webURL + "/profile"})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil && !isPermanentTelegramError(err) {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send weekly report")
	}
	return err == nil
}

// SendWeeklyWinnerNotif - Monday morning message to a top-3 weekly
// leaderboard finisher announcing the +minutes and +AI-test bonuses.
// rank is 1/2/3 (mapped to 🥇🥈🥉). Returns true on successful send.
func SendWeeklyWinnerNotif(telegramID int64, rank int, bonusMinutes int, bonusAITests int) bool {
	if Bot == nil {
		return false
	}

	medal := map[int]string{1: "🥇", 2: "🥈", 3: "🥉"}[rank]
	if medal == "" {
		medal = "🏆"
	}

	text := fmt.Sprintf(
		"%s <b>Tabriklaymiz!</b>\n\n"+
			"O'tgan haftada siz leaderboard'da <b>#%d o'rin</b> egalladingiz!\n\n"+
			"🎁 Siz bir hafta davomida quyidagi bonuslarni olasiz:\n"+
			"• Kunlik limitga <b>+%d daqiqa</b> qo'shildi\n"+
			"• AI bilan mock qilishga <b>+%d qo'shimcha imkoniyat</b>\n\n"+
			"Yangi hafta - yangi cho'qqilar! 💪",
		medal, rank, bonusMinutes, bonusAITests,
	)

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("🏆 Leaderboard", &tele.WebApp{URL: webURL + "/leaderboard"})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil && !isPermanentTelegramError(err) {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send weekly winner notif")
	}
	return err == nil
}

// SendLeaderboardChallenge - user ko'tarilganda yoki yaqinida raqobatchi borligini bildiradi.
func SendLeaderboardChallenge(telegramID int64, currentRank int, nextRank int, minutesNeeded int) bool {
	if Bot == nil {
		return false
	}

	text := fmt.Sprintf(
		"🏆 Siz leaderboardda <b>#%d</b> o'rindasiz!\n\n"+
			"Yana <b>%d daqiqa</b> gaplashing - <b>#%d</b> o'ringa ko'tarilasiz! 🚀\n\n"+
			"Bugun 1 ta sessiya bilan maqsadga yeting!",
		currentRank, minutesNeeded, nextRank,
	)

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("🎯 Sessiya boshlash", &tele.WebApp{URL: webURL})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil && !isPermanentTelegramError(err) {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send leaderboard challenge")
	}
	return err == nil
}

// SendOnlineReminder - kechqurun faol vaqtda "hozir X kishi online" eslatmasi.
func SendOnlineReminder(telegramID int64, onlineCount int) bool {
	if Bot == nil {
		return false
	}

	text := fmt.Sprintf(
		"🟢 Hozir <b>%d kishi</b> online - speaking partner topish oson!\n\n"+
			"Kechqurun eng yaxshi vaqt - hozir kiring va gaplashing! 🎤",
		onlineCount,
	)

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("🎤 Hozir kirish", &tele.WebApp{URL: webURL})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil && !isPermanentTelegramError(err) {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send online reminder")
	}
	return err == nil
}

// SendPremiumActivated notifies user that premium is activated.
func SendPremiumActivated(telegramID int64) bool {
	if Bot == nil {
		return false
	}

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, "✅ Premium faollashtirildi! Endi cheksiz gaplashishingiz mumkin.")
	if err != nil {
		log.Error().Err(err).Msg("Failed to send premium notification")
		return false
	}
	return true
}

// SendPremiumGranted notifies a user that an admin manually activated
// (or extended) their premium. Called from the admin grant-premium flow.
func SendPremiumGranted(telegramID int64, months int, expiresAt time.Time) bool {
	if Bot == nil {
		return false
	}

	expires := expiresAt.In(time.FixedZone("UZT", 5*60*60)).Format("02.01.2006")
	text := fmt.Sprintf(
		"👑 <b>Premium faollashtirildi!</b>\n\n"+
			"📅 Muddat: <b>%d oy</b>\n"+
			"⏳ Tugash sanasi: <b>%s</b>\n\n"+
			"Endi cheksiz daqiqalar va barcha imkoniyatlar sizniki!",
		months, expires,
	)

	markup := &tele.ReplyMarkup{}
	webURL := config.App.MiniAppURL
	if webURL == "" {
		webURL = "https://speak-up.uz"
	}
	btn := markup.WebApp("🚀 Ilovaga kirish", &tele.WebApp{URL: webURL})
	markup.Inline(markup.Row(btn))

	recipient := &tele.User{ID: telegramID}
	_, err := SendWithRetry(recipient, text, markup, tele.ModeHTML)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", telegramID).Msg("Failed to send premium granted notification")
		return false
	}
	return true
}

// --- Friends & scheduled appointments ---

// SendFriendRequest tells someone a speaking partner wants to connect.
func SendFriendRequest(telegramID int64, senderName string) bool {
	if Bot == nil {
		return false
	}

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("Ko'rish →", &tele.WebApp{URL: config.App.MiniAppURL + "/profile/friends"})
	markup.Inline(markup.Row(btn))

	text := fmt.Sprintf(
		"🤝 <b>%s</b> sizni do'stlar ro'yxatiga qo'shmoqchi.\n\n"+
			"Qabul qilsangiz - bir-biringiz bilan speaking vaqtini kelishib olasiz.",
		senderName,
	)

	if _, err := SendWithRetry(&tele.User{ID: telegramID}, text, markup, tele.ModeHTML); err != nil {
		log.Error().Err(err).Msg("Failed to send friend request notification")
		return false
	}
	return true
}

// SendFriendAccepted closes the loop for whoever sent the request.
func SendFriendAccepted(telegramID int64, friendName string) bool {
	if Bot == nil {
		return false
	}

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("Do'stlarim →", &tele.WebApp{URL: config.App.MiniAppURL + "/profile/friends"})
	markup.Inline(markup.Row(btn))

	text := fmt.Sprintf(
		"✅ <b>%s</b> do'stlik so'rovingizni qabul qildi!\n\n"+
			"Endi u bilan speaking vaqtini rejalashtirsangiz bo'ladi.",
		friendName,
	)

	if _, err := SendWithRetry(&tele.User{ID: telegramID}, text, markup, tele.ModeHTML); err != nil {
		log.Error().Err(err).Msg("Failed to send friend accepted notification")
		return false
	}
	return true
}

// SendScheduleInvite announces a newly booked appointment.
func SendScheduleInvite(telegramID int64, organiserName string, at time.Time, note string) bool {
	if Bot == nil {
		return false
	}

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("Ko'rish →", &tele.WebApp{URL: config.App.MiniAppURL + "/speak"})
	markup.Inline(markup.Row(btn))

	text := fmt.Sprintf(
		"📅 <b>%s</b> siz bilan speaking rejalashtirdi.\n\n"+
			"🕗 <b>%s</b>",
		organiserName, formatTashkent(at),
	)
	if note != "" {
		text += fmt.Sprintf("\n💬 %s", note)
	}
	text += "\n\nBelgilangan vaqtda ilovaga kiring - 10 daqiqa ichida qo'shilishingiz kerak."

	if _, err := SendWithRetry(&tele.User{ID: telegramID}, text, markup, tele.ModeHTML); err != nil {
		log.Error().Err(err).Msg("Failed to send schedule invite")
		return false
	}
	return true
}

// SendScheduleAnswer tells the organiser whether their invite was taken.
func SendScheduleAnswer(telegramID int64, partnerName string, at time.Time, accepted bool) bool {
	if Bot == nil {
		return false
	}

	var text string
	if accepted {
		text = fmt.Sprintf("✅ <b>%s</b> %s dagi speakingni tasdiqladi.",
			partnerName, formatTashkent(at))
	} else {
		text = fmt.Sprintf("❌ <b>%s</b> %s dagi speakingga kela olmaydi.\n\n"+
			"Boshqa vaqt taklif qilib ko'ring.", partnerName, formatTashkent(at))
	}

	if _, err := SendWithRetry(&tele.User{ID: telegramID}, text, nil, tele.ModeHTML); err != nil {
		log.Error().Err(err).Msg("Failed to send schedule answer")
		return false
	}
	return true
}

// SendScheduleCancelled tells the other side an appointment is off.
func SendScheduleCancelled(telegramID int64, byName string, at time.Time) bool {
	if Bot == nil {
		return false
	}

	text := fmt.Sprintf("🚫 <b>%s</b> %s dagi speakingni bekor qildi.",
		byName, formatTashkent(at))

	if _, err := SendWithRetry(&tele.User{ID: telegramID}, text, nil, tele.ModeHTML); err != nil {
		log.Error().Err(err).Msg("Failed to send schedule cancellation")
		return false
	}
	return true
}

// SendScheduleReminder is the "starting soon" nudge.
func SendScheduleReminder(telegramID int64, partnerName string, minutes int) bool {
	if Bot == nil {
		return false
	}

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("Tayyorlanish →", &tele.WebApp{URL: config.App.MiniAppURL + "/speak"})
	markup.Inline(markup.Row(btn))

	text := fmt.Sprintf(
		"⏰ <b>%d daqiqadan keyin</b> <b>%s</b> bilan speakingingiz bor.\n\n"+
			"Tinch joy toping va naushnikni tayyorlang 🎧",
		minutes, partnerName,
	)

	if _, err := SendWithRetry(&tele.User{ID: telegramID}, text, markup, tele.ModeHTML); err != nil {
		log.Error().Err(err).Msg("Failed to send schedule reminder")
		return false
	}
	return true
}

// SendScheduleStarting is the message at the agreed minute. The ten
// minute limit is stated outright: a user who thinks they can wander in
// half an hour later is the one who ends up disappointed.
func SendScheduleStarting(telegramID int64, partnerName string) bool {
	if Bot == nil {
		return false
	}

	markup := &tele.ReplyMarkup{}
	btn := markup.WebApp("Qo'shilish →", &tele.WebApp{URL: config.App.MiniAppURL + "/speak"})
	markup.Inline(markup.Row(btn))

	text := fmt.Sprintf(
		"🔔 <b>Vaqt keldi!</b>\n\n<b>%s</b> sizni kutmoqda.\n"+
			"Qo'shilish uchun <b>10 daqiqa</b> vaqtingiz bor.",
		partnerName,
	)

	if _, err := SendWithRetry(&tele.User{ID: telegramID}, text, markup, tele.ModeHTML); err != nil {
		log.Error().Err(err).Msg("Failed to send schedule start notification")
		return false
	}
	return true
}

// formatTashkent renders an instant in the only timezone our users are
// in. Sending UTC to somebody standing in Tashkent is how appointments
// get missed by five hours.
func formatTashkent(t time.Time) string {
	loc := time.FixedZone("UTC+5", 5*60*60)
	local := t.In(loc)
	return fmt.Sprintf("%02d.%02d %02d:%02d",
		local.Day(), int(local.Month()), local.Hour(), local.Minute())
}
