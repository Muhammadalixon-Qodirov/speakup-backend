package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Friendship is a mutual connection between two users.
//
// It exists for one reason: scheduling. A user can only book a future
// speaking session with someone who has agreed to be their partner, so
// the request/accept handshake is what keeps a calendar invite from
// becoming a way to pester strangers.
//
// A request may only be sent to somebody you have ALREADY spoken to -
// enforced in the service layer. There is no user search, no "add by
// username": the only door into someone's friend list is having had a
// real conversation with them.
type Friendship struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	RequesterID uuid.UUID `gorm:"type:uuid;not null;index" json:"requester_id"`
	AddresseeID uuid.UUID `gorm:"type:uuid;not null;index" json:"addressee_id"`

	Requester *User `gorm:"foreignKey:RequesterID" json:"requester,omitempty"`
	Addressee *User `gorm:"foreignKey:AddresseeID" json:"addressee,omitempty"`

	Status string `gorm:"size:16;default:'pending';index" json:"status"`

	// PairKey is the two user IDs sorted and joined, so the unique index
	// catches a duplicate regardless of who asked whom. Without it,
	// A→B and B→A would happily coexist as two separate "friendships"
	// and every list query would show the same person twice.
	PairKey string `gorm:"size:80;uniqueIndex;not null" json:"-"`

	RespondedAt *time.Time `json:"responded_at"`
}

// Friendship statuses.
const (
	FriendPending  = "pending"
	FriendAccepted = "accepted"
	FriendDeclined = "declined"
)

// FriendPairKey builds the canonical key for a pair of users. Sorting
// the two IDs is what makes it direction-independent.
func FriendPairKey(a, b uuid.UUID) string {
	x, y := a.String(), b.String()
	if x > y {
		x, y = y, x
	}
	return x + ":" + y
}

func (f *Friendship) BeforeCreate(tx *gorm.DB) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	if f.PairKey == "" {
		f.PairKey = FriendPairKey(f.RequesterID, f.AddresseeID)
	}
	return nil
}

// Other returns the ID of whichever side of the friendship is not the
// given user. Callers render a friend list from the viewer's side, and
// doing this inline everywhere is where off-by-one identity bugs live.
func (f *Friendship) Other(viewerID uuid.UUID) uuid.UUID {
	if f.RequesterID == viewerID {
		return f.AddresseeID
	}
	return f.RequesterID
}

// OtherUser is Other() but returning the preloaded profile.
func (f *Friendship) OtherUser(viewerID uuid.UUID) *User {
	if f.RequesterID == viewerID {
		return f.Addressee
	}
	return f.Requester
}
