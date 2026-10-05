package handlers

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
	"github.com/speak-up/backend/internal/ws"
)

// Scheduled speaking appointments between friends.
//
// Creating one is premium-only; receiving, confirming and joining are
// free. That split is the product: the free partner gets the full
// experience of being invited, which is the best possible argument for
// buying the ability to do the inviting.

// scheduledJSON is what the client renders. The live/joinable flags are
// computed server-side from one clock - two devices with skewed clocks
// deciding independently whether a window is open is a bug waiting to
// happen.
type scheduledJSON struct {
	ID          uuid.UUID       `json:"id"`
	ScheduledAt time.Time       `json:"scheduled_at"`
	Status      string          `json:"status"`
	Note        string          `json:"note"`
	Partner     *friendUserJSON `json:"partner"`
	// IsOrganiser tells the client whose appointment this is: only the
	// organiser sees "cancel", only the invitee sees "confirm".
	IsOrganiser bool `json:"is_organiser"`
	// Live is true while the join window is open.
	Live bool `json:"live"`
	// WindowEndsAt lets the client run the 10-minute countdown without
	// re-deriving the rule.
	WindowEndsAt time.Time `json:"window_ends_at"`
	// PartnerWaiting is true when the other side is already standing in
	// the join window - the single most motivating thing to show.
	PartnerWaiting bool       `json:"partner_waiting"`
	SessionID      *uuid.UUID `json:"session_id"`
	CreatedAt      time.Time  `json:"created_at"`
}

func toScheduledJSON(s *models.ScheduledSession, viewerID uuid.UUID) scheduledJSON {
	now := time.Now()
	isOrganiser := s.User1ID == viewerID

	partner := s.User2
	if !isOrganiser {
		partner = s.User1
	}

	live := s.IsLive(now)
	out := scheduledJSON{
		ID:           s.ID,
		ScheduledAt:  s.ScheduledAt,
		Status:       s.Status,
		Note:         s.Note,
		Partner:      toFriendUser(partner),
		IsOrganiser:  isOrganiser,
		Live:         live,
		WindowEndsAt: s.WindowEnd(),
		SessionID:    s.SessionID,
		CreatedAt:    s.CreatedAt,
	}
	if live {
		out.PartnerWaiting = services.IsScheduledPresent(s.ID, s.Partner(viewerID).String())
	}
	return out
}

func scheduledErrorStatus(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, services.ErrSchedulePremium):
		// The frontend branches on this code to open the upgrade dialog
		// instead of showing a dead-end toast.
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "PREMIUM_REQUIRED", err.Error())
	case errors.Is(err, services.ErrScheduleNotFriend):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "NOT_FRIENDS", err.Error())
	case errors.Is(err, services.ErrScheduleTooSoon):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "TOO_SOON", err.Error())
	case errors.Is(err, services.ErrScheduleTooFar):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "TOO_FAR", err.Error())
	case errors.Is(err, services.ErrScheduleClash):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "TIME_CLASH", err.Error())
	case errors.Is(err, services.ErrScheduleLimit):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "TOO_MANY", err.Error())
	case errors.Is(err, services.ErrScheduleNotFound):
		return utils.ErrorWithCode(c, fiber.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, services.ErrFriendSelf):
		return utils.BadRequest(c, err.Error())
	default:
		return utils.InternalError(c)
	}
}

type createScheduledRequest struct {
	PartnerID string `json:"partner_id"`
	// RFC3339, UTC. The client owns the timezone conversion; the server
	// stores one unambiguous instant.
	ScheduledAt string `json:"scheduled_at"`
	Note        string `json:"note"`
}

// CreateScheduled handles POST /scheduled
func CreateScheduled(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req createScheduledRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	partnerID, err := uuid.Parse(req.PartnerID)
	if err != nil {
		return utils.BadRequest(c, "Sherik ID noto'g'ri")
	}

	at, err := time.Parse(time.RFC3339, req.ScheduledAt)
	if err != nil {
		return utils.BadRequest(c, "Vaqt formati noto'g'ri (RFC3339)")
	}

	note := strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(note) > 256 {
		return utils.BadRequest(c, "Izoh juda uzun")
	}

	s, serr := services.CreateScheduledSession(user, partnerID, at, note)
	if serr != nil {
		return scheduledErrorStatus(c, serr)
	}

	notifyScheduleCreated(user, s)
	ws.SendToUser(partnerID.String(), "scheduled_update", fiber.Map{
		"reason": "created",
		"id":     s.ID,
	})

	return utils.Created(c, toScheduledJSON(s, user.ID))
}

// ListScheduled handles GET /scheduled - upcoming plus anything live.
func ListScheduled(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	items, err := services.ListScheduledFor(user.ID)
	if err != nil {
		return utils.InternalError(c)
	}

	out := make([]scheduledJSON, 0, len(items))
	for i := range items {
		out = append(out, toScheduledJSON(&items[i], user.ID))
	}
	return utils.Success(c, out)
}

// ListScheduledHistory handles GET /scheduled/history
func ListScheduledHistory(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	items, err := services.ListScheduledHistory(user.ID, c.QueryInt("limit", 20))
	if err != nil {
		return utils.InternalError(c)
	}

	out := make([]scheduledJSON, 0, len(items))
	for i := range items {
		out = append(out, toScheduledJSON(&items[i], user.ID))
	}
	return utils.Success(c, out)
}

// GetScheduled handles GET /scheduled/:id
func GetScheduled(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "ID noto'g'ri")
	}

	s, serr := services.LoadScheduledSession(id)
	if serr != nil {
		return scheduledErrorStatus(c, serr)
	}
	if s.User1ID != user.ID && s.User2ID != user.ID {
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "NOT_FOUND", "Reja topilmadi")
	}

	return utils.Success(c, toScheduledJSON(s, user.ID))
}

// ConfirmScheduled handles POST /scheduled/:id/confirm
func ConfirmScheduled(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "ID noto'g'ri")
	}

	s, serr := services.ConfirmScheduled(id, user.ID)
	if serr != nil {
		return scheduledErrorStatus(c, serr)
	}

	notifyScheduleAnswered(s, user, true)
	ws.SendToUser(s.User1ID.String(), "scheduled_update", fiber.Map{
		"reason": "confirmed",
		"id":     s.ID,
	})

	return utils.Success(c, toScheduledJSON(s, user.ID))
}

// DeclineScheduled handles POST /scheduled/:id/decline
func DeclineScheduled(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "ID noto'g'ri")
	}

	s, serr := services.DeclineScheduled(id, user.ID)
	if serr != nil {
		return scheduledErrorStatus(c, serr)
	}

	notifyScheduleAnswered(s, user, false)
	ws.SendToUser(s.User1ID.String(), "scheduled_update", fiber.Map{
		"reason": "declined",
		"id":     s.ID,
	})

	return utils.Success(c, toScheduledJSON(s, user.ID))
}

// CancelScheduled handles DELETE /scheduled/:id
func CancelScheduled(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "ID noto'g'ri")
	}

	s, serr := services.CancelScheduled(id, user.ID)
	if serr != nil {
		return scheduledErrorStatus(c, serr)
	}

	// Whoever didn't press cancel is the one who needs telling.
	otherID := s.Partner(user.ID)
	notifyScheduleCancelled(s, user, otherID)
	ws.SendToUser(otherID.String(), "scheduled_update", fiber.Map{
		"reason": "cancelled",
		"id":     s.ID,
	})

	return utils.Success(c, toScheduledJSON(s, user.ID))
}

// --- Telegram side ---

func notifyScheduleCreated(organiser *models.User, s *models.ScheduledSession) {
	partner, err := services.GetUserByID(s.User2ID)
	if err != nil || partner == nil || partner.TelegramID == 0 {
		return
	}
	tgID := partner.TelegramID
	name := organiser.DisplayName()
	at := s.ScheduledAt
	note := s.Note
	safego.Go("notifyScheduleCreated", func() {
		bot.SendScheduleInvite(tgID, name, at, note)
	})
}

func notifyScheduleAnswered(s *models.ScheduledSession, answerer *models.User, accepted bool) {
	organiser, err := services.GetUserByID(s.User1ID)
	if err != nil || organiser == nil || organiser.TelegramID == 0 {
		return
	}
	tgID := organiser.TelegramID
	name := answerer.DisplayName()
	at := s.ScheduledAt
	safego.Go("notifyScheduleAnswered", func() {
		bot.SendScheduleAnswer(tgID, name, at, accepted)
	})
}

func notifyScheduleCancelled(s *models.ScheduledSession, canceller *models.User, toID uuid.UUID) {
	target, err := services.GetUserByID(toID)
	if err != nil || target == nil || target.TelegramID == 0 {
		return
	}
	tgID := target.TelegramID
	name := canceller.DisplayName()
	at := s.ScheduledAt
	safego.Go("notifyScheduleCancelled", func() {
		bot.SendScheduleCancelled(tgID, name, at)
	})
}
