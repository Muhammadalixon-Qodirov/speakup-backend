package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// Admin management of partner learning centres.
//
// Two responsibilities live here and nowhere else:
//   1. CREATION - a centre can never sign itself up, otherwise the
//      in-session banner becomes free advertising for anyone.
//   2. MODERATION - a centre's own words and artwork reach other users'
//      screens, so a human at SpeakUp approves them first.

// ListCenters handles GET /admin/centers?status=&search=&limit=&offset=
//
// `status=pending` is the moderation queue - the view an admin actually
// works from.
func ListCenters(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := c.QueryInt("offset", 0)
	if offset < 0 {
		offset = 0
	}

	q := database.DB.Model(&models.StudyCenter{})

	if status := c.Query("status"); status != "" {
		q = q.Where("moderation_status = ?", status)
	}
	if search := c.Query("search"); search != "" {
		like := "%" + search + "%"
		q = q.Joins("JOIN users ON users.id = study_centers.owner_id").
			Where("study_centers.name ILIKE ? OR users.first_name ILIKE ? OR users.username ILIKE ?",
				like, like, like)
	}

	var total int64
	q.Count(&total)

	var centers []models.StudyCenter
	if err := q.Preload("Owner").
		// Pending first: the queue should open on the work, not on the
		// alphabetically luckiest partner.
		Order("moderation_status = 'pending' DESC, study_centers.created_at DESC").
		Limit(limit).Offset(offset).
		Find(&centers).Error; err != nil {
		return utils.InternalError(c)
	}

	var pendingCount int64
	database.DB.Model(&models.StudyCenter{}).
		Where("moderation_status = ?", models.CenterModerationPending).
		Count(&pendingCount)

	return utils.Success(c, fiber.Map{
		"centers":       centers,
		"total":         total,
		"pending_count": pendingCount,
		"limit":         limit,
		"offset":        offset,
	})
}

// GetCenter handles GET /admin/centers/:id
func GetCenter(c *fiber.Ctx) error {
	centerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Markaz ID noto'g'ri")
	}

	center, cerr := services.GetCenterByID(centerID)
	if cerr != nil {
		return utils.NotFound(c, "Markaz topilmadi")
	}

	rooms, _ := services.CenterRooms(centerID)

	return utils.Success(c, fiber.Map{
		"center": center,
		"rooms":  rooms,
	})
}

type createCenterRequest struct {
	OwnerID         string `json:"owner_id"`
	OwnerTelegramID int64  `json:"owner_telegram_id"`
	Name            string `json:"name"`
	// ExpiresInDays > 0 sells the partnership for a fixed term.
	ExpiresInDays int `json:"expires_in_days"`
	// RoomID optionally attaches an existing room to the new centre, so
	// opening a centre for a teacher who already has a room is one call.
	RoomID string `json:"room_id"`
}

// CreateCenter handles POST /admin/centers
func CreateCenter(c *fiber.Ctx) error {
	var req createCenterRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	ownerID, err := resolveOwnerID(req.OwnerID, req.OwnerTelegramID)
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}

	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().AddDate(0, 0, req.ExpiresInDays)
		expiresAt = &t
	}

	center, cerr := services.CreateCenter(ownerID, req.Name, expiresAt)
	if cerr != nil {
		return utils.BadRequest(c, cerr.Error())
	}

	if req.RoomID != "" {
		roomID, perr := uuid.Parse(req.RoomID)
		if perr != nil {
			return utils.BadRequest(c, "room_id noto'g'ri")
		}
		if aerr := services.AttachRoomToCenter(roomID, &center.ID); aerr != nil {
			return utils.BadRequest(c, aerr.Error())
		}
	}

	return utils.Created(c, center)
}

type moderateCenterRequest struct {
	Approve bool   `json:"approve"`
	Note    string `json:"note"`
}

// ModerateCenter handles POST /admin/centers/:id/moderate
//
// On rejection the note is shown to the centre, so it should say what to
// fix - "logotipda boshqa markaz nomi bor" beats a silent refusal.
func ModerateCenter(c *fiber.Ctx) error {
	admin := middleware.GetCurrentUser(c)

	centerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Markaz ID noto'g'ri")
	}

	var req moderateCenterRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}
	if !req.Approve && req.Note == "" {
		return utils.BadRequest(c, "Rad etish sababini yozing")
	}

	center, cerr := services.ModerateCenter(centerID, admin.ID, req.Approve, req.Note)
	if cerr != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, center)
}

type centerContractRequest struct {
	IsActive      *bool `json:"is_active"`
	IsAdvertised  *bool `json:"is_advertised"`
	ExpiresInDays *int  `json:"expires_in_days"`
}

// UpdateCenterContract handles PUT /admin/centers/:id/contract
//
// The commercial half of a centre: is the partnership live, does it
// include banner advertising, and until when. Kept apart from the
// profile endpoint so a content edit can never change the deal.
func UpdateCenterContract(c *fiber.Ctx) error {
	centerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Markaz ID noto'g'ri")
	}

	var req centerContractRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	center, cerr := services.SetCenterContract(centerID, req.IsActive, req.IsAdvertised, req.ExpiresInDays)
	if cerr != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, center)
}

type attachRoomRequest struct {
	// Empty string detaches the room from its centre.
	CenterID string `json:"center_id"`
}

// AttachRoomToCenter handles PUT /admin/rooms/:id/center
func AttachRoomToCenter(c *fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Room ID noto'g'ri")
	}

	var req attachRoomRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	var centerID *uuid.UUID
	if req.CenterID != "" {
		id, perr := uuid.Parse(req.CenterID)
		if perr != nil {
			return utils.BadRequest(c, "center_id noto'g'ri")
		}
		centerID = &id
	}

	if aerr := services.AttachRoomToCenter(roomID, centerID); aerr != nil {
		return utils.BadRequest(c, aerr.Error())
	}

	return utils.SuccessMessage(c, "Room markazga bog'landi")
}
