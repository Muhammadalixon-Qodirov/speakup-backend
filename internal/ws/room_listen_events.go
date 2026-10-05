package ws

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/services"
)

// Teacher listen-in over WebSocket.
//
// Topology: the two students keep their existing 1:1 peer connection
// exactly as it is. On top of that, EACH student opens a second,
// send-only audio PeerConnection toward the teacher. The teacher answers
// both and receives two independent streams.
//
// That choice is deliberate. Renegotiating the students' own connection
// to carry a third party would risk dropping a live call for a feature
// that is supposed to be unobtrusive; a parallel connection cannot
// disturb it at all, and costs each student one extra audio upload.
//
// Client → server:
//
//	room_listen_start {session_id}                  OWNER ONLY
//	room_listen_stop  {}
//	listen_offer      {session_id, target_id, offer}      student → teacher
//	listen_answer     {session_id, target_id, answer}     teacher → student
//	listen_ice        {session_id, target_id, candidate}  both ways
//
// Server → client:
//
//	listen_started  {session_id, room_id, participants[]}   → teacher
//	listen_stopped  {session_id, reason}                    → teacher
//	listener_joined {session_id, listener_id, listener_name}→ students
//	listener_left   {session_id, listener_id}               → students
//	listen_offer / listen_answer / listen_ice  (relayed, + sender_id)
//
// The students ALWAYS receive listener_joined / listener_left. There is
// no silent listening mode, by design - see services/room_listen.go.

type listenStartIn struct {
	SessionID string `json:"session_id"`
}

type listenSignalIn struct {
	SessionID string          `json:"session_id"`
	TargetID  string          `json:"target_id"`
	Offer     json.RawMessage `json:"offer,omitempty"`
	Answer    json.RawMessage `json:"answer,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
}

// handleRoomListenStart attaches the teacher to a live session.
func handleRoomListenStart(c *Client, data json.RawMessage) {
	var in listenStartIn
	if err := json.Unmarshal(data, &in); err != nil {
		roomError(c, "BAD_REQUEST", "So'rov formati noto'g'ri")
		return
	}

	listenerID, err := uuid.Parse(c.UserID)
	if err != nil {
		roomError(c, "INVALID_USER", "Foydalanuvchi noto'g'ri")
		return
	}

	session, err := services.CanListenToSession(in.SessionID, listenerID)
	if err != nil {
		switch err {
		case services.ErrNotSessionRoomOwner:
			roomError(c, "NOT_OWNER", "Faqat room egasi tinglay oladi")
		default:
			roomError(c, "SESSION_NOT_LISTENABLE", "Bu suhbatni tinglab bo'lmaydi")
		}
		return
	}

	// Attaching elsewhere detaches the previous session first, so the
	// students there stop showing a stale "teacher is listening" badge.
	if prev := services.StartListening(in.SessionID, c.UserID); prev != "" {
		notifyListenerLeft(prev, c.UserID)
	}

	u1 := session.User1ID.String()
	u2 := session.User2ID.String()

	name := displayName(c.UserID)

	// Tell both students. This is what makes them open the extra
	// send-only peer connection AND what puts the indicator on screen.
	for _, uid := range []string{u1, u2} {
		SendToUser(uid, "listener_joined", map[string]interface{}{
			"session_id":    in.SessionID,
			"listener_id":   c.UserID,
			"listener_name": name,
		})
	}

	c.SendJSON("listen_started", map[string]interface{}{
		"session_id": in.SessionID,
		"room_id":    session.RoomID.String(),
		"participants": []map[string]interface{}{
			{"id": u1, "name": displayName(u1)},
			{"id": u2, "name": displayName(u2)},
		},
	})

	log.Info().
		Str("session_id", in.SessionID).
		Str("listener_id", c.UserID).
		Msg("Teacher started listening to a room session")

	if session.RoomID != nil {
		BroadcastRoomState(*session.RoomID)
	}
}

// handleRoomListenStop detaches the teacher from whatever they're on.
func handleRoomListenStop(c *Client) {
	sessionID := services.StopListening(c.UserID)
	if sessionID == "" {
		return
	}

	notifyListenerLeft(sessionID, c.UserID)

	c.SendJSON("listen_stopped", map[string]interface{}{
		"session_id": sessionID,
		"reason":     "you",
	})

	if roomID := services.SessionRoomID(sessionID); roomID != nil {
		BroadcastRoomState(*roomID)
	}
}

// notifyListenerLeft tells both speakers to tear down the extra peer
// connection and drop the on-screen indicator.
func notifyListenerLeft(sessionID, listenerID string) {
	for _, uid := range sessionParticipantIDs(sessionID) {
		SendToUser(uid, "listener_left", map[string]interface{}{
			"session_id":  sessionID,
			"listener_id": listenerID,
		})
	}
}

// handleListenSignaling relays listen-mode WebRTC signalling.
//
// The authorisation rule is the whole point of routing this through the
// server rather than reusing handleSignaling: a message may only travel
// between a REGISTERED LISTENER and a SPEAKER of that same session, in
// either direction. Anything else is dropped silently.
//
// Without this gate, a user who guessed a session UUID could push offers
// and ICE at two people mid-call.
func handleListenSignaling(c *Client, event string, data json.RawMessage) {
	var sig listenSignalIn
	if err := json.Unmarshal(data, &sig); err != nil {
		return
	}
	if sig.SessionID == "" || sig.TargetID == "" {
		return
	}

	senderIsListener := services.IsSessionListener(sig.SessionID, c.UserID)
	senderIsSpeaker := services.IsSessionParticipant(sig.SessionID, c.UserID)

	targetIsListener := services.IsSessionListener(sig.SessionID, sig.TargetID)
	targetIsSpeaker := services.IsSessionParticipant(sig.SessionID, sig.TargetID)

	if !mayRelayListenSignal(senderIsListener, senderIsSpeaker, targetIsListener, targetIsSpeaker) {
		log.Warn().
			Str("event", event).
			Str("session_id", sig.SessionID).
			Str("sender", c.UserID).
			Str("target", sig.TargetID).
			Msg("listen signalling rejected: not a listener↔speaker pair")
		return
	}

	target := H.GetClientByUserID(sig.TargetID)
	if target == nil {
		return
	}

	target.SendJSON(event, map[string]interface{}{
		"session_id": sig.SessionID,
		"sender_id":  c.UserID,
		"offer":      sig.Offer,
		"answer":     sig.Answer,
		"candidate":  sig.Candidate,
	})
}

// mayRelayListenSignal is the authorisation rule for listen-mode
// signalling, isolated so it can be exhaustively tested.
//
// A message may only travel between a REGISTERED LISTENER and a SPEAKER
// of the same session, in either direction. Everything else is refused,
// including the cases that look harmless:
//
//   - speaker → speaker: that is what `session_offer` is for; letting it
//     through here would be a second, unaudited path into a live call
//   - listener → listener: two teachers have no business negotiating
//     media with each other through a student's session
//   - anyone unrelated: a stranger who guessed the session UUID
//
// The "sender is BOTH" case cannot occur - CanListenToSession refuses to
// register a speaker as a listener - but it is refused here anyway
// rather than trusted, so this function is safe on its own terms.
func mayRelayListenSignal(senderIsListener, senderIsSpeaker, targetIsListener, targetIsSpeaker bool) bool {
	if senderIsListener && senderIsSpeaker {
		return false
	}
	if targetIsListener && targetIsSpeaker {
		return false
	}
	if senderIsListener && targetIsSpeaker {
		return true
	}
	if senderIsSpeaker && targetIsListener {
		return true
	}
	return false
}

// sessionParticipantIDs returns the two speakers of a session.
func sessionParticipantIDs(sessionID string) []string {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return nil
	}
	var s struct {
		User1ID uuid.UUID
		User2ID uuid.UUID
	}
	if err := database.DB.
		Table("sessions").
		Select("user1_id", "user2_id").
		Where("id = ?", sid).
		Scan(&s).Error; err != nil {
		return nil
	}
	if s.User1ID == uuid.Nil {
		return nil
	}
	return []string{s.User1ID.String(), s.User2ID.String()}
}

// DetachListenersOnSessionEnd is the exported form, for the REST
// session-end fallback in the handlers package.
func DetachListenersOnSessionEnd(sessionID string) {
	detachListenersOnSessionEnd(sessionID)
}

// detachListenersOnSessionEnd tears down every listener when a call
// finishes, and tells each teacher why their audio just stopped.
// Called from both session-end paths.
func detachListenersOnSessionEnd(sessionID string) {
	listeners := services.ClearSessionListeners(sessionID)
	for _, id := range listeners {
		SendToUser(id, "listen_stopped", map[string]interface{}{
			"session_id": sessionID,
			"reason":     "session_ended",
		})
	}
}

// detachListenerOnDisconnect runs from the WS disconnect path: if the
// disconnecting user was listening to something, the students must be
// told so their extra peer connection is closed and the indicator clears.
func detachListenerOnDisconnect(userID string) {
	sessionID := services.StopListening(userID)
	if sessionID == "" {
		return
	}
	uid := userID
	sid := sessionID
	safego.Go("detachListenerOnDisconnect", func() {
		notifyListenerLeft(sid, uid)
		if roomID := services.SessionRoomID(sid); roomID != nil {
			BroadcastRoomState(*roomID)
		}
	})
}
