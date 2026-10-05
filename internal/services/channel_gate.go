package services

import (
	"context"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
)

// Mandatory channel subscription ("force subscribe") gate.
//
// Why it is built exactly this way — each rule below exists because the
// naive version of it is broken:
//
//  1. Telegram's getChatMember is authoritative ONLY while the bot is an
//     admin of the channel. It is, so member/administrator/creator means
//     subscribed. (See bot.IsChannelMember.)
//
//  2. A user can leave the channel at any time, so subscription is a
//     LIVE question, never a one-off flag stored on the user row. We
//     re-ask Telegram, cached.
//
//  3. Telegram's chat_member push updates are known to be lossy (a
//     documented share of join events is never delivered), so we never
//     rely on being told about a join — we ask, on a short TTL.
//
//  4. This host's route to Telegram is intermittently flaky. A failed
//     check must NEVER lock the entire user base out of the product, so
//     the gate falls back to the last cached answer and, absent one,
//     fails OPEN (lets the user through) and logs it.
const (
	// Subscribed answers are trusted for a while — the cost of a stale
	// "yes" is that someone who just left keeps access for a few minutes.
	channelSubTTL = 10 * time.Minute

	// Negative answers expire fast so a user who just subscribed gets in
	// almost immediately, even if they never press "check again".
	channelUnsubTTL = 30 * time.Second
)

func channelSubKey(tgID int64) string {
	return "chan_sub:" + strconv.FormatInt(tgID, 10)
}

// RequiredChannelUsername is the channel users must join, without "@".
func RequiredChannelUsername() string {
	return config.App.RequiredChannel
}

// ChannelGateEnabled reports whether the gate is switched on at all.
func ChannelGateEnabled() bool {
	return config.App.ChannelGateEnabled
}

// IsChannelSubscribed answers "may this user use the product right now?".
//
// Fail-open by design: if Telegram can't be reached and we have no cached
// answer, the user is let through. Blocking everyone during a network
// blip is a far worse failure than briefly letting an unsubscribed user in.
func IsChannelSubscribed(tgID int64) bool {
	if !config.App.ChannelGateEnabled {
		return true
	}
	// No Telegram identity → nothing to check against. Don't punish them.
	if tgID == 0 {
		return true
	}

	ctx := context.Background()
	key := channelSubKey(tgID)

	// Cached answer (also the fallback when Telegram is unreachable).
	if v, err := database.Redis.Get(ctx, key).Result(); err == nil {
		return v == "1"
	}

	subscribed, err := bot.IsChannelMember(tgID, config.App.RequiredChannel)
	if err != nil {
		log.Warn().Err(err).Int64("tg_id", tgID).
			Msg("channel gate: check failed and no cached answer — failing open")
		return true
	}

	cacheChannelAnswer(ctx, key, subscribed)
	return subscribed
}

// CheckChannelSubscriptionFresh bypasses the cache. This backs the explicit
// "I've subscribed — check again" button, so a user never has to sit out a
// TTL after joining. The error is surfaced (not swallowed) so the UI can say
// "couldn't verify, try again" rather than silently claiming they're not
// subscribed.
func CheckChannelSubscriptionFresh(tgID int64) (bool, error) {
	if !config.App.ChannelGateEnabled || tgID == 0 {
		return true, nil
	}

	ctx := context.Background()
	key := channelSubKey(tgID)

	subscribed, err := bot.IsChannelMember(tgID, config.App.RequiredChannel)
	if err != nil {
		// Telegram unreachable. If we were previously told they are in,
		// that answer is better than an error screen.
		if cachedYes(ctx, key) {
			return true, nil
		}
		return false, err
	}

	// The poll and the push disagree, and the push is the one to believe.
	//
	// getChatMember keeps answering "left" for one to three minutes after
	// a real join, while chat_member arrives at once. A user who joins
	// and immediately presses "check" hits exactly that window - and
	// before this, the stale "no" was also written over the push's "yes",
	// so the gate stayed shut until the negative cache expired. That is
	// the "I had to close and reopen the app" report.
	if !subscribed && cachedYes(ctx, key) {
		log.Info().Int64("tg_id", tgID).
			Msg("channel gate: poll says left but a push said joined - trusting the push")
		return true, nil
	}

	cacheChannelAnswer(ctx, key, subscribed)
	return subscribed, nil
}

// cachedYes reports whether we hold a positive answer for this user.
// Only ever written by a confirmed membership - a poll that said yes, or
// a chat_member push - so trusting it cannot let a stranger through.
func cachedYes(ctx context.Context, key string) bool {
	v, err := database.Redis.Get(ctx, key).Result()
	return err == nil && v == "1"
}

// OnChannelUnlocked is called when a push tells us a user has just
// subscribed, so the open Mini App can drop the gate without the user
// pressing anything. Wired to the WebSocket layer in main.go (services
// must not import ws, which imports services).
var OnChannelUnlocked func(tgID int64)

// ApplyChannelMembership records a membership change that Telegram
// PUSHED to us, rather than one we went looking for.
//
// This is what fixes the "I've joined, why won't it let me in" report:
// getChatMember keeps answering "left" for a minute or three after a
// real join, so the polled path alone leaves the user staring at the
// gate. The push lands in the same cache the gate reads, so the very
// next check - or the WebSocket nudge below - lets them through.
func ApplyChannelMembership(tgID int64, subscribed bool) {
	if tgID == 0 {
		return
	}

	cacheChannelAnswer(context.Background(), channelSubKey(tgID), subscribed)

	log.Info().
		Int64("tg_id", tgID).
		Bool("subscribed", subscribed).
		Msg("channel gate: membership updated from push")

	if subscribed && OnChannelUnlocked != nil {
		OnChannelUnlocked(tgID)
	}
}

func cacheChannelAnswer(ctx context.Context, key string, subscribed bool) {
	val, ttl := "0", channelUnsubTTL
	if subscribed {
		val, ttl = "1", channelSubTTL
	}
	database.Redis.Set(ctx, key, val, ttl)
}
