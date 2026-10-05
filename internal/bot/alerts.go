package bot

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/safego"
	tele "gopkg.in/telebot.v3"
)

// alertDedupe rate-limits identical alerts so a tight panic loop in a
// background task can't spam the admin chat with thousands of messages
// per second. Same `key` within the cooldown window is suppressed.
var (
	alertDedupe   = map[string]time.Time{}
	alertDedupeMu sync.Mutex
)

const alertCooldown = 5 * time.Minute

// AlertAdmin posts a critical-error message to every chat ID listed in
// ADMIN_ALERT_CHAT_IDS. Best-effort: if the bot or env var is missing,
// or if Telegram returns an error, we fall through to a plain log.
//
// Use this for things you'd otherwise miss without monitoring:
//   - Recovered panics (wired through safego.AlertHook)
//   - Scheduler failures
//   - Database connectivity loss in long-running paths
//   - Anything that should wake an admin up
func AlertAdmin(title string, body string) {
	if Bot == nil || len(config.App.AdminAlertChatIDs) == 0 {
		log.Warn().
			Str("alert_title", title).
			Msg("AlertAdmin: bot or admin chat IDs not configured")
		return
	}

	// Dedupe identical alerts inside the cooldown window.
	dedupeKey := title
	alertDedupeMu.Lock()
	if last, ok := alertDedupe[dedupeKey]; ok && time.Since(last) < alertCooldown {
		alertDedupeMu.Unlock()
		return
	}
	alertDedupe[dedupeKey] = time.Now()
	// Garbage collect old entries periodically.
	if len(alertDedupe) > 200 {
		cutoff := time.Now().Add(-alertCooldown)
		for k, t := range alertDedupe {
			if t.Before(cutoff) {
				delete(alertDedupe, k)
			}
		}
	}
	alertDedupeMu.Unlock()

	host, _ := osHostname()
	text := fmt.Sprintf(
		"🚨 <b>%s</b>\n\n<b>Host:</b> %s\n<b>Time:</b> %s\n\n<pre>%s</pre>",
		htmlEscape(title),
		htmlEscape(host),
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		htmlEscape(strings.TrimSpace(body)),
	)

	for _, chatID := range config.App.AdminAlertChatIDs {
		recipient := &tele.User{ID: chatID}
		if _, err := SendWithRetry(recipient, text, tele.ModeHTML); err != nil {
			log.Warn().Err(err).Int64("chat_id", chatID).Msg("AlertAdmin: send failed")
		}
	}
}

// InstallAlertHook wires bot.AlertAdmin into safego so any panic
// recovered by safego.Go() / safego.Run() is also fanned out to the
// admin chat. Called once from InitBot().
func InstallAlertHook() {
	safego.AlertHook = AlertAdmin
}

// osHostname reads the container/host name without importing os in
// every call site. Errors fall back to "unknown".
func osHostname() (string, error) {
	return readHostname()
}
