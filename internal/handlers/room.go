package handlers

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
	"github.com/speak-up/backend/internal/ws"
)

// Room REST API - the student and teacher side. Room CREATION lives in
// the admin package: rooms are handed to learning centres by us, never
// self-served.

// roomJSON is the shape every room-returning endpoint uses. The invite
// link is only ever attached for the owner - it is the room's password.
type roomJSON struct {
	ID            uuid.UUID  `json:"id"`
	Name          string     `json:"name"`
	Description   *string    `json:"description"`
	OwnerID       uuid.UUID  `json:"owner_id"`
	OwnerName     string     `json:"owner_name"`
	IsActive      bool       `json:"is_active"`
	IsOpen        bool       `json:"is_open"`
	MemberCount   int        `json:"member_count"`
	MaxMembers    int        `json:"max_members"`
	TotalSessions int        `json:"total_sessions"`
	TotalMinutes  int        `json:"total_minutes"`
	ExpiresAt     *time.Time `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`

	// Owner-only fields.
	Code       string `json:"code,omitempty"`
	InviteLink string `json:"invite_link,omitempty"`
	IsOwner    bool   `json:"is_owner"`
}

func toRoomJSON(r *models.Room, viewerID uuid.UUID) roomJSON {
	out := roomJSON{
		ID:            r.ID,
		Name:          r.Name,
		Description:   r.Description,
		OwnerID:       r.OwnerID,
		IsActive:      r.IsActive,
		IsOpen:        r.IsOpen(),
		MemberCount:   r.MemberCount,
		MaxMembers:    r.MaxMembers,
		TotalSessions: r.TotalSessions,
		TotalMinutes:  r.TotalMinutes,
		ExpiresAt:     r.ExpiresAt,
		CreatedAt:     r.CreatedAt,
		IsOwner:       r.OwnerID == viewerID,
	}
	if r.Owner != nil {
		out.OwnerName = r.Owner.DisplayName()
	}
	// Only the owner receives the code / link - anyone holding it can
	// walk into the room.
	if out.IsOwner {
		out.Code = r.Code
		out.InviteLink = services.RoomInviteLink(r.Code)
	}
	return out
}

// roomErrorStatus maps a service error onto an HTTP response.
func roomErrorStatus(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, services.ErrRoomNotFound):
		return utils.ErrorWithCode(c, fiber.StatusNotFound, "ROOM_NOT_FOUND", "Room topilmadi")
	case errors.Is(err, services.ErrRoomClosed):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "ROOM_CLOSED", "Room yopilgan")
	case errors.Is(err, services.ErrRoomFull):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "ROOM_FULL", "Room to'lgan")
	case errors.Is(err, services.ErrNotRoomMember):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "NOT_A_MEMBER", "Siz bu room a'zosi emassiz")
	case errors.Is(err, services.ErrNotRoomOwner):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "NOT_OWNER", "Faqat room egasi uchun")
	case errors.Is(err, services.ErrOwnerCantLeave):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "OWNER_CANT_LEAVE", "Room egasi o'z roomidan chiqa olmaydi")
	default:
		return utils.InternalError(c)
	}
}

// requireRoomMember loads the room and checks the caller is inside it.
func requireRoomMember(c *fiber.Ctx) (*models.Room, *models.User, error) {
	user := middleware.GetCurrentUser(c)
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return nil, nil, services.ErrRoomNotFound
	}
	room, err := services.GetRoomByID(roomID)
	if err != nil {
		return nil, nil, err
	}
	if !services.IsRoomMember(room.ID, user.ID) {
		return nil, nil, services.ErrNotRoomMember
	}
	return room, user, nil
}

// requireRoomOwner is requireRoomMember + ownership. Platform admins pass
// too, so support can inspect a room without joining it.
func requireRoomOwner(c *fiber.Ctx) (*models.Room, *models.User, error) {
	user := middleware.GetCurrentUser(c)
	roomID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return nil, nil, services.ErrRoomNotFound
	}
	room, err := services.GetRoomByID(roomID)
	if err != nil {
		return nil, nil, err
	}
	if room.OwnerID != user.ID && !user.IsAdmin {
		return nil, nil, services.ErrNotRoomOwner
	}
	return room, user, nil
}

// GetMyRooms handles GET /rooms/mine
//
// Returns the rooms the caller OWNS (their teaching spaces, with invite
// link) and the rooms they've JOINED (classes they attend). The profile
// screen renders "Mening roomlarim" from this.
func GetMyRooms(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	owned, joined, err := services.ListMyRooms(user.ID)
	if err != nil {
		return utils.InternalError(c)
	}

	ownedOut := make([]roomJSON, 0, len(owned))
	for i := range owned {
		// ListMyRooms doesn't preload Owner for owned rooms - it's the
		// caller, so fill the name in directly.
		owned[i].Owner = user
		ownedOut = append(ownedOut, toRoomJSON(&owned[i], user.ID))
	}

	joinedOut := make([]roomJSON, 0, len(joined))
	for i := range joined {
		joinedOut = append(joinedOut, toRoomJSON(&joined[i], user.ID))
	}

	return utils.Success(c, fiber.Map{
		"owned":  ownedOut,
		"joined": joinedOut,
	})
}

// PreviewRoom handles GET /rooms/preview/:code
//
// What a student sees after tapping an invite link but BEFORE joining:
// room name, teacher, size. Deliberately minimal - a valid code is the
// only thing that reveals anything at all.
func PreviewRoom(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	room, err := services.GetRoomByCode(c.Params("code"))
	if err != nil {
		return roomErrorStatus(c, err)
	}

	ownerName := ""
	if room.Owner != nil {
		ownerName = room.Owner.DisplayName()
	}

	return utils.Success(c, fiber.Map{
		"id":             room.ID,
		"name":           room.Name,
		"description":    room.Description,
		"owner_name":     ownerName,
		"member_count":   room.MemberCount,
		"max_members":    room.MaxMembers,
		"is_open":        room.IsOpen(),
		"already_member": services.IsRoomMember(room.ID, user.ID),
	})
}

type joinRoomRequest struct {
	Code string `json:"code"`
}

// JoinRoom handles POST /rooms/join
//
// Idempotent - students tap the invite link every lesson to open the app,
// so a repeat join must be a success, not an error.
func JoinRoom(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req joinRoomRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}
	if services.NormalizeRoomCode(req.Code) == "" {
		return utils.BadRequest(c, "Room kodi talab qilinadi")
	}

	room, err := services.JoinRoomByCode(user.ID, req.Code)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	// The teacher's live panel should show the new student immediately.
	ws.BroadcastRoomState(room.ID)

	return utils.Success(c, toRoomJSON(room, user.ID))
}

// GetRoom handles GET /rooms/:id
//
// The room screen: metadata plus the live panel snapshot (who is online,
// searching, speaking). Members only.
func GetRoom(c *fiber.Ctx) error {
	room, user, err := requireRoomMember(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	return utils.Success(c, fiber.Map{
		"room":  toRoomJSON(room, user.ID),
		"state": ws.BuildRoomState(room),
	})
}

// GetRoomMembers handles GET /rooms/:id/members
//
// Every member sees the roster - students want to know who else is in
// their class, and it's the same data the live panel already streams.
func GetRoomMembers(c *fiber.Ctx) error {
	room, _, err := requireRoomMember(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	state := ws.BuildRoomState(room)
	return utils.Success(c, state["members"])
}

// LeaveRoom handles POST /rooms/:id/leave
func LeaveRoom(c *fiber.Ctx) error {
	room, user, err := requireRoomMember(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	if err := services.LeaveRoom(room.ID, user.ID); err != nil {
		return roomErrorStatus(c, err)
	}

	// Drop them out of the room queue too, otherwise a departed student
	// could still be handed a partner.
	services.RemoveFromRoomQueue(user.ID.String())
	ws.BroadcastRoomState(room.ID)

	return utils.SuccessMessage(c, "Roomdan chiqdingiz")
}

// RemoveRoomMember handles DELETE /rooms/:id/members/:user_id
// Owner (or platform admin) only.
func RemoveRoomMember(c *fiber.Ctx) error {
	room, _, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	targetID, perr := uuid.Parse(c.Params("user_id"))
	if perr != nil {
		return utils.BadRequest(c, "Foydalanuvchi ID noto'g'ri")
	}

	if err := services.RemoveRoomMember(room.ID, targetID); err != nil {
		return roomErrorStatus(c, err)
	}

	// Removing the roster row is not enough on its own: cut the queue
	// entry AND any call they are currently in, otherwise a student
	// removed mid-lesson keeps talking until someone hangs up.
	services.RemoveFromRoomQueue(targetID.String())
	ws.EndRoomSessionsFor(room.ID, targetID.String(), "removed_from_room")
	ws.BroadcastRoomState(room.ID)

	return utils.SuccessMessage(c, "A'zo chiqarildi")
}

// --- Owner administration ---
//
// The room owner is the admin OF THEIR ROOM: they rename it, freeze it,
// rotate the invite link and remove students. What they deliberately
// CANNOT do is anything that would turn a room into a self-service
// product - create another room, raise their own member cap, or extend
// their paid term. Those stay with the platform admin.

type updateRoomRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	// IsActive false freezes the room: no joins, no matching. Useful
	// between lessons so students can't wander in and pair up unattended.
	IsActive *bool `json:"is_active"`
}

// UpdateRoom handles PUT /rooms/:id — owner only.
func UpdateRoom(c *fiber.Ctx) error {
	room, user, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	var req updateRoomRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return utils.BadRequest(c, "Room nomi bo'sh bo'lishi mumkin emas")
		}
		if utf8.RuneCountInString(name) > 128 {
			return utils.BadRequest(c, "Room nomi juda uzun")
		}
		updates["name"] = name
	}
	if req.Description != nil {
		desc := strings.TrimSpace(*req.Description)
		if utf8.RuneCountInString(desc) > 512 {
			return utils.BadRequest(c, "Tavsif juda uzun")
		}
		updates["description"] = desc
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}

	updated, err := services.UpdateRoom(room.ID, updates)
	if err != nil {
		return utils.InternalError(c)
	}

	// Freezing a room must also empty its queue, otherwise students who
	// were already waiting would still get matched into it.
	if req.IsActive != nil && !*req.IsActive {
		services.ClearRoomQueue(room.ID.String())
	}

	ws.BroadcastRoomState(room.ID)

	return utils.Success(c, toRoomJSON(updated, user.ID))
}

// RegenerateRoomCode handles POST /rooms/:id/regenerate-code — owner only.
//
// This is the "our link leaked into a public chat" button. Every
// previously shared link stops working at once; existing members keep
// their access, because they are already rows in room_members.
func RegenerateRoomCode(c *fiber.Ctx) error {
	room, user, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	updated, err := services.RegenerateRoomCode(room.ID)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, toRoomJSON(updated, user.ID))
}

// GetRoomSessions handles GET /rooms/:id/sessions?limit=&offset=
// Owner only - this is the lesson log.
func GetRoomSessions(c *fiber.Ctx) error {
	room, _, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	limit := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)

	sessions, total, err := services.ListRoomSessions(room.ID, limit, offset)
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

// GetRoomStudents handles GET /rooms/:id/students
//
// The teacher's main table: every member with today's attendance, their
// in-room totals, streak and AI band. Owner only - this is the class
// register, not something classmates browse.
func GetRoomStudents(c *fiber.Ctx) error {
	room, _, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	students, serr := services.RoomStudentsOverview(room.ID)
	if serr != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, students)
}

// GetRoomStudent handles GET /rooms/:id/students/:user_id
//
// One student's full card: room activity, platform-wide effort, streak
// and their AI speaking history. Owner only.
func GetRoomStudent(c *fiber.Ctx) error {
	room, _, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	studentID, perr := uuid.Parse(c.Params("user_id"))
	if perr != nil {
		return utils.BadRequest(c, "Foydalanuvchi ID noto'g'ri")
	}

	activity, aerr := services.RoomStudentActivity(room.ID, studentID, c.QueryInt("ai_limit", 20))
	if aerr != nil {
		return roomErrorStatus(c, aerr)
	}

	return utils.Success(c, activity)
}

// GetRoomAttendance handles GET /rooms/:id/attendance?days=14
//
// Day-by-day attendance, including days nobody showed up - a chart that
// skips empty days draws a flat line over exactly the gaps a centre is
// paying to be able to see.
func GetRoomAttendance(c *fiber.Ctx) error {
	room, _, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	days, aerr := services.RoomDailyAttendance(room.ID, c.QueryInt("days", 14))
	if aerr != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, fiber.Map{"days": days})
}

// GetRoomReport handles GET /rooms/:id/report?days=7
//
// Per-student attendance for the last N days: sessions, minutes and the
// number of distinct days they showed up. Owner only.
func GetRoomReport(c *fiber.Ctx) error {
	room, _, err := requireRoomOwner(c)
	if err != nil {
		return roomErrorStatus(c, err)
	}

	days := c.QueryInt("days", 7)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}

	until := time.Now()
	since := until.AddDate(0, 0, -days)

	rows, err := services.RoomReport(room.ID, since, until)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, fiber.Map{
		"days":  days,
		"since": since,
		"until": until,
		"rows":  rows,
	})
}
