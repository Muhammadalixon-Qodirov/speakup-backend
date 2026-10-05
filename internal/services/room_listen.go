package services

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// Teacher listen-in.
//
// A teacher owning a room can listen to a live session between two of
// their students - the online equivalent of walking around the classroom
// while pairs practise.
//
// TRANSPARENCY IS PART OF THE DESIGN, not a courtesy. Both students are
// told the moment a listener attaches (`listener_joined`) and when they
// detach, and the frontend is expected to show it. There is deliberately
// no silent mode: covert audio monitoring of people is surveillance, and
// it is also unnecessary here - the students' browsers have to open a
// second peer connection toward the teacher anyway, so hiding it would
// mean lying in the UI about something the client already knows.
//
// Transport: the teacher does NOT join the students' peer connection.
// Each student opens a second, send-only audio PeerConnection toward the
// teacher, and the teacher receives two independent streams. That keeps
// the students' own 1:1 call untouched (no renegotiation, no quality
// change) and costs each student one extra audio upload (~40 kbit/s).

var (
	ErrSessionNotListenable = errors.New("session cannot be listened to")
	ErrNotSessionRoomOwner  = errors.New("not the owner of this session's room")
)

// listenTTL bounds every listener key. A room session is capped at
// RoomSessionLimitMinutes (120), so 3 hours is comfortably longer than
// any real call while still guaranteeing that a crashed teacher client
// can never leave a permanent listener registration behind.
const listenTTL = 3 * time.Hour

func listenersKey(sessionID string) string { return "room_listeners:" + sessionID }
func listeningKey(userID string) string    { return "room_listening:" + userID }

// CanListenToSession authorises a listen request and returns the session.
//
// Every condition here is load-bearing:
//   - the session must exist and still be active (no listening to history)
//   - it must belong to a room (public-queue calls are strangers talking
//     to each other and are nobody's to monitor)
//   - the caller must own THAT room
//   - the caller must not be one of the two speakers
func CanListenToSession(sessionID string, listenerID uuid.UUID) (*models.Session, error) {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, ErrSessionNotListenable
	}

	var session models.Session
	if err := database.DB.First(&session, "id = ?", sid).Error; err != nil {
		return nil, ErrSessionNotListenable
	}
	if session.Status != "active" || session.RoomID == nil {
		return nil, ErrSessionNotListenable
	}
	if session.User1ID == listenerID || session.User2ID == listenerID {
		return nil, ErrSessionNotListenable
	}
	if !IsRoomOwner(*session.RoomID, listenerID) {
		return nil, ErrNotSessionRoomOwner
	}

	return &session, nil
}

// StartListening registers the teacher as a listener on a session.
//
// A teacher can only listen to one session at a time - attaching to a
// second one detaches the first, and the caller gets the previous
// session ID back so it can notify those students that the listener
// left. Without this, a teacher clicking through three pairs would leave
// two stale "teacher is listening" badges on screen.
func StartListening(sessionID string, listenerID string) (previousSessionID string) {
	ctx := context.Background()

	previousSessionID = CurrentlyListeningTo(listenerID)
	if previousSessionID == sessionID {
		previousSessionID = ""
	} else if previousSessionID != "" {
		database.Redis.SRem(ctx, listenersKey(previousSessionID), listenerID)
	}

	pipe := database.Redis.Pipeline()
	pipe.SAdd(ctx, listenersKey(sessionID), listenerID)
	pipe.Expire(ctx, listenersKey(sessionID), listenTTL)
	pipe.Set(ctx, listeningKey(listenerID), sessionID, listenTTL)
	pipe.Exec(ctx)

	return previousSessionID
}

// StopListening detaches the teacher and returns the session they were
// listening to (empty if they weren't). Safe to call unconditionally -
// the WS disconnect path does exactly that.
func StopListening(listenerID string) (sessionID string) {
	ctx := context.Background()

	sessionID = CurrentlyListeningTo(listenerID)
	if sessionID == "" {
		return ""
	}

	pipe := database.Redis.Pipeline()
	pipe.SRem(ctx, listenersKey(sessionID), listenerID)
	pipe.Del(ctx, listeningKey(listenerID))
	pipe.Exec(ctx)

	return sessionID
}

// CurrentlyListeningTo returns the session a user is listening to, or "".
func CurrentlyListeningTo(listenerID string) string {
	ctx := context.Background()
	sid, err := database.Redis.Get(ctx, listeningKey(listenerID)).Result()
	if err != nil {
		return ""
	}
	return sid
}

// SessionListeners returns every listener currently attached to a session.
func SessionListeners(sessionID string) []string {
	ctx := context.Background()
	ids, err := database.Redis.SMembers(ctx, listenersKey(sessionID)).Result()
	if err != nil {
		return nil
	}
	return ids
}

// IsSessionListener reports whether a user is a registered listener on a
// session. This is the authorisation gate for relaying listen-mode
// signalling - without it, anyone who guessed a session UUID could push
// WebRTC offers at the two speakers.
func IsSessionListener(sessionID, userID string) bool {
	ctx := context.Background()
	ok, err := database.Redis.SIsMember(ctx, listenersKey(sessionID), userID).Result()
	if err != nil {
		return false
	}
	return ok
}

// ClearSessionListeners drops every listener on a session and returns
// who they were, so the caller can tell each of them the call is over.
// Invoked from the session-end path.
func ClearSessionListeners(sessionID string) []string {
	ctx := context.Background()

	listeners := SessionListeners(sessionID)
	if len(listeners) == 0 {
		return nil
	}

	pipe := database.Redis.Pipeline()
	pipe.Del(ctx, listenersKey(sessionID))
	for _, id := range listeners {
		pipe.Del(ctx, listeningKey(id))
	}
	pipe.Exec(ctx)

	return listeners
}

// IsSessionParticipant reports whether the user is one of the two
// speakers. Used together with IsSessionListener to decide whether a
// listen-mode signalling message may be relayed.
func IsSessionParticipant(sessionID, userID string) bool {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return false
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return false
	}

	var count int64
	database.DB.Model(&models.Session{}).
		Where("id = ? AND (user1_id = ? OR user2_id = ?)", sid, uid, uid).
		Count(&count)
	return count > 0
}

// ActiveRoomSessionView is one live call in the teacher's panel - the row
// behind the "listen" button.
type ActiveRoomSessionView struct {
	SessionID uuid.UUID  `json:"session_id"`
	User1ID   uuid.UUID  `json:"user1_id"`
	User1Name string     `json:"user1_name"`
	User2ID   uuid.UUID  `json:"user2_id"`
	User2Name string     `json:"user2_name"`
	Topic     *string    `json:"topic"`
	StartedAt *time.Time `json:"started_at"`
	// Listeners is who is currently monitoring this call. Shown to the
	// teacher so a co-teacher's presence is visible too.
	Listeners []string `json:"listeners"`
}

// ActiveRoomSessions lists the room's live calls with participant names.
func ActiveRoomSessions(roomID uuid.UUID) []ActiveRoomSessionView {
	var sessions []models.Session
	database.DB.
		Preload("User1").Preload("User2").
		Where("room_id = ? AND status = ?", roomID, "active").
		Order("started_at ASC").
		Find(&sessions)

	out := make([]ActiveRoomSessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, ActiveRoomSessionView{
			SessionID: s.ID,
			User1ID:   s.User1ID,
			User1Name: s.User1.DisplayName(),
			User2ID:   s.User2ID,
			User2Name: s.User2.DisplayName(),
			Topic:     s.Topic,
			StartedAt: s.StartedAt,
			Listeners: SessionListeners(s.ID.String()),
		})
	}
	return out
}
