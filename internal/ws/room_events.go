package ws

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/services"
)

// Room WebSocket flow.
//
// A room is a teacher's private speaking space. Inside it the student
// experience is the SAME single tap as the public queue - press Speak,
// get matched, talk - but the pool is restricted to that room's members
// and the minutes are free. The public `join_queue` path is untouched;
// everything room-related lives in this file.
//
// Client → server:
//
//	room_join_queue   {room_id}   enter the room's waiting pool
//	room_leave_queue  {}          leave whichever room pool you're in
//	room_state        {room_id}   ask for a fresh panel snapshot
//	room_start_round  {room_id}   OWNER ONLY - pair everyone waiting
//
// Server → client:
//
//	room_queued       {room_id, queue_size}
//	room_queue_left   {room_id}
//	match_found       {..., room_id, room_name}   ← same shape as public
//	room_state        {room_id, queue_size, members:[...]}
//	room_round_started{room_id, pairs}
//	room_error        {code, message}

type roomJoinQueueIn struct {
	RoomID string `json:"room_id"`
}

type roomStateIn struct {
	RoomID string `json:"room_id"`
}

// roomError sends a machine-readable failure to one client. Codes are
// part of the API contract - the Mini App branches on them.
func roomError(c *Client, code, message string) {
	c.SendJSON("room_error", map[string]string{
		"code":    code,
		"message": message,
	})
}

// resolveRoomAccess loads the room and verifies the caller may use it.
// Returns nil after having already sent the appropriate room_error.
func resolveRoomAccess(c *Client, rawRoomID string) (*models.Room, uuid.UUID, bool) {
	roomID, err := uuid.Parse(rawRoomID)
	if err != nil {
		roomError(c, "INVALID_ROOM", "Room ID noto'g'ri")
		return nil, uuid.Nil, false
	}

	room, err := services.GetRoomByID(roomID)
	if err != nil {
		roomError(c, "ROOM_NOT_FOUND", "Room topilmadi")
		return nil, uuid.Nil, false
	}

	userID, err := uuid.Parse(c.UserID)
	if err != nil {
		roomError(c, "INVALID_USER", "Foydalanuvchi noto'g'ri")
		return nil, uuid.Nil, false
	}

	if !services.IsRoomMember(roomID, userID) {
		roomError(c, "NOT_A_MEMBER", "Siz bu room a'zosi emassiz")
		return nil, uuid.Nil, false
	}

	if !room.IsOpen() {
		roomError(c, "ROOM_CLOSED", "Room yopilgan")
		return nil, uuid.Nil, false
	}

	return room, userID, true
}

// handleRoomJoinQueue is the room counterpart of handleJoinQueue.
//
// Two deliberate differences from the public queue:
//
//   - NO daily-limit check. Room minutes are free; that is the entire
//     value proposition for a learning centre, and charging them here
//     would cut a lesson off after ten minutes.
//   - NO level/gender filters. The teacher already curated who is in the
//     room, so filtering would only starve an already small pool.
//
// The channel-subscription gate IS still enforced, exactly as on the
// public queue - it is a platform-wide rule, not a matching preference.
func handleRoomJoinQueue(c *Client, data json.RawMessage) {
	var in roomJoinQueueIn
	if err := json.Unmarshal(data, &in); err != nil {
		roomError(c, "BAD_REQUEST", "So'rov formati noto'g'ri")
		return
	}

	room, userID, ok := resolveRoomAccess(c, in.RoomID)
	if !ok {
		return
	}

	var me models.User
	if err := database.DB.First(&me, "id = ?", userID).Error; err != nil {
		log.Error().Err(err).Msg("handleRoomJoinQueue: failed to load user")
		return
	}

	if !services.IsChannelSubscribed(me.TelegramID) {
		c.SendJSON("channel_subscription_required", map[string]string{
			"channel": services.RequiredChannelUsername(),
		})
		return
	}

	roomIDStr := room.ID.String()

	// AddToRoomQueue also drops the user out of the public queue, so a
	// student can never be waiting in both pools at once.
	services.AddToRoomQueue(roomIDStr, c.UserID)

	partnerID, err := services.FindRoomMatch(roomIDStr, c.UserID)
	if err != nil || partnerID == "" {
		// We found nobody - but a CONCURRENT matcher may have claimed us
		// in the meantime (two students tapping Speak in the same
		// millisecond: theirs wins, ours comes back empty). Claiming
		// clears our queue marker, so its absence means a `match_found`
		// is already on its way and a "searching..." message would only
		// make the UI flicker between the two states.
		if services.CurrentRoomQueue(c.UserID) == "" {
			return
		}

		c.SendJSON("room_queued", map[string]interface{}{
			"room_id":    roomIDStr,
			"room_name":  room.Name,
			"queue_size": services.RoomQueueSize(roomIDStr),
		})
		safego.Go("roomStatePulse/join", func() { broadcastRoomState(room.ID) })
		return
	}

	createAndNotifyRoomSession(c.UserID, partnerID, room)
	safego.Go("roomStatePulse/match", func() { broadcastRoomState(room.ID) })
}

// handleRoomLeaveQueue takes the student out of whichever room pool they
// are waiting in.
func handleRoomLeaveQueue(c *Client) {
	roomID := services.CurrentRoomQueue(c.UserID)
	services.RemoveFromRoomQueue(c.UserID)

	c.SendJSON("room_queue_left", map[string]string{"room_id": roomID})

	if roomID == "" {
		return
	}
	if rid, err := uuid.Parse(roomID); err == nil {
		safego.Go("roomStatePulse/leave", func() { broadcastRoomState(rid) })
	}
}

// handleRoomState answers a one-off panel refresh request.
func handleRoomState(c *Client, data json.RawMessage) {
	var in roomStateIn
	if err := json.Unmarshal(data, &in); err != nil {
		roomError(c, "BAD_REQUEST", "So'rov formati noto'g'ri")
		return
	}
	room, _, ok := resolveRoomAccess(c, in.RoomID)
	if !ok {
		return
	}
	c.SendJSON("room_state", BuildRoomState(room))
}

// handleRoomStartRound is the teacher's "pair everyone now" button.
//
// Instead of students trickling into matches one by one, the whole
// waiting pool is paired in a single shot - which is how a real lesson
// runs ("everyone, find your partner, go"). Only the room owner may
// trigger it.
func handleRoomStartRound(c *Client, data json.RawMessage) {
	var in roomStateIn
	if err := json.Unmarshal(data, &in); err != nil {
		roomError(c, "BAD_REQUEST", "So'rov formati noto'g'ri")
		return
	}

	room, userID, ok := resolveRoomAccess(c, in.RoomID)
	if !ok {
		return
	}
	if room.OwnerID != userID {
		roomError(c, "NOT_OWNER", "Faqat room egasi raund boshlay oladi")
		return
	}

	pairs, leftover := services.PairAllInRoom(room.ID.String())

	for _, p := range pairs {
		u1, u2 := p[0], p[1]
		safego.Go("roomRoundSession", func() {
			// PairAllInRoom already claimed both under the match lock,
			// so a failure here is an unrecoverable edge case that is
			// logged inside; nothing useful to report back per pair.
			_ = createAndNotifyRoomSession(u1, u2, room)
		})
	}

	// An odd student out goes back into the pool so they match as soon
	// as anyone else presses Speak, rather than silently falling out.
	for _, uid := range leftover {
		services.AddToRoomQueue(room.ID.String(), uid)
		if cl := H.GetClientByUserID(uid); cl != nil {
			cl.SendJSON("room_queued", map[string]interface{}{
				"room_id":    room.ID.String(),
				"room_name":  room.Name,
				"queue_size": services.RoomQueueSize(room.ID.String()),
				"note":       "waiting_for_partner",
			})
		}
	}

	c.SendJSON("room_round_started", map[string]interface{}{
		"room_id":  room.ID.String(),
		"pairs":    len(pairs),
		"leftover": len(leftover),
	})

	log.Info().
		Str("room_id", room.ID.String()).
		Int("pairs", len(pairs)).
		Int("leftover", len(leftover)).
		Msg("Room round started")

	safego.Go("roomStatePulse/round", func() { broadcastRoomState(room.ID) })
}

// --- Manual pairing (teacher picks who talks to whom) ---

type roomPairIn struct {
	RoomID string `json:"room_id"`
	// Batch form: the teacher lays out the whole class at once.
	Pairs []struct {
		User1ID string `json:"user1_id"`
		User2ID string `json:"user2_id"`
	} `json:"pairs"`
	// Shorthand for a single pair.
	User1ID string `json:"user1_id"`
	User2ID string `json:"user2_id"`
}

// handleRoomPair is the teacher's manual alternative to random matching.
//
// `room_start_round` shuffles; this one does exactly what the teacher
// says - "Ali with Vali, Dilnoza with Malika" - which is what you want
// when you're deliberately pairing a strong student with a weak one, or
// re-running a pair that was mid-conversation when the call dropped.
//
// Each pair is validated independently and a rejected pair does not
// abort the others: in a 20-student class one person having closed their
// tab must not cancel the entire round. The teacher gets a per-pair
// result back so the UI can show precisely which pairing failed and why.
func handleRoomPair(c *Client, data json.RawMessage) {
	var in roomPairIn
	if err := json.Unmarshal(data, &in); err != nil {
		roomError(c, "BAD_REQUEST", "So'rov formati noto'g'ri")
		return
	}

	room, userID, ok := resolveRoomAccess(c, in.RoomID)
	if !ok {
		return
	}
	if room.OwnerID != userID {
		roomError(c, "NOT_OWNER", "Faqat room egasi juftlik tanlay oladi")
		return
	}

	// Normalise both request shapes into one list.
	type pair struct{ a, b string }
	var requested []pair
	for _, p := range in.Pairs {
		requested = append(requested, pair{p.User1ID, p.User2ID})
	}
	if in.User1ID != "" && in.User2ID != "" {
		requested = append(requested, pair{in.User1ID, in.User2ID})
	}
	if len(requested) == 0 {
		roomError(c, "BAD_REQUEST", "Juftlik ko'rsatilmagan")
		return
	}

	results := make([]map[string]interface{}, 0, len(requested))
	created := 0

	// Guards against the teacher listing the same student in two pairs.
	claimed := make(map[string]bool, len(requested)*2)

	for _, p := range requested {
		res := map[string]interface{}{
			"user1_id": p.a,
			"user2_id": p.b,
		}

		reason := validateManualPair(room.ID, p.a, p.b, claimed)
		if reason != "" {
			res["ok"] = false
			res["reason"] = reason
			results = append(results, res)
			continue
		}

		claimed[p.a] = true
		claimed[p.b] = true

		// Take both out of the waiting pool before creating the session,
		// otherwise a concurrent random match could grab one of them
		// between here and CreateRoomSession.
		services.RemoveFromRoomQueue(p.a)
		services.RemoveFromRoomQueue(p.b)

		// Synchronous on purpose: the teacher is staring at a screen
		// waiting to see which pairings took. Reporting "ok" from a
		// goroutine we haven't waited for would sometimes be a lie -
		// CreateRoomSession can still lose the advisory-lock race.
		if err := createAndNotifyRoomSession(p.a, p.b, room); err != nil {
			delete(claimed, p.a)
			delete(claimed, p.b)
			res["ok"] = false
			res["reason"] = "CREATE_FAILED"
			results = append(results, res)
			continue
		}

		created++
		res["ok"] = true
		results = append(results, res)
	}

	c.SendJSON("room_paired", map[string]interface{}{
		"room_id": room.ID.String(),
		"created": created,
		"results": results,
	})

	log.Info().
		Str("room_id", room.ID.String()).
		Int("requested", len(requested)).
		Int("created", created).
		Msg("Room manual pairing")

	safego.Go("roomStatePulse/manualPair", func() { broadcastRoomState(room.ID) })
}

// validateManualPair returns "" when the pair may be created, or a stable
// machine-readable reason code explaining why it may not.
func validateManualPair(roomID uuid.UUID, aRaw, bRaw string, claimed map[string]bool) string {
	if aRaw == "" || bRaw == "" || aRaw == bRaw {
		return "INVALID_PAIR"
	}

	aID, err := uuid.Parse(aRaw)
	if err != nil {
		return "INVALID_PAIR"
	}
	bID, err := uuid.Parse(bRaw)
	if err != nil {
		return "INVALID_PAIR"
	}

	if claimed[aRaw] || claimed[bRaw] {
		return "ALREADY_PAIRED_IN_THIS_REQUEST"
	}

	// Both must belong to THIS room - the teacher's authority stops at
	// their own class.
	if !services.IsRoomMember(roomID, aID) || !services.IsRoomMember(roomID, bID) {
		return "NOT_A_MEMBER"
	}

	// Both must be connected: a session created for an offline student
	// would just sit there until it times out, and their partner would
	// stare at "ulanmoqda" the whole time.
	if H.GetClientByUserID(aRaw) == nil || H.GetClientByUserID(bRaw) == nil {
		return "OFFLINE"
	}

	// And neither may already be talking to someone else.
	if services.UserHasActiveSession(aRaw) || services.UserHasActiveSession(bRaw) {
		return "ALREADY_IN_SESSION"
	}

	return ""
}

// createAndNotifyRoomSession is createAndNotifySession for a room match.
//
// It reuses the SAME `match_found` event shape as the public flow, with
// `room_id` / `room_name` added, so the frontend's WebRTC and call-screen
// code needs no branching - only the "this was a class session" badge.
func createAndNotifyRoomSession(user1ID, user2ID string, room *models.Room) error {
	// Belt-and-braces: the matcher already claimed both, but any path
	// that jumps straight here must not leave stale queue entries.
	services.RemoveFromRoomQueue(user1ID)
	services.RemoveFromRoomQueue(user2ID)

	session, err := services.CreateRoomSession(user1ID, user2ID, room.ID, services.RoomSessionLimitMinutes)
	if err != nil {
		if errors.Is(err, services.ErrUserAlreadyInSession) {
			log.Warn().
				Str("room_id", room.ID.String()).
				Str("user1_id", user1ID).
				Str("user2_id", user2ID).
				Msg("createAndNotifyRoomSession: one side already in a session, skipping")
			return err
		}
		log.Error().Err(err).Msg("Failed to create room session")
		return err
	}

	sessionID := session.ID.String()

	uid1, _ := uuid.Parse(user1ID)
	uid2, _ := uuid.Parse(user2ID)
	user1, err1 := services.GetUserByID(uid1)
	user2, err2 := services.GetUserByID(uid2)
	if err1 != nil || err2 != nil || user1 == nil || user2 == nil {
		log.Error().Msg("Failed to load users for room match notification")
		return errors.New("failed to load users for room match notification")
	}

	topic := ""
	if session.Topic != nil {
		topic = *session.Topic
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
			"limit_minutes": session.LimitMinutes,
			"is_caller":     isCaller,
			"room_id":       room.ID.String(),
			"room_name":     room.Name,
		}
	}

	if c1 := H.GetClientByUserID(user1ID); c1 != nil {
		c1.SendJSON("match_found", matchData(user2ID, user2, true))
	}
	if c2 := H.GetClientByUserID(user2ID); c2 != nil {
		c2.SendJSON("match_found", matchData(user1ID, user1, false))
	}

	log.Info().
		Str("session_id", sessionID).
		Str("room_id", room.ID.String()).
		Str("user1", user1ID).
		Str("user2", user2ID).
		Msg("Room match created")

	return nil
}

// EndRoomSessionsFor force-ends any live call the user has inside this
// room and tells both sides why.
//
// Called when a teacher removes a student. Removing them from the roster
// alone would not be enough: a student kicked for misbehaving would keep
// talking to their partner until one of them hung up, which is exactly
// what the teacher just tried to stop.
func EndRoomSessionsFor(roomID uuid.UUID, userID string, reason string) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return
	}

	var sessions []models.Session
	if err := database.DB.
		Where("room_id = ? AND status = ? AND (user1_id = ? OR user2_id = ?)",
			roomID, "active", uid, uid).
		Find(&sessions).Error; err != nil {
		return
	}

	for _, s := range sessions {
		sessionID := s.ID.String()

		ended, err := services.EndSession(sessionID, userID)
		if err != nil || ended == nil {
			continue
		}

		handleSessionGamesCleanup(sessionID)
		detachListenersOnSessionEnd(sessionID)

		minutes := ended.DurationMinutes()
		creditRoomSession(ended, sessionID, minutes)

		for _, participant := range []string{ended.User1ID.String(), ended.User2ID.String()} {
			endedBy := "partner"
			if participant == userID {
				endedBy = "you"
			}
			SendToUser(participant, "session_ended", map[string]interface{}{
				"session_id":       sessionID,
				"duration_minutes": minutes,
				"ended_by":         endedBy,
				"reason":           reason,
				"room_id":          sessionRoomIDValue(ended),
			})
		}

		log.Info().
			Str("session_id", sessionID).
			Str("room_id", roomID.String()).
			Str("user_id", userID).
			Str("reason", reason).
			Msg("Room session force-ended")
	}
}

// sessionRoomIDValue renders a session's room as a JSON value: the UUID
// string for a classroom call, null for a public-queue one. The client
// uses it to decide whether "call finished" returns to the room screen
// or to the ordinary rate screen.
func sessionRoomIDValue(s *models.Session) interface{} {
	if s == nil || s.RoomID == nil {
		return nil
	}
	return s.RoomID.String()
}

// creditRoomSession books a finished room call.
//
// Called from BOTH session-end paths (explicit `session_end` and the
// disconnect sweep). Safe to call twice for the same session: the minute
// write is guarded by a per-session Redis SETNX, and the counter update
// sits behind services.EndSession's atomic status flip, which only lets
// one caller through per session.
//
// Three things happen, and none of them touch the daily limit:
//  1. minutes go into the free (non-billing) 24h window, so the streak
//     and the home progress bar still count classroom practice
//  2. the room + both members' attendance counters advance
//  3. the teacher's live panel is refreshed
func creditRoomSession(session *models.Session, sessionID string, minutes int) {
	if session == nil || session.RoomID == nil {
		return
	}
	roomID := *session.RoomID

	u1 := session.User1ID.String()
	u2 := session.User2ID.String()

	services.IncrementRoomUsage(u1, minutes, sessionID)
	services.IncrementRoomUsage(u2, minutes, sessionID)

	services.RecordRoomSessionEnd(roomID, session.User1ID, session.User2ID, minutes)

	BroadcastRoomState(roomID)
}

// --- Live panel ---

// BuildRoomState assembles the teacher-panel snapshot: every member with
// their in-room stats plus three live flags (online / searching /
// speaking). Exported so the REST handler can serve the same payload for
// the initial page load.
func BuildRoomState(room *models.Room) map[string]interface{} {
	members, err := services.ListRoomMembers(room.ID)
	if err != nil {
		log.Error().Err(err).Msg("BuildRoomState: failed to list members")
		members = nil
	}

	roomIDStr := room.ID.String()

	queued := make(map[string]bool)
	for _, uid := range services.RoomQueueMembers(roomIDStr) {
		queued[uid] = true
	}

	// The live call list doubles as the teacher's "listen" menu: each row
	// carries the session ID the listen button needs, plus who is already
	// monitoring it.
	activeSessions := services.ActiveRoomSessions(room.ID)
	speaking := make(map[string]bool, len(activeSessions)*2)
	for _, s := range activeSessions {
		speaking[s.User1ID.String()] = true
		speaking[s.User2ID.String()] = true
	}

	onlineCount, searchingCount, speakingCount := 0, 0, 0
	for i := range members {
		id := members[i].UserID.String()
		members[i].Online = H != nil && H.GetClientByUserID(id) != nil
		members[i].Searching = queued[id]
		members[i].Speaking = speaking[id]

		if members[i].Online {
			onlineCount++
		}
		if members[i].Searching {
			searchingCount++
		}
		if members[i].Speaking {
			speakingCount++
		}
	}

	return map[string]interface{}{
		"room_id":        roomIDStr,
		"room_name":      room.Name,
		"owner_id":       room.OwnerID.String(),
		"member_count":   room.MemberCount,
		"online_count":   onlineCount,
		"queue_size":     searchingCount,
		"speaking_count": speakingCount,
		"total_sessions": room.TotalSessions,
		"total_minutes":  room.TotalMinutes,
		"members":        members,
		// Live calls, for the teacher's listen menu. Students see this
		// too - knowing who is talking is not sensitive, and the roster
		// already reveals it through the `speaking` flag.
		"active_sessions": activeSessions,
	}
}

// broadcastRoomState pushes a fresh snapshot to every ONLINE member of
// the room. Called whenever the room's live state changes: someone
// queues, leaves, matches, or a call ends.
//
// Fan-out is bounded by the room's member cap (100 by default) and
// SendJSON drops on a full buffer, so a slow client can't back-pressure
// the broadcast.
func broadcastRoomState(roomID uuid.UUID) {
	if H == nil {
		return
	}
	room, err := services.GetRoomByID(roomID)
	if err != nil {
		return
	}

	state := BuildRoomState(room)

	for _, uid := range services.RoomMemberIDs(roomID) {
		for _, cl := range H.GetClientsByUserID(uid) {
			cl.SendJSON("room_state", state)
		}
	}
}

// BroadcastRoomState is the exported form used from the session-end path
// (and any future non-ws caller) to refresh the panel.
func BroadcastRoomState(roomID uuid.UUID) {
	safego.Go("broadcastRoomState", func() { broadcastRoomState(roomID) })
}
