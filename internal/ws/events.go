package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/services"
)

// HandleEvent routes incoming WebSocket messages to the right handler.
func HandleEvent(c *Client, msg *Message) {
	switch msg.Event {
	case "join_queue":
		handleJoinQueue(c, msg.Data)
	case "leave_queue":
		handleLeaveQueue(c)
	// Private teacher rooms. Same tap-to-speak experience as the public
	// queue, but the pool is one class and the minutes are free.
	case "room_join_queue":
		handleRoomJoinQueue(c, msg.Data)
	case "room_leave_queue":
		handleRoomLeaveQueue(c)
	case "room_state":
		handleRoomState(c, msg.Data)
	case "room_start_round":
		handleRoomStartRound(c, msg.Data)
	case "room_pair":
		handleRoomPair(c, msg.Data)
	// Scheduled appointments between friends: both sides stand in the
	// doorway for ten minutes and the call opens when the second arrives.
	case "scheduled_join":
		handleScheduledJoin(c, msg.Data)
	case "scheduled_leave":
		handleScheduledLeave(c, msg.Data)
	// Teacher listen-in. The students always see it happening - there is
	// no silent mode (see services/room_listen.go).
	case "room_listen_start":
		handleRoomListenStart(c, msg.Data)
	case "room_listen_stop":
		handleRoomListenStop(c)
	case "listen_offer":
		handleListenSignaling(c, "listen_offer", msg.Data)
	case "listen_answer":
		handleListenSignaling(c, "listen_answer", msg.Data)
	case "listen_ice":
		handleListenSignaling(c, "listen_ice", msg.Data)
	case "session_offer":
		handleSignaling(c, "session_offer", msg.Data)
	case "session_answer":
		handleSignaling(c, "session_answer", msg.Data)
	case "ice_candidate":
		handleSignaling(c, "ice_candidate", msg.Data)
	case "session_end":
		handleSessionEnd(c, msg.Data)
	case "chat_message":
		handleChatMessage(c, msg.Data)
	case "mic_state":
		handleMicState(c, msg.Data)
	case "topic_change":
		handleTopicChange(c, msg.Data)
	case "game_invite":
		handleGameInvite(c, msg.Data)
	case "game_accept":
		handleGameAccept(c, msg.Data)
	case "game_decline":
		handleGameDecline(c, msg.Data)
	case "game_move":
		handleGameMove(c, msg.Data)
	case "game_abort":
		handleGameAbort(c, msg.Data)
	case "ping":
		// Application-level heartbeat from the client. Used purely
		// to keep NAT mappings alive on mobile networks - we reply
		// with a pong so the client can also detect a dead socket
		// when no response arrives within the expected window.
		c.SendJSON("pong", map[string]interface{}{})
	default:
		log.Warn().Str("event", msg.Event).Msg("Unknown WebSocket event")
	}
}

// HandleDisconnect is called when a client disconnects (tab closed,
// network lost, phone died, etc.). We make sure the user doesn't leave
// their partner hanging: any active session is force-ended and the
// partner receives a `session_ended` event so their UI can navigate to
// the rate page automatically.
//
// IMPORTANT: a single user can hold MULTIPLE concurrent sockets (extra
// browser tab, Mini App reload during a session, the iOS WebView spin-up
// before the old one tears down). Killing the session on the FIRST tab
// close kicks the user off their own match - they see "ulanmoqda"
// forever even though they're still "really" connected on another
// socket. So we only end sessions when the disconnecting socket was the
// user's LAST one.
func HandleDisconnect(c *Client) {
	services.RemoveFromQueue(c.UserID)

	// Also drop out of any room queue, and refresh that room's live
	// panel so the teacher sees the student go offline immediately.
	if roomID := services.CurrentRoomQueue(c.UserID); roomID != "" {
		services.RemoveFromRoomQueue(c.UserID)
		if rid, err := uuid.Parse(roomID); err == nil {
			BroadcastRoomState(rid)
		}
	}

	// If this was a teacher who was listening in, detach them and clear
	// the "teacher is listening" indicator on both students' screens -
	// otherwise a closed tab would leave it showing for the whole call.
	detachListenerOnDisconnect(c.UserID)

	// Other live sockets for the same user? If yes, skip session end -
	// the user is still on the platform via another tab/connection.
	others := 0
	for _, client := range H.GetClientsByUserID(c.UserID) {
		if client != c {
			others++
		}
	}
	if others == 0 {
		endActiveSessionsOnDisconnect(c.UserID)
		// Abort any in-flight mini-game so the partner isn't left
		// staring at a frozen game board.
		handleGameDisconnect(c.UserID)
	}

	// Broadcast updated queue size so other clients can stop the
	// Speak Now pulse if this user was the last one searching.
	uid := c.UserID
	safego.Go("pulseQueueDisconnect", func() {
		pulseQueueState(uid, "", "")
	})
}

// endActiveSessionsOnDisconnect atomically ends every `active` session
// the disconnecting user belongs to and notifies the partner (if online).
// Called from the WS disconnect path, so it must not block - but it's
// bounded because a user can only be in one active session at a time.
func endActiveSessionsOnDisconnect(userID string) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return
	}

	var sessions []models.Session
	err = database.DB.
		Where("(user1_id = ? OR user2_id = ?) AND status = ?", uid, uid, "active").
		Find(&sessions).Error
	if err != nil || len(sessions) == 0 {
		return
	}

	for _, s := range sessions {
		sessionID := s.ID.String()

		// Atomic end (no-op if another path already ended it).
		ended, err := services.EndSession(sessionID, userID)
		if err != nil || ended == nil {
			continue
		}

		// Tear down any mini-game tied to this session too.
		handleSessionGamesCleanup(sessionID)

		// Detach any listening teacher and tell them why the audio stopped.
		detachListenersOnSessionEnd(sessionID)

		minutes := ended.DurationMinutes()

		// Determine partner + notify them.
		partnerID := ended.User2ID.String()
		if partnerID == userID {
			partnerID = ended.User1ID.String()
		}
		if partnerID == "" {
			continue
		}

		// Room minutes are free: they go into the non-billing window and
		// the room's own report, never against the daily limit.
		if ended.RoomID != nil {
			creditRoomSession(ended, sessionID, minutes)
		} else {
			services.IncrementUsage(userID, minutes)
			services.IncrementUsage(partnerID, minutes)
		}

		if partner := H.GetClientByUserID(partnerID); partner != nil {
			partner.SendJSON("session_ended", map[string]interface{}{
				"session_id":       sessionID,
				"duration_minutes": minutes,
				"ended_by":         "partner",
				"reason":           "disconnected",
				"room_id":          sessionRoomIDValue(ended),
			})
		}

		log.Info().
			Str("session_id", sessionID).
			Str("user_id", userID).
			Int("duration_min", minutes).
			Msg("Session auto-ended on disconnect")
	}
}

// --- Event Handlers ---

type JoinQueueData struct {
	Level      string `json:"level"`       // "basic" | "independent" | "proficient" | ""
	GenderPref string `json:"gender_pref"` // "same" | "mixed"
}

func handleJoinQueue(c *Client, data json.RawMessage) {
	var filters JoinQueueData
	json.Unmarshal(data, &filters)

	// A user waits in exactly one pool. Entering the public queue drops
	// any room queue membership, mirroring what AddToRoomQueue does in
	// the other direction - otherwise a student could be pulled into a
	// public match in the middle of their lesson.
	services.RemoveFromRoomQueue(c.UserID)

	userID, _ := uuid.Parse(c.UserID)

	// Check daily limit
	canSpeak, minutesLeft := services.CheckDailyLimit(userID)
	if !canSpeak {
		c.SendJSON("limit_reached", map[string]int{"minutes_left": minutesLeft})
		return
	}

	// Load the joining user's profile so we can snapshot gender + interests
	// into the queue entry (lets FindMatch score without extra DB hits).
	var me models.User
	if err := database.DB.First(&me, "id = ?", userID).Error; err != nil {
		log.Error().Err(err).Msg("handleJoinQueue: failed to load user")
		return
	}

	// Mandatory channel subscription. Enforced HERE, at the socket, and not
	// only in the UI: the queue is reachable by anyone who can open a
	// WebSocket, so a client-side modal alone would be trivially bypassed.
	// Fails open on a Telegram outage - see services.IsChannelSubscribed.
	if !services.IsChannelSubscribed(me.TelegramID) {
		c.SendJSON("channel_subscription_required", map[string]string{
			"channel": services.RequiredChannelUsername(),
		})
		return
	}

	myGender := ""
	if me.Gender != nil {
		myGender = *me.Gender
	}
	myInterests := []string(me.Interests)

	// A user whose own gender we never recorded cannot ask for a
	// same-gender partner: the rule needs both sides to be known, so
	// "same" would quietly make them unmatchable against the entire
	// queue - and "same" is the client's default. Older accounts predate
	// the onboarding step that collects it, so this is a real population,
	// not a theoretical one. Their preference is relaxed to "mixed";
	// everyone who DID ask for same-gender is still refused a partner of
	// unknown gender, so no promise is broken in the other direction.
	if myGender == "" && filters.GenderPref == "same" {
		log.Debug().
			Str("user_id", c.UserID).
			Msg("join_queue: unknown gender, relaxing same-gender preference")
		filters.GenderPref = "mixed"
	}

	// Add to queue with full profile snapshot.
	services.AddToQueue(
		c.UserID,
		filters.Level,
		myGender,
		filters.GenderPref,
		myInterests,
	)

	// Try to find match immediately
	matchedUserID, err := services.FindMatchFull(
		c.UserID,
		filters.Level,
		myGender,
		filters.GenderPref,
		myInterests,
	)
	if err != nil || matchedUserID == "" {
		c.SendJSON("queued", map[string]interface{}{
			"message": "Suhbatdosh qidirilmoqda...",
		})
		uid := c.UserID
		level := filters.Level
		// Real-time pulse: ping every other online client so their
		// "Speak Now" button keeps shaking until this user matches or
		// leaves. queue_size>0 in the broadcast → animation on.
		safego.Go("livePartnerPulse", func() {
			pulseQueueState(uid, me.FirstName, level)
		})
		safego.Go("sendMatchInvitations", func() {
			sendMatchInvitations(uid, level)
		})
		return
	}

	// Match found! Create session and notify both users
	createAndNotifySession(c.UserID, matchedUserID)
}

const (
	// Sender har 20 daqiqada faqat 1 marta broadcast trigger qila oladi.
	// Tighter than the recipient gap so a rapid join/leave loop from one
	// user can't blast the whole active pool.
	notifySenderCooldown = 20 * time.Minute
	// Recipientga kuniga max 6 ta xabar (90 daq gap × ~16 faol soat).
	// Above this, open rate collapses and block rate spikes.
	dailyMax = 6
	// Ikki xabar orasida kamida 90 daqiqa bo'lishi kerak - messenger
	// notification best-practice sweet spot.
	minGap = 90 * time.Minute
	// Bitta sender nomi kuniga bitta recipientda max 2 marta ko'rinadi -
	// ko'paytirsa spamdek tuyuladi.
	maxPerSender = 2
)

// canNotifyUser - recipientga hozir xabar yuborish mumkinmi?
//
//  1. Gap: oxirgi xabardan 90 daqiqa o'tdimi?
//  2. Sender: bu sender bugun bu recipientga 2 martadan oshmaganmi?
//  3. Kunlik: recipient bugun 6 ta limitga yetmaganmi?
//
// Agar biror qoida rad etsa - hech qanday counter/key o'zgarmaydi.
func canNotifyUser(senderID, recipientID string, notifType string, _ *time.Time) bool {
	ctx := context.Background()

	// 1. Gap tekshiruvi (oldin tekshiriladi, hech narsa o'zgartirmaydi)
	gapKey := fmt.Sprintf("notif_gap:%s:%s", notifType, recipientID)
	if exists, _ := database.Redis.Exists(ctx, gapKey).Result(); exists > 0 {
		return false
	}

	// 2. Sender-per-recipient kunlik limit tekshiruvi
	senderKey := fmt.Sprintf("notif_sd:%s:%s->%s", notifType, senderID, recipientID)
	senderCount, _ := database.Redis.Get(ctx, senderKey).Int64()
	if senderCount >= int64(maxPerSender) {
		return false
	}

	// 3. Kunlik limit tekshiruvi
	countKey := fmt.Sprintf("notif_daily:%s:%s", notifType, recipientID)
	dailyCount, _ := database.Redis.Get(ctx, countKey).Int64()
	if dailyCount >= int64(dailyMax) {
		return false
	}

	// Barcha tekshiruvlardan o'tdi - counterlarni yangilash
	pipe := database.Redis.Pipeline()
	pipe.Incr(ctx, countKey)
	pipe.Expire(ctx, countKey, 24*time.Hour)
	pipe.Incr(ctx, senderKey)
	pipe.Expire(ctx, senderKey, 24*time.Hour)
	pipe.Set(ctx, gapKey, "1", minGap)
	pipe.Exec(ctx)

	return true
}

// pulseQueueState broadcasts the current matching-queue size to every
// online client. Frontend uses queue_size > 0 to keep the "Speak Now"
// button animated continuously while at least one user is searching.
// Once queue_size hits 0 (everyone matched or left), the animation stops.
//
// Called on every queue state change: join, leave, match, disconnect.
// Cheap fan-out - SendJSON drops on a full buffer so a slow client
// can't backpressure the broadcast.
func pulseQueueState(triggeringUserID, senderName, levelGroup string) {
	H.BroadcastJSONExcept(triggeringUserID, "partner_searching", map[string]interface{}{
		"queue_size":  services.QueueSize(),
		"online":      H.OnlineUserCount(),
		"sender_name": senderName,
		"level_group": levelGroup,
	})
}

// optInSenderCooldown - opt-in foydalanuvchilar uchun yengilroq sender
// cooldown. Ular xabar olishni xohlashgan, shuning uchun bitta sender
// 10 daqiqada bir marta ularga yuboradi (passiv pool'dagi 20 daq emas).
const optInSenderCooldown = 10 * time.Minute

// canSendInvitations - sender notifySenderCooldown (20 daq) ichida faqat
// 1 marta passiv pool'ga broadcast trigger qila oladi.
func canSendInvitations(senderID string) bool {
	ctx := context.Background()
	key := fmt.Sprintf("queue_notif_sender:%s", senderID)
	ok, err := database.Redis.SetNX(ctx, key, "1", notifySenderCooldown).Result()
	if err != nil {
		return true
	}
	return ok
}

// canSendInvitationsOptIn - alohida 10 daqiqalik gate, faqat opt-in
// foydalanuvchilarga yuborish uchun. Passiv gate'dan mustaqil:
// passiv 20 daq cooldown ichida bo'lsa ham, opt-in pool ishlay oladi.
func canSendInvitationsOptIn(senderID string) bool {
	ctx := context.Background()
	key := fmt.Sprintf("queue_notif_sender_optin:%s", senderID)
	ok, err := database.Redis.SetNX(ctx, key, "1", optInSenderCooldown).Result()
	if err != nil {
		return true
	}
	return ok
}
func sendMatchInvitations(excludeUserID string, levelGroup string) {
	// Two independent sender cooldowns:
	//   passiveOK - 20 min, gates broadcasts to inactive recipients
	//   optInOK   - 10 min, gates broadcasts to opt-in recipients
	// Each fires its own SetNX so they advance independently. If both
	// are on cooldown there's nothing to do; otherwise we proceed and
	// each user is checked against the relevant flag inside the loop.
	passiveOK := canSendInvitations(excludeUserID)
	optInOK := canSendInvitationsOptIn(excludeUserID)
	if !passiveOK && !optInOK {
		return
	}

	// Resolve the sender's display name first (one query).
	var sender models.User
	senderName := "Kimdir"
	if err := database.DB.First(&sender, "id = ?", excludeUserID).Error; err == nil {
		senderName = sender.DisplayName()
	}

	levels := services.LevelsInGroup(levelGroup)

	// Two pools fold into one query:
	//   1. Recently active users (last 7d) - sent only if OFFLINE.
	//      Existing reactivation flow.
	//   2. Opt-in users (partner_alerts_enabled) - sent regardless of
	//      online status, as long as cooldowns allow. They asked for
	//      these pings explicitly.
	// Both still respect bot_blocked + canNotifyUser caps.
	var users []models.User
	query := database.DB.
		Where("is_banned = ? AND is_active = ? AND id != ?", false, true, excludeUserID).
		Where("bot_blocked = ?", false).
		Where("last_active_at > NOW() - INTERVAL '7 days' OR partner_alerts_enabled = ?", true)

	if levelGroup != "" && levelGroup != "all" {
		query = query.Where("level IN ?", levels)
	}

	query.Order("last_active_at DESC").Limit(500).Find(&users)

	sent := 0
	for _, u := range users {
		// Online users normally skip - they're already on the platform.
		// EXCEPTION: opt-in users explicitly want to be pinged anyway.
		if H.GetClientByUserID(u.ID.String()) != nil && !u.PartnerAlertsEnabled {
			continue
		}
		// Branch by recipient type: opt-in users use the 10-min sender
		// gate and BYPASS per-recipient cooldowns (they asked for every
		// ping). Passive users use the 20-min sender gate AND the
		// per-recipient cooldowns (90min gap / 6/day / 2/sender).
		if u.PartnerAlertsEnabled {
			if !optInOK {
				continue
			}
		} else {
			if !passiveOK {
				continue
			}
			if !canNotifyUser(excludeUserID, u.ID.String(), "invite", u.LastActiveAt) {
				continue
			}
		}
		if bot.SendMatchInvitation(u.TelegramID, levelGroup, u.FirstName, senderName) {
			sent++
		}
	}

	if sent > 0 {
		log.Info().Int("sent", sent).Str("level", levelGroup).Str("sender", senderName).Msg("Match invitations sent")
	}
}

func handleLeaveQueue(c *Client) {
	services.RemoveFromQueue(c.UserID)
	c.SendJSON("queue_left", map[string]string{"message": "Navbatdan chiqdingiz"})
	// Broadcast the new queue state so other clients can stop
	// animating their Speak Now button if the queue just emptied.
	uid := c.UserID
	safego.Go("pulseQueueLeft", func() {
		pulseQueueState(uid, "", "")
	})
}

type SignalingData struct {
	SessionID string          `json:"session_id"`
	Offer     json.RawMessage `json:"offer,omitempty"`
	Answer    json.RawMessage `json:"answer,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
}

func handleSignaling(c *Client, event string, data json.RawMessage) {
	var sig SignalingData
	if err := json.Unmarshal(data, &sig); err != nil {
		return
	}

	partnerID, err := services.GetPartnerID(sig.SessionID, c.UserID)
	if err != nil || partnerID == "" {
		return
	}

	partner := H.GetClientByUserID(partnerID)
	if partner == nil {
		return
	}

	partner.SendJSON(event, sig)
}

type SessionEndData struct {
	SessionID string `json:"session_id"`
}

func handleSessionEnd(c *Client, data json.RawMessage) {
	var endData SessionEndData
	if err := json.Unmarshal(data, &endData); err != nil {
		return
	}

	// Tear down any mini-game in this session so it can't linger or block
	// future invites once the call is over.
	handleSessionGamesCleanup(endData.SessionID)

	// Detach any listening teacher and tell them why the audio stopped.
	detachListenersOnSessionEnd(endData.SessionID)

	// Get partner ID BEFORE ending session (Redis data may be cleaned)
	partnerID, _ := services.GetPartnerID(endData.SessionID, c.UserID)

	// End session in DB
	session, err := services.EndSession(endData.SessionID, c.UserID)
	if err != nil || session == nil {
		return
	}

	minutes := session.DurationMinutes()

	// Notify partner - session ended for both sides
	if partnerID == "" {
		// Fallback: get partner from session model
		partnerID = session.User1ID.String()
		if partnerID == c.UserID {
			partnerID = session.User2ID.String()
		}
	}

	// Room call: minutes are free. They land in the non-billing window
	// (so the streak and the daily progress bar still see them) and in
	// the room's own attendance report - but never against the daily
	// limit, which is the whole point of a classroom room.
	isRoom := session.RoomID != nil
	if isRoom {
		creditRoomSession(session, endData.SessionID, minutes)
	} else {
		services.IncrementUsage(c.UserID, minutes)
	}

	// After usage is in Redis, see if today's total qualifies for a streak.
	uid := c.UserID
	safego.Go("recordStreak/self", func() {
		mins := services.CountTodayTotalMinutes(uid)
		services.RecordStreakIfQualified(uid, mins)
	})

	if partnerID != "" {
		if !isRoom {
			services.IncrementUsage(partnerID, minutes)
		}
		pid := partnerID
		safego.Go("recordStreak/partner", func() {
			mins := services.CountTodayTotalMinutes(pid)
			services.RecordStreakIfQualified(pid, mins)
		})

		if partner := H.GetClientByUserID(partnerID); partner != nil {
			partner.SendJSON("session_ended", map[string]interface{}{
				"session_id":       endData.SessionID,
				"duration_minutes": minutes,
				"ended_by":         "partner",
				"room_id":          sessionRoomIDValue(session),
			})
		}
	}

	// Confirm to the user who ended
	c.SendJSON("session_ended", map[string]interface{}{
		"session_id":       endData.SessionID,
		"duration_minutes": minutes,
		"ended_by":         "you",
		"room_id":          sessionRoomIDValue(session),
	})

	log.Info().
		Str("session_id", endData.SessionID).
		Int("duration_min", minutes).
		Msg("Session ended")
}

// --- Mic state ---

type MicStateData struct {
	SessionID string `json:"session_id"`
	Muted     bool   `json:"muted"`
}

// handleMicState forwards a "partner muted / unmuted" event to the other side.
// Pure relay - nothing is persisted.
func handleMicState(c *Client, data json.RawMessage) {
	var ms MicStateData
	if err := json.Unmarshal(data, &ms); err != nil {
		return
	}

	partnerID, err := services.GetPartnerID(ms.SessionID, c.UserID)
	if err != nil || partnerID == "" {
		return
	}

	partner := H.GetClientByUserID(partnerID)
	if partner == nil {
		return
	}

	partner.SendJSON("mic_state", map[string]interface{}{
		"session_id": ms.SessionID,
		"sender_id":  c.UserID,
		"muted":      ms.Muted,
	})
}

// --- Topic change ---

// TopicChangeData carries a soft, ephemeral topic prompt switch from
// one partner to the other. Topic strings are picked client-side from
// the shared frontend pool, so the server is a pure relay - nothing
// is persisted, and we only enforce a sane size cap so a malicious
// client can't blast giant payloads at the partner.
type TopicChangeData struct {
	SessionID string `json:"session_id"`
	Topic     string `json:"topic"`
}

// handleTopicChange forwards the new prompt to the partner so both
// sides see the same conversation card after a "Change" tap. Pure
// relay - we intentionally DO NOT persist the swap; the topic is
// an ephemeral conversation aid and users frequently cycle through
// the whole pool in a single session, which would bloat the row
// without adding any real review value.
func handleTopicChange(c *Client, data json.RawMessage) {
	var tc TopicChangeData
	if err := json.Unmarshal(data, &tc); err != nil {
		return
	}

	topic := tc.Topic
	if topic == "" || utf8.RuneCountInString(topic) > 500 {
		return
	}

	partnerID, err := services.GetPartnerID(tc.SessionID, c.UserID)
	if err != nil || partnerID == "" {
		return
	}

	partner := H.GetClientByUserID(partnerID)
	if partner == nil {
		return
	}

	partner.SendJSON("topic_change", map[string]interface{}{
		"session_id": tc.SessionID,
		"sender_id":  c.UserID,
		"topic":      topic,
	})
}

// --- Chat ---

type ChatMessageData struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

func handleChatMessage(c *Client, data json.RawMessage) {
	var msg ChatMessageData
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	// Validate text
	text := msg.Text
	if text == "" || utf8.RuneCountInString(text) > 1000 {
		return
	}

	// Get partner
	partnerID, err := services.GetPartnerID(msg.SessionID, c.UserID)
	if err != nil || partnerID == "" {
		return
	}

	now := time.Now()

	// Forward to partner (no DB save - chat is ephemeral)
	outgoing := map[string]interface{}{
		"session_id": msg.SessionID,
		"sender_id":  c.UserID,
		"text":       text,
		"created_at": now.Format(time.RFC3339),
	}

	if partner := H.GetClientByUserID(partnerID); partner != nil {
		partner.SendJSON("chat_message", outgoing)
	}

	// Confirm to sender
	c.SendJSON("chat_message", outgoing)
}

// createAndNotifySession creates a session and notifies both matched users.
//
// Belt-and-braces: FindMatchFull already removes both users from the
// queue atomically, but we also call RemoveFromQueue here to cover
// any path that jumps straight to createAndNotifySession without
// going through the queue (tests, manual triggers, future features).
func createAndNotifySession(user1ID, user2ID string) {
	services.RemoveFromQueue(user1ID)
	services.RemoveFromQueue(user2ID)

	// Broadcast the post-match queue state so other clients stop
	// animating Speak Now if both matched users were the last in queue.
	safego.Go("pulseQueueMatched", func() {
		pulseQueueState("", "", "")
	})

	uid1, _ := uuid.Parse(user1ID)
	uid2, _ := uuid.Parse(user2ID)

	limitMinutes := services.GetSessionLimitMinutes(uid1, uid2)

	session, err := services.CreateSession(user1ID, user2ID, limitMinutes)
	if err != nil {
		// `ErrUserAlreadyInSession` is the DB-level guard firing -
		// one of the users was already in an active session at the
		// moment we tried to create this one. Silent log + no
		// notification; the user keeps their existing session.
		if errors.Is(err, services.ErrUserAlreadyInSession) {
			log.Warn().
				Str("user1_id", user1ID).
				Str("user2_id", user2ID).
				Msg("createAndNotifySession: one side already in a session, skipping")
			return
		}
		log.Error().Err(err).Msg("Failed to create session")
		return
	}

	sessionID := session.ID.String()

	user1, err1 := services.GetUserByID(uid1)
	user2, err2 := services.GetUserByID(uid2)
	if err1 != nil || err2 != nil || user1 == nil || user2 == nil {
		log.Error().Msg("Failed to load users for match notification")
		return
	}

	topic := ""
	if session.Topic != nil {
		topic = *session.Topic
	}

	// Send Telegram notification to offline users
	if H.GetClientByUserID(user1ID) == nil {
		if canNotifyUser(user2ID, user1ID, "match", user1.LastActiveAt) {
			bot.SendMatchNotification(user1.TelegramID, user2.DisplayName(), sessionID)
		}
	}
	if H.GetClientByUserID(user2ID) == nil {
		if canNotifyUser(user1ID, user2ID, "match", user2.LastActiveAt) {
			bot.SendMatchNotification(user2.TelegramID, user1.DisplayName(), sessionID)
		}
	}

	matchData := func(partnerID string, partner *models.User, isCaller bool) map[string]interface{} {
		return map[string]interface{}{
			"session_id": sessionID,
			"partner": map[string]interface{}{
				"id":                   partnerID,
				"name":                 partner.DisplayName(),
				"level":                partner.Level,
				"region":               partner.Region,
				"photo_url":            partner.PhotoURL,
				"pinned_speaking_band": partner.PinnedSpeakingBand,
			},
			"topic":         topic,
			"limit_minutes": limitMinutes,
			"is_caller":     isCaller,
		}
	}

	if client1 := H.GetClientByUserID(user1ID); client1 != nil {
		client1.SendJSON("match_found", matchData(user2ID, user2, true))
	}
	if client2 := H.GetClientByUserID(user2ID); client2 != nil {
		client2.SendJSON("match_found", matchData(user1ID, user1, false))
	}

	log.Info().
		Str("session_id", sessionID).
		Str("user1", user1ID).
		Str("user2", user2ID).
		Msg("Match created")
}
