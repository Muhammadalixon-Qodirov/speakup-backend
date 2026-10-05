package bot

import (
	"errors"
	"strings"

	"github.com/rs/zerolog/log"
	tele "gopkg.in/telebot.v3"
)

// OnChannelMembership is called the instant Telegram tells us a user
// joined or left the required channel.
//
// It exists because getChatMember LAGS: the Bot API keeps answering
// "left" for one to three minutes after a real join. Users experienced
// that as the app refusing to believe they had subscribed, no matter how
// many times they pressed "check". The push arrives immediately, so it
// is what actually lifts the gate; the polled check stays as the
// fallback for the joins Telegram forgets to push.
//
// Wired in main.go to services.ApplyChannelMembership - bot must not
// import services, which imports bot.
var OnChannelMembership func(tgID int64, subscribed bool)

// MembershipRoleSubscribed reports whether a chat member role counts as
// being subscribed. Kept next to IsChannelMember so the two can never
// drift apart.
func MembershipRoleSubscribed(role string) bool {
	switch role {
	case "member", "administrator", "creator":
		return true
	default:
		return false
	}
}

// IsChannelMember asks Telegram whether the given user is a member of
// the public channel. Used by the "subscribe to @speakup_app → +10 min"
// flow - we verify before granting so users can't just click the
// "Tekshirish" button without actually joining.
//
// Requirements:
//  1. The bot must be an admin (or at least a member) of the channel.
//     Public visibility alone isn't enough - Telegram returns
//     "CHAT_ADMIN_REQUIRED" otherwise.
//  2. channelUsername is passed WITHOUT the leading "@" (we add it).
//
// Returns (subscribed, error). If Telegram says "user not found" or
// "participant not found", we return (false, nil) - that's the common
// "not subscribed yet" path and shouldn't surface as an error.
func IsChannelMember(telegramID int64, channelUsername string) (bool, error) {
	if Bot == nil {
		return false, errors.New("bot not initialised")
	}

	username := strings.TrimPrefix(channelUsername, "@")
	chat, err := Bot.ChatByUsername("@" + username)
	if err != nil {
		log.Warn().Err(err).Str("channel", username).Msg("IsChannelMember: ChatByUsername failed")
		return false, err
	}

	member, err := Bot.ChatMemberOf(chat, &tele.User{ID: telegramID})
	if err != nil {
		msg := strings.ToLower(err.Error())
		log.Warn().
			Err(err).
			Int64("tg_id", telegramID).
			Int64("chat_id", chat.ID).
			Str("channel", username).
			Msg("IsChannelMember: ChatMemberOf error")
		// "user not found" / "participant not found" → simply not a member.
		if strings.Contains(msg, "not found") ||
			strings.Contains(msg, "participant not found") ||
			strings.Contains(msg, "user_not_participant") {
			return false, nil
		}
		return false, err
	}

	role := string(member.Role)
	log.Info().
		Int64("tg_id", telegramID).
		Str("channel", username).
		Str("role", role).
		Msg("IsChannelMember: got member status")

	switch role {
	case "member", "administrator", "creator":
		return true, nil
	default:
		// "left" or "kicked" - user has visited but isn't a member now.
		return false, nil
	}
}
