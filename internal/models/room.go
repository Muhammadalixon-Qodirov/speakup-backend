package models

import (
	"crypto/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// roomCodeAlphabet deliberately drops the visually ambiguous characters
// (0/O, 1/I/L) so a teacher can read a code out loud in class without
// anyone mistyping it. 28 symbols ^ 8 chars ≈ 3.8e11 combinations, which
// is far beyond guessable at any rate limit we allow.
const roomCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// roomCodeLength is 8 - short enough to type by hand, long enough that
// scanning the space is hopeless.
const roomCodeLength = 8

// GenerateRoomCode returns a random, unguessable, human-readable room code.
// Uniqueness is enforced by the DB index; the service layer retries on
// collision (astronomically unlikely, but cheap to handle).
func GenerateRoomCode() string {
	b := make([]byte, roomCodeLength)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is close to impossible; fall back to a UUID
		// slice so we never hand out an empty code.
		return strings.ToUpper(strings.ReplaceAll(uuid.New().String(), "-", "")[:roomCodeLength])
	}
	out := make([]byte, roomCodeLength)
	for i, x := range b {
		out[i] = roomCodeAlphabet[int(x)%len(roomCodeAlphabet)]
	}
	return string(out)
}

// Room is a private speaking space owned by one user (a teacher or a
// learning centre). Students join through the owner's invite link and,
// once inside, can press "Speak" and get matched ONLY against other
// members of the same room - never against the public queue.
//
// This is the core of the learning-centre offering: the public queue is
// open to everyone and its wait time depends on who happens to be online,
// while a room's queue is fully controlled by the teacher's own class.
type Room struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// OwnerID is the teacher. Rooms are created by platform admins only
	// (POST /admin/rooms) and the owner is assigned at creation time - a
	// user can never mint a room for themselves.
	OwnerID uuid.UUID `gorm:"type:uuid;not null;index" json:"owner_id"`
	Owner   *User     `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`

	// CenterID ties this room to a partner learning centre. Nullable, and
	// today most rooms are one-teacher rooms that either have their own
	// centre or none at all. It exists from day one so a centre that
	// grows to several teachers just gets several rooms pointing here -
	// no data migration, no reshuffling of membership.
	CenterID *uuid.UUID   `gorm:"type:uuid;index" json:"center_id"`
	Center   *StudyCenter `gorm:"foreignKey:CenterID" json:"center,omitempty"`

	// Name is what students see on the join screen ("IELTS Zone B2 guruh").
	Name string `gorm:"size:128;not null" json:"name"`

	// Code is the join secret. It lives in the invite link
	// (t.me/<bot>?startapp=room_<CODE>) and can be rotated by an admin
	// if it leaks, which instantly invalidates every old link without
	// removing anyone who already joined.
	Code string `gorm:"size:16;uniqueIndex;not null" json:"code"`

	Description *string `gorm:"size:512" json:"description"`

	// IsActive false = the room is frozen: nobody can join and nobody
	// can enter the room queue. Existing members and history are kept.
	IsActive bool `gorm:"default:true;index" json:"is_active"`

	// MaxMembers caps how many students can join through the link. 0 is
	// treated as DefaultRoomMaxMembers rather than "unlimited" so a
	// misconfigured room can't be flooded.
	MaxMembers int `gorm:"default:100" json:"max_members"`

	// ExpiresAt lets an admin sell a room for a fixed term. Nil = never
	// expires. An expired room behaves exactly like IsActive = false.
	ExpiresAt *time.Time `json:"expires_at"`

	// Denormalised counters. Kept in sync by the service layer with
	// atomic SQL so the teacher panel and the admin list can render
	// without aggregating room_members / sessions on every request.
	MemberCount   int `gorm:"default:0" json:"member_count"`
	TotalSessions int `gorm:"default:0" json:"total_sessions"`
	TotalMinutes  int `gorm:"default:0" json:"total_minutes"`
}

// DefaultRoomMaxMembers is the cap applied when a room is created without
// an explicit limit (or with a nonsensical one).
const DefaultRoomMaxMembers = 100

func (r *Room) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	if r.Code == "" {
		r.Code = GenerateRoomCode()
	}
	if r.MaxMembers <= 0 {
		r.MaxMembers = DefaultRoomMaxMembers
	}
	return nil
}

// IsOpen reports whether the room currently accepts joins and matching.
// Both the "frozen" flag and the optional expiry are folded in here so
// callers only ever need this one check.
func (r *Room) IsOpen() bool {
	if r == nil || !r.IsActive {
		return false
	}
	if r.ExpiresAt != nil && r.ExpiresAt.Before(time.Now()) {
		return false
	}
	return true
}

// RoomMemberRole values.
const (
	RoomRoleOwner   = "owner"
	RoomRoleStudent = "student"
)

// RoomMember binds a user to a room. The owner gets a row too (role
// "owner") so the teacher can join the matching pool themselves and be
// counted in the live panel like anyone else.
type RoomMember struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// The composite unique index is what makes "join" idempotent: a
	// student clicking the invite link ten times still produces one row.
	RoomID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_room_member_unique;index" json:"room_id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_room_member_unique;index" json:"user_id"`

	Room *Room `gorm:"foreignKey:RoomID" json:"-"`
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`

	Role string `gorm:"size:16;default:'student'" json:"role"`

	// Per-member activity inside THIS room. This is the attendance
	// report a learning centre actually cares about - "who spoke how
	// much this week" - and it must not be conflated with the user's
	// global totals, which include public-queue sessions.
	TotalSessions int        `gorm:"default:0" json:"total_sessions"`
	TotalMinutes  int        `gorm:"default:0" json:"total_minutes"`
	LastSpokeAt   *time.Time `json:"last_spoke_at"`

	JoinedAt time.Time `json:"joined_at"`
}

func (m *RoomMember) BeforeCreate(tx *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	if m.JoinedAt.IsZero() {
		m.JoinedAt = time.Now()
	}
	if m.Role == "" {
		m.Role = RoomRoleStudent
	}
	return nil
}
