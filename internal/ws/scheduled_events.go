package ws

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/services"
)

// Walking into a scheduled appointment.
//
// The appointment is not a queue: there is exactly one other person and
// we know who they are. So instead of matching, both sides simply stand
// in the doorway for the ten minutes the window is open, and the moment
// the second one arrives the call is created and the ordinary
// `match_found` flow takes over - no new client-side call machinery.
//
// Client → server:
//
//	scheduled_join  {scheduled_id}
//	scheduled_leave {scheduled_id}
//
// Server → client:
//
//	scheduled_waiting  {scheduled_id, partner_waiting}   → you
//	scheduled_partner  {scheduled_id, waiting}           → the other side
//	scheduled_error    {code, message}
//	match_found        {..., scheduled_id}               → both, on pairing

type scheduledJoinIn struct {
	ScheduledID string `json:"scheduled_id"`
}

func scheduledError(c *Client, code, message string) {
	c.SendJSON("scheduled_error", map[string]string{
		"code":    code,
		"message": message,
	})
}

// resolveScheduled loads the appointment and checks the caller may act
// on it right now. Returns nil after having sent the error.
func resolveScheduled(c *Client, raw string) (*models.ScheduledSession, uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		scheduledError(c, "BAD_REQUEST", "Reja ID noto'g'ri")
		return nil, uuid.Nil, false
	}

	userID, err := uuid.Parse(c.UserID)
	if err != nil {
		scheduledError(c, "INVALID_USER", "Foydalanuvchi noto'g'ri")
		return nil, uuid.Nil, false
	}

	s, serr := services.LoadScheduledSession(id)
	if serr != nil {
		scheduledError(c, "NOT_FOUND", "Reja topilmadi")
		return nil, uuid.Nil, false
	}

	if s.User1ID != userID && s.User2ID != userID {
		scheduledError(c, "NOT_FOUND", "Reja topilmadi")
		return nil, uuid.Nil, false
	}

	return s, userID, true
}

// handleScheduledJoin puts the caller in the doorway and pairs the two
// as soon as both are there.
func handleScheduledJoin(c *Client, data json.RawMessage) {
	var in scheduledJoinIn
	if err := json.Unmarshal(data, &in); err != nil {
		scheduledError(c, "BAD_REQUEST", "So'rov formati noto'g'ri")
		return
	}

	s, userID, ok := resolveScheduled(c, in.ScheduledID)
	if !ok {
		return
	}

	// The window is the whole promise of the feature, so it is enforced
	// here and not only in the UI: an old card left open on a phone must
	// not be able to start a call an hour late.
	if !s.IsLive(time.Now()) {
		scheduledError(c, "NOT_LIVE", "Hozir bu rejaga qo'shilib bo'lmaydi")
		return
	}

	// The channel gate is a platform rule, enforced on every path into a
	// conversation - this one included.
	var me models.User
	if err := database.DB.First(&me, "id = ?", userID).Error; err != nil {
		log.Error().Err(err).Msg("handleScheduledJoin: failed to load user")
		return
	}
	if !services.IsChannelSubscribed(me.TelegramID) {
		c.SendJSON("channel_subscription_required", map[string]string{
			"channel": services.RequiredChannelUsername(),
		})
		return
	}

	partnerID := s.Partner(userID)
	partnerWaiting := services.MarkScheduledPresence(s.ID, userID.String(), partnerID.String())

	c.SendJSON("scheduled_waiting", map[string]interface{}{
		"scheduled_id":    s.ID.String(),
		"partner_waiting": partnerWaiting,
	})

	// Tell the other side somebody is standing there. This is the single
	// most effective nudge in the whole flow - "Ali kutmoqda" beats any
	// countdown.
	SendToUser(partnerID.String(), "scheduled_partner", map[string]interface{}{
		"scheduled_id": s.ID.String(),
		"waiting":      true,
	})

	if !partnerWaiting {
		return
	}

	// Both present: the doorway has done its job. Exactly one of the two
	// simultaneous joiners gets to create the call.
	if !services.ClaimScheduledPairing(s.ID) {
		return
	}
	services.ClearScheduledPresence(s.ID, userID.String(), partnerID.String())
	createAndNotifyScheduledSession(s, userID.String(), partnerID.String())
}

// handleScheduledLeave takes the caller back out of the doorway.
func handleScheduledLeave(c *Client, data json.RawMessage) {
	var in scheduledJoinIn
	if err := json.Unmarshal(data, &in); err != nil {
		return
	}
	s, userID, ok := resolveScheduled(c, in.ScheduledID)
	if !ok {
		return
	}

	services.ClearScheduledPresence(s.ID, userID.String())
	SendToUser(s.Partner(userID).String(), "scheduled_partner", map[string]interface{}{
		"scheduled_id": s.ID.String(),
		"waiting":      false,
	})
}

// createAndNotifyScheduledSession turns a kept appointment into a real
// call, reusing the exact `match_found` contract the client already
// implements for the public queue and for rooms.
func createAndNotifyScheduledSession(s *models.ScheduledSession, joinerID, partnerID string) {
	// Neither side can be sitting in a queue while walking into an
	// appointment - otherwise the matcher could hand one of them a
	// stranger a millisecond later.
	services.RemoveFromQueue(joinerID)
	services.RemoveFromQueue(partnerID)
	services.RemoveFromRoomQueue(joinerID)
	services.RemoveFromRoomQueue(partnerID)

	uid1, err1 := uuid.Parse(joinerID)
	uid2, err2 := uuid.Parse(partnerID)
	if err1 != nil || err2 != nil {
		return
	}

	// Same limit rule as any other call: premium on either side carries
	// it, two free users share the smaller remaining allowance. An
	// appointment is a promise about WHEN, not a way around the cap.
	limitMinutes := services.GetSessionLimitMinutes(uid1, uid2)

	session, err := services.CreateSession(joinerID, partnerID, limitMinutes)
	if err != nil {
		// Already talking: another path (a retry, or the partner's own
		// join) got there first. The users are in the call they wanted,
		// so this is not something to report to them.
		if errors.Is(err, services.ErrUserAlreadyInSession) {
			log.Warn().
				Str("scheduled_id", s.ID.String()).
				Msg("scheduled join: session already exists, skipping")
			return
		}
		log.Error().Err(err).
			Str("scheduled_id", s.ID.String()).
			Msg("Failed to create scheduled session")
		SendToUser(joinerID, "scheduled_error", map[string]string{
			"code":    "CREATE_FAILED",
			"message": "Suhbat ochilmadi, qayta urinib ko'ring",
		})
		return
	}

	sessionID := session.ID.String()
	services.MarkScheduledStarted(s.ID, session.ID)

	user1, e1 := services.GetUserByID(uid1)
	user2, e2 := services.GetUserByID(uid2)
	if e1 != nil || e2 != nil || user1 == nil || user2 == nil {
		log.Error().Msg("Failed to load users for scheduled match notification")
		return
	}

	topic := ""
	if session.Topic != nil {
		topic = *session.Topic
	}

	matchData := func(partner *models.User, partnerUserID string, isCaller bool) map[string]interface{} {
		return map[string]interface{}{
			"session_id": sessionID,
			"partner": map[string]interface{}{
				"id":                   partnerUserID,
				"name":                 partner.DisplayName(),
				"level":                partner.Level,
				"region":               partner.Region,
				"photo_url":            partner.PhotoURL,
				"pinned_speaking_band": partner.PinnedSpeakingBand,
			},
			"topic":         topic,
			"limit_minutes": session.LimitMinutes,
			"is_caller":     isCaller,
			"scheduled_id":  s.ID.String(),
		}
	}

	// The joiner who completed the pair is the caller: they are provably
	// on screen this instant, so their offer will not be sent into a void.
	if c1 := H.GetClientByUserID(joinerID); c1 != nil {
		c1.SendJSON("match_found", matchData(user2, partnerID, true))
	}
	if c2 := H.GetClientByUserID(partnerID); c2 != nil {
		c2.SendJSON("match_found", matchData(user1, joinerID, false))
	}

	log.Info().
		Str("scheduled_id", s.ID.String()).
		Str("session_id", sessionID).
		Msg("Scheduled session started")

	safego.Go("scheduledStartedPulse", func() {
		for _, uid := range []string{joinerID, partnerID} {
			SendToUser(uid, "scheduled_update", map[string]interface{}{
				"reason":       "started",
				"id":           s.ID.String(),
				"session_id":   sessionID,
				"scheduled_id": s.ID.String(),
			})
		}
	})
}
