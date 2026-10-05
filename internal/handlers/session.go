package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
	"github.com/speak-up/backend/internal/ws"
)

type RateSessionRequest struct {
	Rating   int     `json:"rating" validate:"required,min=1,max=5"`
	Feedback *string `json:"feedback"`
}

// GetSession handles GET /sessions/:id
func GetSession(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	sessionID := c.Params("id")

	session, err := services.GetSession(sessionID, user.ID.String())
	if err != nil {
		return utils.NotFound(c, "Sessiya topilmadi")
	}

	return utils.Success(c, session)
}

// EndSession handles POST /sessions/:id/end
func EndSession(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	sessionID := c.Params("id")

	session, err := services.EndSession(sessionID, user.ID.String())
	if err != nil {
		return utils.InternalError(c)
	}
	if session == nil {
		return utils.NotFound(c, "Sessiya topilmadi yoki allaqachon tugagan")
	}

	minutes := session.DurationMinutes()

	// Detach any teacher who was listening in, so their client stops
	// waiting on audio that will never arrive.
	ws.DetachListenersOnSessionEnd(sessionID)

	// Room calls are free - their minutes go into the non-billing window
	// and the room's attendance report instead of the daily limit. Same
	// split as the WebSocket session_end path; this REST endpoint is the
	// fallback used when the socket is already gone.
	if session.RoomID != nil {
		services.IncrementRoomUsage(session.User1ID.String(), minutes, sessionID)
		services.IncrementRoomUsage(session.User2ID.String(), minutes, sessionID)
		services.RecordRoomSessionEnd(*session.RoomID, session.User1ID, session.User2ID, minutes)
	} else {
		services.IncrementUsage(session.User1ID.String(), minutes)
		services.IncrementUsage(session.User2ID.String(), minutes)
	}

	return utils.SuccessMessage(c, "Sessiya tugadi")
}

// RateSession handles POST /sessions/:id/rate
func RateSession(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	sessionID := c.Params("id")

	var req RateSessionRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	if req.Rating < 1 || req.Rating > 5 {
		return utils.BadRequest(c, "Baho 1 dan 5 gacha bo'lishi kerak")
	}

	if err := services.RateSession(sessionID, user.ID.String(), req.Rating, req.Feedback); err != nil {
		return utils.InternalError(c)
	}

	return utils.SuccessMessage(c, "Baho qo'yildi")
}
