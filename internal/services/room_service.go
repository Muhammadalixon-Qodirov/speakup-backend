package services

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Room-layer errors. Handlers map these onto HTTP codes, and the WS layer
// maps them onto `room_error` payloads, so they must stay stable.
var (
	ErrRoomNotFound   = errors.New("room not found")
	ErrRoomClosed     = errors.New("room is closed")
	ErrRoomFull       = errors.New("room is full")
	ErrNotRoomMember  = errors.New("not a member of this room")
	ErrNotRoomOwner   = errors.New("not the owner of this room")
	ErrOwnerCantLeave = errors.New("the owner cannot leave their own room")
)

// --- Creation / administration (platform admins only) ---

// CreateRoom mints a room and assigns it to `ownerID`. Called exclusively
// from the admin API: the product decision is that we hand rooms out to
// learning centres ourselves rather than letting anyone self-serve, so
// there is deliberately no user-facing create endpoint.
//
// The owner is inserted as a RoomMember with role "owner" in the same
// transaction, so the teacher can enter their own matching pool without
// any special-casing elsewhere.
func CreateRoom(ownerID uuid.UUID, name, description string, maxMembers int, expiresAt *time.Time) (*models.Room, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("room name is required")
	}
	if maxMembers <= 0 {
		maxMembers = models.DefaultRoomMaxMembers
	}

	// Owner must exist and not be banned - a room pointing at a dead
	// user would be invisible to everyone and un-manageable.
	var owner models.User
	if err := database.DB.First(&owner, "id = ?", ownerID).Error; err != nil {
		return nil, errors.New("owner user not found")
	}
	if owner.IsBanned {
		return nil, errors.New("owner is banned")
	}

	var descPtr *string
	if d := strings.TrimSpace(description); d != "" {
		descPtr = &d
	}

	room := models.Room{
		OwnerID:     ownerID,
		Name:        name,
		Description: descPtr,
		MaxMembers:  maxMembers,
		ExpiresAt:   expiresAt,
		IsActive:    true,
		MemberCount: 1, // the owner
	}

	// Retry the WHOLE transaction on the (astronomically unlikely) code
	// collision. The retry has to live out here, not inside the closure:
	// a failed INSERT aborts the Postgres transaction, so every statement
	// after it - including a second attempt - would fail with "current
	// transaction is aborted" rather than succeeding.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		room.ID = uuid.Nil
		room.Code = models.GenerateRoomCode()

		lastErr = database.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&room).Error; err != nil {
				return err
			}
			return tx.Create(&models.RoomMember{
				RoomID: room.ID,
				UserID: ownerID,
				Role:   models.RoomRoleOwner,
			}).Error
		})

		if lastErr == nil {
			break
		}
		if !isUniqueViolation(lastErr) {
			return nil, lastErr
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}

	room.Owner = &owner
	return &room, nil
}

// isUniqueViolation is a driver-agnostic check for "duplicate key". We
// match on the message rather than importing pq's error type because the
// project talks to Postgres through both pgx (gorm driver) and lib/pq.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate key") || strings.Contains(s, "unique constraint")
}

// UpdateRoom applies a partial update. Only the fields present in the map
// are touched; the caller (admin handler) is responsible for whitelisting.
func UpdateRoom(roomID uuid.UUID, updates map[string]interface{}) (*models.Room, error) {
	if len(updates) == 0 {
		return GetRoomByID(roomID)
	}
	res := database.DB.Model(&models.Room{}).Where("id = ?", roomID).Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	return GetRoomByID(roomID)
}

// RegenerateRoomCode rotates the invite code. Every previously shared
// link stops working immediately, but existing members keep their access -
// this is the "our link leaked into a public chat" remedy.
func RegenerateRoomCode(roomID uuid.UUID) (*models.Room, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		code := models.GenerateRoomCode()
		err := database.DB.Model(&models.Room{}).
			Where("id = ?", roomID).
			Update("code", code).Error
		if err == nil {
			return GetRoomByID(roomID)
		}
		lastErr = err
		if !isUniqueViolation(err) {
			break
		}
	}
	return nil, lastErr
}

// TransferRoomOwnership hands a room to a different teacher.
//
// Both membership rows are fixed up in the same transaction: the old
// owner is demoted to "student" (they usually stay in the class) and the
// new owner is inserted or promoted. Skipping this would leave a room
// whose owner isn't a member, which every membership check would then
// reject.
func TransferRoomOwnership(roomID uuid.UUID, newOwnerID uuid.UUID) error {
	var newOwner models.User
	if err := database.DB.First(&newOwner, "id = ?", newOwnerID).Error; err != nil {
		return errors.New("new owner user not found")
	}

	return database.DB.Transaction(func(tx *gorm.DB) error {
		var room models.Room
		if err := tx.First(&room, "id = ?", roomID).Error; err != nil {
			return ErrRoomNotFound
		}
		if room.OwnerID == newOwnerID {
			return nil
		}

		// Demote the previous owner.
		if err := tx.Model(&models.RoomMember{}).
			Where("room_id = ? AND user_id = ?", roomID, room.OwnerID).
			Update("role", models.RoomRoleStudent).Error; err != nil {
			return err
		}

		// Promote (or insert) the new one.
		res := tx.Model(&models.RoomMember{}).
			Where("room_id = ? AND user_id = ?", roomID, newOwnerID).
			Update("role", models.RoomRoleOwner)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if err := tx.Create(&models.RoomMember{
				RoomID: roomID,
				UserID: newOwnerID,
				Role:   models.RoomRoleOwner,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.Room{}).
				Where("id = ?", roomID).
				UpdateColumn("member_count", gorm.Expr("member_count + 1")).Error; err != nil {
				return err
			}
		}

		return tx.Model(&models.Room{}).
			Where("id = ?", roomID).
			Update("owner_id", newOwnerID).Error
	})
}

// DeleteRoom soft-deletes the room and drops its membership rows. Session
// history keeps its room_id so past reports stay intact.
func DeleteRoom(roomID uuid.UUID) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("room_id = ?", roomID).Delete(&models.RoomMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Room{}, "id = ?", roomID).Error
	})
}

// --- Lookups ---

func GetRoomByID(roomID uuid.UUID) (*models.Room, error) {
	var room models.Room
	err := database.DB.Preload("Owner").First(&room, "id = ?", roomID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoomNotFound
	}
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// GetRoomByCode resolves an invite code. Codes are stored uppercase and
// compared uppercase so a link pasted in lowercase still works.
func GetRoomByCode(code string) (*models.Room, error) {
	code = NormalizeRoomCode(code)
	if code == "" {
		return nil, ErrRoomNotFound
	}
	var room models.Room
	err := database.DB.Preload("Owner").First(&room, "code = ?", code).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoomNotFound
	}
	if err != nil {
		return nil, err
	}
	return &room, nil
}

// NormalizeRoomCode uppercases and strips the optional "room_" prefix so
// the same function handles a raw code, a pasted deep-link payload, and
// a code the user typed with stray whitespace.
func NormalizeRoomCode(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "ROOM_")
	s = strings.TrimPrefix(s, "ROOM-")
	// Drop anything that isn't part of the alphabet (spaces, dashes users
	// insert for readability).
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RoomInviteLink builds the Telegram deep link students follow to join.
// `startapp` opens the Mini App directly with start_param = room_<CODE>,
// which the auth handler turns into an automatic join.
func RoomInviteLink(code string) string {
	bot := strings.TrimPrefix(config.App.BotUsername, "@")
	return "https://t.me/" + bot + "?startapp=room_" + code
}

// --- Membership ---

// GetMembership returns the caller's row in the room, or ErrNotRoomMember.
func GetMembership(roomID uuid.UUID, userID uuid.UUID) (*models.RoomMember, error) {
	var m models.RoomMember
	err := database.DB.
		Where("room_id = ? AND user_id = ?", roomID, userID).
		First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotRoomMember
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// IsRoomMember is the cheap boolean form used on hot paths (the WS queue).
func IsRoomMember(roomID uuid.UUID, userID uuid.UUID) bool {
	var count int64
	database.DB.Model(&models.RoomMember{}).
		Where("room_id = ? AND user_id = ?", roomID, userID).
		Count(&count)
	return count > 0
}

// IsRoomOwner reports whether the user owns the room. Used to gate the
// teacher panel, the round trigger and the member-removal endpoint.
func IsRoomOwner(roomID uuid.UUID, userID uuid.UUID) bool {
	var count int64
	database.DB.Model(&models.Room{}).
		Where("id = ? AND owner_id = ?", roomID, userID).
		Count(&count)
	return count > 0
}

// JoinRoomByCode adds the user to the room behind `code`.
//
// Idempotent: re-joining an already-joined room is a no-op that returns
// the room, because the invite link is something students will tap over
// and over (it's how they open the app during class).
func JoinRoomByCode(userID uuid.UUID, code string) (*models.Room, error) {
	room, err := GetRoomByCode(code)
	if err != nil {
		return nil, err
	}
	if !room.IsOpen() {
		return nil, ErrRoomClosed
	}

	// Already a member → success, no counter change.
	if IsRoomMember(room.ID, userID) {
		return room, nil
	}

	// Capacity is checked inside the transaction against a locked row so
	// two students tapping the link at the same moment can't both slip
	// past a full room.
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.Room
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&locked, "id = ?", room.ID).Error; err != nil {
			return err
		}

		var current int64
		if err := tx.Model(&models.RoomMember{}).
			Where("room_id = ?", room.ID).
			Count(&current).Error; err != nil {
			return err
		}
		if int(current) >= locked.MaxMembers {
			return ErrRoomFull
		}

		if err := tx.Create(&models.RoomMember{
			RoomID: room.ID,
			UserID: userID,
			Role:   models.RoomRoleStudent,
		}).Error; err != nil {
			// A concurrent duplicate join is a success, not an error.
			if isUniqueViolation(err) {
				return nil
			}
			return err
		}

		return tx.Model(&models.Room{}).
			Where("id = ?", room.ID).
			UpdateColumn("member_count", gorm.Expr("member_count + 1")).Error
	})
	if err != nil {
		return nil, err
	}

	return GetRoomByID(room.ID)
}

// LeaveRoom removes a student from a room. The owner is refused - losing
// the owner would orphan the room; an admin deletes it instead.
func LeaveRoom(roomID uuid.UUID, userID uuid.UUID) error {
	if IsRoomOwner(roomID, userID) {
		return ErrOwnerCantLeave
	}
	return removeMemberRow(roomID, userID)
}

// RemoveRoomMember is the teacher-side kick. Authorization is the
// caller's job (handlers check IsRoomOwner or admin).
func RemoveRoomMember(roomID uuid.UUID, targetUserID uuid.UUID) error {
	if IsRoomOwner(roomID, targetUserID) {
		return ErrOwnerCantLeave
	}
	return removeMemberRow(roomID, targetUserID)
}

func removeMemberRow(roomID uuid.UUID, userID uuid.UUID) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("room_id = ? AND user_id = ?", roomID, userID).
			Delete(&models.RoomMember{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotRoomMember
		}
		// GREATEST guards against the counter drifting below zero if a
		// row was ever deleted out of band.
		return tx.Model(&models.Room{}).
			Where("id = ?", roomID).
			UpdateColumn("member_count", gorm.Expr("GREATEST(member_count - 1, 0)")).Error
	})
}

// RoomMemberView is one row of the teacher panel / member list. It carries
// the in-room stats plus enough profile data to render an avatar row,
// and a live `online` flag injected by the handler.
type RoomMemberView struct {
	UserID      uuid.UUID  `json:"user_id"`
	Name        string     `json:"name"`
	PhotoURL    *string    `json:"photo_url"`
	Level       *string    `json:"level"`
	Role        string     `json:"role"`
	JoinedAt    time.Time  `json:"joined_at"`
	Sessions    int        `json:"total_sessions"`
	Minutes     int        `json:"total_minutes"`
	LastSpokeAt *time.Time `json:"last_spoke_at"`

	// Live state, filled in by the WS/handler layer:
	//   online    - has at least one open socket
	//   searching - sitting in this room's match queue right now
	//   speaking  - inside an active session in this room
	Online    bool `json:"online"`
	Searching bool `json:"searching"`
	Speaking  bool `json:"speaking"`
}

// ListRoomMembers returns every member with their in-room stats, owner
// first and then the most active students.
func ListRoomMembers(roomID uuid.UUID) ([]RoomMemberView, error) {
	var members []models.RoomMember
	err := database.DB.
		Preload("User").
		Where("room_id = ?", roomID).
		Order("role = 'owner' DESC, total_minutes DESC, joined_at ASC").
		Find(&members).Error
	if err != nil {
		return nil, err
	}

	out := make([]RoomMemberView, 0, len(members))
	for _, m := range members {
		v := RoomMemberView{
			UserID:      m.UserID,
			Role:        m.Role,
			JoinedAt:    m.JoinedAt,
			Sessions:    m.TotalSessions,
			Minutes:     m.TotalMinutes,
			LastSpokeAt: m.LastSpokeAt,
		}
		if m.User != nil {
			v.Name = m.User.DisplayName()
			v.PhotoURL = m.User.PhotoURL
			v.Level = m.User.Level
		}
		out = append(out, v)
	}
	return out, nil
}

// RoomMemberIDs returns just the user IDs - used by the WS layer to fan
// out room state without loading full profiles.
func RoomMemberIDs(roomID uuid.UUID) []string {
	var ids []uuid.UUID
	database.DB.Model(&models.RoomMember{}).
		Where("room_id = ?", roomID).
		Pluck("user_id", &ids)

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

// ListMyRooms returns every room the user owns plus every room they've
// joined. The owned ones come first - that's the teacher's own space.
func ListMyRooms(userID uuid.UUID) ([]models.Room, []models.Room, error) {
	var owned []models.Room
	if err := database.DB.
		Where("owner_id = ?", userID).
		Order("created_at DESC").
		Find(&owned).Error; err != nil {
		return nil, nil, err
	}

	var joined []models.Room
	if err := database.DB.
		Joins("JOIN room_members rm ON rm.room_id = rooms.id AND rm.deleted_at IS NULL").
		Preload("Owner").
		Where("rm.user_id = ? AND rooms.owner_id != ?", userID, userID).
		Order("rooms.created_at DESC").
		Find(&joined).Error; err != nil {
		return nil, nil, err
	}

	return owned, joined, nil
}

// --- Stats bookkeeping ---

// RecordRoomSessionEnd folds a finished room session into the room's and
// both members' counters. Idempotency is the caller's responsibility -
// it is invoked from the single session-end path that already guards
// against double-ending (services.EndSession returns nil on a repeat).
func RecordRoomSessionEnd(roomID uuid.UUID, user1ID, user2ID uuid.UUID, minutes int) {
	if minutes < 0 {
		minutes = 0
	}
	now := time.Now()

	database.DB.Model(&models.Room{}).
		Where("id = ?", roomID).
		Updates(map[string]interface{}{
			"total_sessions": gorm.Expr("total_sessions + 1"),
			"total_minutes":  gorm.Expr("total_minutes + ?", minutes),
		})

	database.DB.Model(&models.RoomMember{}).
		Where("room_id = ? AND user_id IN ?", roomID, []uuid.UUID{user1ID, user2ID}).
		Updates(map[string]interface{}{
			"total_sessions": gorm.Expr("total_sessions + 1"),
			"total_minutes":  gorm.Expr("total_minutes + ?", minutes),
			"last_spoke_at":  now,
		})
}

// RoomSessionView is one line of the teacher's session history.
type RoomSessionView struct {
	SessionID uuid.UUID  `json:"session_id"`
	User1ID   uuid.UUID  `json:"user1_id"`
	User1Name string     `json:"user1_name"`
	User2ID   uuid.UUID  `json:"user2_id"`
	User2Name string     `json:"user2_name"`
	Topic     *string    `json:"topic"`
	Status    string     `json:"status"`
	Minutes   int        `json:"duration_minutes"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

// ListRoomSessions returns the room's session history, newest first.
func ListRoomSessions(roomID uuid.UUID, limit, offset int) ([]RoomSessionView, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var total int64
	database.DB.Model(&models.Session{}).Where("room_id = ?", roomID).Count(&total)

	var sessions []models.Session
	err := database.DB.
		Preload("User1").Preload("User2").
		Where("room_id = ?", roomID).
		Order("created_at DESC").
		Limit(limit).Offset(offset).
		Find(&sessions).Error
	if err != nil {
		return nil, 0, err
	}

	out := make([]RoomSessionView, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, RoomSessionView{
			SessionID: s.ID,
			User1ID:   s.User1ID,
			User1Name: s.User1.DisplayName(),
			User2ID:   s.User2ID,
			User2Name: s.User2.DisplayName(),
			Topic:     s.Topic,
			Status:    s.Status,
			Minutes:   s.DurationMinutes(),
			StartedAt: s.StartedAt,
			EndedAt:   s.EndedAt,
		})
	}
	return out, total, nil
}

// RoomReportRow is one student's activity inside a date range - the
// attendance report a learning centre actually acts on.
type RoomReportRow struct {
	UserID   uuid.UUID `json:"user_id"`
	Name     string    `json:"name"`
	PhotoURL *string   `json:"photo_url"`
	Role     string    `json:"role"`
	Sessions int       `json:"sessions"`
	Minutes  int       `json:"minutes"`
	// Days is the number of distinct Tashkent calendar days on which the
	// student spoke - the honest measure of "did they show up", since a
	// single marathon session shouldn't look like a week of practice.
	Days int `json:"days"`
}

// RoomReport aggregates every member's speaking inside [since, until).
//
// Members with zero activity are included with zeroes on purpose: the
// teacher's first question is "who did NOT practise this week", and a
// report that silently omits them cannot answer it.
func RoomReport(roomID uuid.UUID, since, until time.Time) ([]RoomReportRow, error) {
	type agg struct {
		UserID   uuid.UUID
		Sessions int
		Minutes  int
		Days     int
	}

	var rows []agg
	err := database.DB.Raw(`
		SELECT user_id,
		       COUNT(*)                                   AS sessions,
		       -- Divide-then-sum, then ::bigint. Matches how minutes are
		       -- credited elsewhere (EndSession truncates per session via
		       -- DurationMinutes), so this report and the per-member
		       -- counters never disagree. The cast is required because
		       -- SUM() over bigint returns NUMERIC.
		       COALESCE(SUM(duration_seconds / 60)::bigint, 0) AS minutes,
		       COUNT(DISTINCT DATE(created_at AT TIME ZONE 'Asia/Tashkent')) AS days
		FROM (
			SELECT user1_id AS user_id, duration_seconds, created_at
			FROM sessions
			WHERE room_id = ? AND status = 'ended'
			  AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
			UNION ALL
			SELECT user2_id AS user_id, duration_seconds, created_at
			FROM sessions
			WHERE room_id = ? AND status = 'ended'
			  AND created_at >= ? AND created_at < ? AND deleted_at IS NULL
		) combined
		GROUP BY user_id
	`, roomID, since, until, roomID, since, until).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	byUser := make(map[uuid.UUID]agg, len(rows))
	for _, r := range rows {
		byUser[r.UserID] = r
	}

	members, err := ListRoomMembers(roomID)
	if err != nil {
		return nil, err
	}

	out := make([]RoomReportRow, 0, len(members))
	for _, m := range members {
		a := byUser[m.UserID]
		out = append(out, RoomReportRow{
			UserID:   m.UserID,
			Name:     m.Name,
			PhotoURL: m.PhotoURL,
			Role:     m.Role,
			Sessions: a.Sessions,
			Minutes:  a.Minutes,
			Days:     a.Days,
		})
	}
	return out, nil
}

// SessionRoomID returns the room a session belongs to, or nil for a
// public-queue session. Used on the session-end path to decide whether
// the minutes are free (room) or billed against the daily limit.
func SessionRoomID(sessionID string) *uuid.UUID {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return nil
	}
	var s models.Session
	if err := database.DB.Select("room_id").First(&s, "id = ?", sid).Error; err != nil {
		return nil
	}
	return s.RoomID
}
