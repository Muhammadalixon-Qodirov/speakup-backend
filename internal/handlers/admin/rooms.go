package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// Admin room management.
//
// Rooms are a B2B product: we open one for a learning centre, assign it
// to their teacher, and they bring their own students in through the
// invite link. There is deliberately NO self-service creation endpoint -
// every room passes through this file, which is what keeps the feature
// a controlled offering rather than a free-for-all.

type adminRoomJSON struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	Description   *string    `json:"description"`
	Code          string     `json:"code"`
	InviteLink    string     `json:"invite_link"`
	OwnerID       uuid.UUID  `json:"owner_id"`
	OwnerName     string     `json:"owner_name"`
	OwnerUsername *string    `json:"owner_username"`
	OwnerTgID     int64      `json:"owner_telegram_id"`
	IsActive      bool       `json:"is_active"`
	IsOpen        bool       `json:"is_open"`
	MemberCount   int        `json:"member_count"`
	MaxMembers    int        `json:"max_members"`
	TotalSessions int        `json:"total_sessions"`
	TotalMinutes  int        `json:"total_minutes"`
	ExpiresAt     *time.Time `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

func toAdminRoomJSON(r *models.Room) adminRoomJSON {
	out := adminRoomJSON{
		ID:            r.ID,
		Name:          r.Name,
		Description:   r.Description,
		Code:          r.Code,
		InviteLink:    services.RoomInviteLink(r.Code),
		OwnerID:       r.OwnerID,
		IsActive:      r.IsActive,
		IsOpen:        r.IsOpen(),
		MemberCount:   r.MemberCount,
		MaxMembers:    r.MaxMembers,
		TotalSessions: r.TotalSessions,
		TotalMinutes:  r.TotalMinutes,
		ExpiresAt:     r.ExpiresAt,
		CreatedAt:     r.CreatedAt,
	}
	if r.Owner != nil {
		out.OwnerName = r.Owner.DisplayName()
		out.OwnerUsername = r.Owner.Username
		out.OwnerTgID = r.Owner.TelegramID
	}
	return out
}

// ListRooms handles GET /admin/rooms?search=&limit=&offset=
func ListRooms(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 50)
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := c.QueryInt("offset", 0)
	if offset < 0 {
		offset = 0
	}
	search := c.Query("search")

	q := database.DB.Model(&models.Room{})
	if search != "" {
		// Match on room name OR the owner's name/username, so support can
		// find "that centre in Chilonzor" without knowing the room name.
		like := "%" + search + "%"
		q = q.Joins("JOIN users ON users.id = rooms.owner_id").
			Where("rooms.name ILIKE ? OR users.first_name ILIKE ? OR users.username ILIKE ? OR rooms.code ILIKE ?",
				like, like, like, like)
	}

	var total int64
	q.Count(&total)

	var rooms []models.Room
	if err := q.Preload("Owner").
		Order("rooms.created_at DESC").
		Limit(limit).Offset(offset).
		Find(&rooms).Error; err != nil {
		return utils.InternalError(c)
	}

	out := make([]adminRoomJSON, 0, len(rooms))
	for i := range rooms {
		out = append(out, toAdminRoomJSON(&rooms[i]))
	}

	return utils.Success(c, fiber.Map{
		"rooms":  out,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

type createRoomRequest struct {
	// Exactly one owner identifier is required. OwnerID is what the admin
	// UI sends after picking a user; OwnerTelegramID is the convenience
	// path for "the centre gave me their teacher's Telegram ID".
	OwnerID         string `json:"owner_id"`
	OwnerTelegramID int64  `json:"owner_telegram_id"`

	Name        string `json:"name"`
	Description string `json:"description"`
	MaxMembers  int    `json:"max_members"`
	// ExpiresInDays > 0 sells the room for a fixed term; 0 = never expires.
	ExpiresInDays int `json:"expires_in_days"`

	// CenterID attaches the room to an EXISTING centre.
	//
	// When empty, a centre is created automatically and named after the
	// room. That is what gives the teacher a profile they can put a logo
	// and contact details on - without it, "room ochib berdik" would
	// leave them with a room they cannot brand. The teacher never has to
	// know the word "centre": the frontend labels /centers/mine as the
	// room's own settings screen.
	CenterID string `json:"center_id"`
	// SkipCenter opts out of that auto-creation, for the rare room that
	// is ours rather than a partner's (an internal test room, say).
	SkipCenter bool `json:"skip_center"`
}

// CreateRoom handles POST /admin/rooms
func CreateRoom(c *fiber.Ctx) error {
	var req createRoomRequest
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

	room, err := services.CreateRoom(ownerID, req.Name, req.Description, req.MaxMembers, expiresAt)
	if err != nil {
		return utils.BadRequest(c, err.Error())
	}

	// Give the room a profile the teacher can actually fill in. Failures
	// are logged, not fatal: a room without a centre still works for
	// speaking, and forcing the admin to retry the whole creation over a
	// profile record would be worse than leaving it to be attached later.
	center, cerr := attachOrCreateCenter(room, ownerID, req.CenterID, req.SkipCenter, expiresAt)
	if cerr != nil {
		return utils.BadRequest(c, cerr.Error())
	}

	out := fiber.Map{"room": toAdminRoomJSON(room)}
	if center != nil {
		out["center"] = center
	}
	return utils.Created(c, out)
}

// attachOrCreateCenter wires a freshly created room to a centre profile.
//
// Three paths:
//   - center_id given  → attach to that existing centre (a second group
//     for a partner we already work with)
//   - skip_center      → no profile at all
//   - default          → create a centre named after the room, owned by
//     the same teacher, so /centers/mine gives them somewhere to upload
//     a logo and contact details
//
// A centre created here starts with advertising OFF: the banner is a
// commercial term, never an automatic consequence of opening a room.
func attachOrCreateCenter(
	room *models.Room,
	ownerID uuid.UUID,
	rawCenterID string,
	skip bool,
	expiresAt *time.Time,
) (*models.StudyCenter, error) {
	if rawCenterID != "" {
		centerID, err := uuid.Parse(rawCenterID)
		if err != nil {
			return nil, errBadCenter
		}
		if err := services.AttachRoomToCenter(room.ID, &centerID); err != nil {
			return nil, err
		}
		return services.GetCenterByID(centerID)
	}

	if skip {
		return nil, nil
	}

	// If this teacher already runs a centre, reuse it rather than
	// creating a second one they'd have to fill in twice.
	if existing, err := services.GetCenterByOwner(ownerID); err == nil && existing != nil {
		if aerr := services.AttachRoomToCenter(room.ID, &existing.ID); aerr != nil {
			return nil, aerr
		}
		return existing, nil
	}

	center, err := services.CreateCenter(ownerID, room.Name, expiresAt)
	if err != nil {
		return nil, err
	}
	if aerr := services.AttachRoomToCenter(room.ID, &center.ID); aerr != nil {
		return nil, aerr
	}
	return center, nil
}

const errBadCenter = roomErr("center_id noto'g'ri")

// resolveOwnerID turns either identifier into a user UUID.
func resolveOwnerID(rawUUID string, tgID int64) (uuid.UUID, error) {
	if rawUUID != "" {
		id, err := uuid.Parse(rawUUID)
		if err != nil {
			return uuid.Nil, errBadOwner
		}
		return id, nil
	}
	if tgID != 0 {
		var u models.User
		if err := database.DB.First(&u, "telegram_id = ?", tgID).Error; err != nil {
			return uuid.Nil, errOwnerNotFound
		}
		return u.ID, nil
	}
	return uuid.Nil, errBadOwner
}

type roomErr string

func (e roomErr) Error() string { return string(e) }

const (
	errBadOwner      = roomErr("owner_id yoki owner_telegram_id talab qilinadi")
	errOwnerNotFound = roomErr("Bunday Telegram ID bilan foydalanuvchi topilmadi")
)

type updateRoomRequest struct {
	Name          *string `json:"name"`
	Description   *string `json:"description"`
	MaxMembers    *int    `json:"max_members"`
	IsActive      *bool   `json:"is_active"`
	ExpiresInDays *int    `json:"expires_in_days"` // 0 clears the expiry
	OwnerID       *string `json:"owner_id"`        // transfer ownership
}

// UpdateRoom handles PUT /admin/rooms/:id
func UpdateRoom(c *fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Room ID noto'g'ri")
	}

	var req updateRoomRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.MaxMembers != nil && *req.MaxMembers > 0 {
		updates["max_members"] = *req.MaxMembers
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.ExpiresInDays != nil {
		if *req.ExpiresInDays <= 0 {
			updates["expires_at"] = nil
		} else {
			updates["expires_at"] = time.Now().AddDate(0, 0, *req.ExpiresInDays)
		}
	}

	// Ownership transfer also moves the owner's membership row, otherwise
	// the new teacher would own a room they aren't a member of.
	if req.OwnerID != nil && *req.OwnerID != "" {
		newOwner, perr := uuid.Parse(*req.OwnerID)
		if perr != nil {
			return utils.BadRequest(c, "owner_id noto'g'ri")
		}
		if err := services.TransferRoomOwnership(roomID, newOwner); err != nil {
			return utils.BadRequest(c, err.Error())
		}
	}

	room, err := services.UpdateRoom(roomID, updates)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, toAdminRoomJSON(room))
}

// RegenerateRoomCode handles POST /admin/rooms/:id/regenerate-code
// Invalidates every previously shared invite link at once.
func RegenerateRoomCode(c *fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Room ID noto'g'ri")
	}

	room, err := services.RegenerateRoomCode(roomID)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, toAdminRoomJSON(room))
}

// DeleteRoom handles DELETE /admin/rooms/:id
func DeleteRoom(c *fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Room ID noto'g'ri")
	}

	if err := services.DeleteRoom(roomID); err != nil {
		return utils.InternalError(c)
	}

	return utils.SuccessMessage(c, "Room o'chirildi")
}

// GetRoomMembers handles GET /admin/rooms/:id/members
func GetRoomMembers(c *fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Room ID noto'g'ri")
	}

	members, err := services.ListRoomMembers(roomID)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, members)
}

// GetRoomSessions handles GET /admin/rooms/:id/sessions
func GetRoomSessions(c *fiber.Ctx) error {
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Room ID noto'g'ri")
	}

	limit := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)

	sessions, total, err := services.ListRoomSessions(roomID, limit, offset)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, fiber.Map{
		"sessions": sessions,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
	})
}
