package handlers

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
	"github.com/speak-up/backend/internal/ws"
)

type TelegramAuthRequest struct {
	InitData string `json:"init_data" validate:"required"`
}

// TelegramAuth handles POST /auth/telegram
// Mini App (initData) orqali kirish.
func TelegramAuth(c *fiber.Ctx) error {
	var req TelegramAuthRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}
	if req.InitData == "" {
		return utils.BadRequest(c, "init_data talab qilinadi")
	}

	tgUser, startParam, err := services.VerifyTelegramInitDataWithParam(req.InitData)
	if err != nil {
		return utils.Unauthorized(c, "Telegram ma'lumotlari noto'g'ri")
	}

	user, err := services.UpsertUserWithRef(tgUser, startParam)
	if err != nil {
		return utils.InternalError(c)
	}

	// Refuse login if the user has blocked the bot. Our flows (match
	// notifications, weekly reports, OTP codes) all require bot messages
	// to work, and giving a blocked user a JWT would leave them unable
	// to receive anything and confused about why. The frontend renders
	// a dedicated "unblock the bot" screen on this code.
	if user.BotBlocked {
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "BOT_BLOCKED",
			"Siz botni bloklagansiz. Davom etish uchun botni blokdan oching va qayta kiring.")
	}

	token, err := services.CreateJWTWithSource(user.ID, services.AuthSourceMiniApp)
	if err != nil {
		return utils.InternalError(c)
	}

	resp := fiber.Map{
		"token": token,
		"user":  user,
	}

	// Room invite deep link: t.me/<bot>?startapp=room_<CODE>.
	//
	// The student is joined right here, at login, so tapping the teacher's
	// link is the entire onboarding - no code to type, no extra screen.
	// A failure is never fatal: they're logged in either way, and the
	// frontend just doesn't get a room to navigate to.
	if room := autoJoinRoomFromStartParam(user.ID, startParam); room != nil {
		resp["joined_room"] = fiber.Map{
			"id":   room.ID,
			"name": room.Name,
		}
	}

	return utils.Success(c, resp)
}

// autoJoinRoomFromStartParam joins the user to the room encoded in a
// Mini App start_param, if there is one. Returns nil when the param
// isn't a room link or the join couldn't be completed (closed, full,
// unknown code) - all of which are ordinary outcomes, not errors.
func autoJoinRoomFromStartParam(userID uuid.UUID, startParam string) *models.Room {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(startParam)), "room_") {
		return nil
	}

	room, err := services.JoinRoomByCode(userID, startParam)
	if err != nil {
		log.Warn().Err(err).
			Str("start_param", startParam).
			Str("user_id", userID.String()).
			Msg("room auto-join from deep link failed")
		return nil
	}

	// Refresh the teacher's live panel so the new student appears at once.
	ws.BroadcastRoomState(room.ID)

	return room
}

// RequestCode handles POST /auth/request-code
// Yangi auth sessiya yaratadi. Body kerak emas - bot hamma narsani qiladi.
func RequestCode(c *fiber.Ctx) error {
	result, err := services.GenerateOTPSession()
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, fiber.Map{
		"session_token": result.SessionToken,
		"bot_url":       result.BotURL,
	})
}

// CheckSession handles GET /auth/check-session/:token
// Frontend har 3 sekundda polling qiladi.
// pending → kutish, completed → JWT + user qaytaradi.
func CheckSession(c *fiber.Ctx) error {
	token := c.Params("token")
	if token == "" {
		return utils.BadRequest(c, "Sessiya tokeni talab qilinadi")
	}

	result, err := services.CheckOTPSession(token)
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}

	return utils.Success(c, result)
}
