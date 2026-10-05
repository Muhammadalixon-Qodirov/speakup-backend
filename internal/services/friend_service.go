package services

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
)

var (
	ErrFriendSelf         = errors.New("o'zingizga so'rov yubora olmaysiz")
	ErrNoSharedSession    = errors.New("bu foydalanuvchi bilan suhbatlashmagansiz")
	ErrAlreadyFriends     = errors.New("siz allaqachon do'stsiz")
	ErrRequestPending     = errors.New("so'rov allaqachon yuborilgan")
	ErrRequestRejected    = errors.New("so'rovingiz rad etilgan")
	ErrFriendNotFound     = errors.New("so'rov topilmadi")
	ErrNotFriends         = errors.New("bu foydalanuvchi do'stlaringizda emas")
)

// declineCooldown is how long a rejected requester must wait before
// asking again. Someone who has been told "no" gets one more shot a week
// later, not an unlimited retry button.
const declineCooldown = 7 * 24 * time.Hour

// HaveSpokenTogether reports whether the two users have ever finished a
// session together.
//
// This is the gate on the entire friend system: there is no user search
// and no add-by-username, so the only way to reach someone's friend list
// is to have actually talked to them. It also means a request always
// arrives with context - the recipient just spoke to this person.
func HaveSpokenTogether(a, b uuid.UUID) bool {
	var count int64
	database.DB.Model(&models.Session{}).
		Where("((user1_id = ? AND user2_id = ?) OR (user1_id = ? AND user2_id = ?))",
			a, b, b, a).
		Count(&count)
	return count > 0
}

// GetFriendship loads the row for a pair, in either direction.
func GetFriendship(a, b uuid.UUID) (*models.Friendship, error) {
	var f models.Friendship
	err := database.DB.
		Where("pair_key = ?", models.FriendPairKey(a, b)).
		First(&f).Error
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// AreFriends is the check every scheduling call makes.
func AreFriends(a, b uuid.UUID) bool {
	f, err := GetFriendship(a, b)
	if err != nil {
		return false
	}
	return f.Status == models.FriendAccepted
}

// SendFriendRequest creates (or revives) a request from -> to.
//
// Revival rules exist so a "no" means something: the person who declined
// may change their mind at any time, while the person who was declined
// waits a week before asking again.
func SendFriendRequest(from, to uuid.UUID) (*models.Friendship, error) {
	if from == to {
		return nil, ErrFriendSelf
	}
	if !HaveSpokenTogether(from, to) {
		return nil, ErrNoSharedSession
	}

	existing, err := GetFriendship(from, to)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	if existing != nil {
		switch existing.Status {
		case models.FriendAccepted:
			return nil, ErrAlreadyFriends

		case models.FriendPending:
			// The other side already asked us - answering with a request
			// of our own obviously means yes, so short-circuit the
			// handshake instead of leaving two people waiting on each
			// other.
			if existing.RequesterID == to {
				return AcceptFriendRequest(existing.ID, from)
			}
			return nil, ErrRequestPending

		case models.FriendDeclined:
			decliner := existing.AddresseeID
			if from != decliner {
				// We are the one who was turned down.
				if existing.RespondedAt != nil &&
					time.Since(*existing.RespondedAt) < declineCooldown {
					return nil, ErrRequestRejected
				}
			}
			now := time.Now()
			_ = now
			updates := map[string]interface{}{
				"requester_id": from,
				"addressee_id": to,
				"status":       models.FriendPending,
				"responded_at": nil,
			}
			if err := database.DB.Model(existing).Updates(updates).Error; err != nil {
				return nil, err
			}
			return GetFriendship(from, to)
		}
	}

	f := models.Friendship{
		RequesterID: from,
		AddresseeID: to,
		Status:      models.FriendPending,
		PairKey:     models.FriendPairKey(from, to),
	}
	if err := database.DB.Create(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

// AcceptFriendRequest accepts a pending request. Only the addressee may
// accept - accepting your own request would be a neat way to add anyone
// you ever spoke to without their say-so.
func AcceptFriendRequest(requestID, userID uuid.UUID) (*models.Friendship, error) {
	now := time.Now()
	res := database.DB.Model(&models.Friendship{}).
		Where("id = ? AND addressee_id = ? AND status = ?",
			requestID, userID, models.FriendPending).
		Updates(map[string]interface{}{
			"status":       models.FriendAccepted,
			"responded_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrFriendNotFound
	}

	var f models.Friendship
	if err := database.DB.
		Preload("Requester").Preload("Addressee").
		First(&f, "id = ?", requestID).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

// DeclineFriendRequest turns a pending request down.
func DeclineFriendRequest(requestID, userID uuid.UUID) error {
	res := database.DB.Model(&models.Friendship{}).
		Where("id = ? AND addressee_id = ? AND status = ?",
			requestID, userID, models.FriendPending).
		Updates(map[string]interface{}{
			"status":       models.FriendDeclined,
			"responded_at": time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrFriendNotFound
	}
	return nil
}

// Unfriend removes an accepted friendship from either side.
//
// The row is deleted rather than marked declined: unfriending is not a
// rejection of a request, and leaving a "declined" tombstone would put
// the other person on a 7-day cooldown for something they never did.
func Unfriend(userID, otherID uuid.UUID) error {
	res := database.DB.
		Where("pair_key = ? AND status = ?",
			models.FriendPairKey(userID, otherID), models.FriendAccepted).
		Delete(&models.Friendship{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFriends
	}

	// Any appointment between them loses its basis, so drop the ones
	// that haven't happened yet instead of leaving a stranger on
	// someone's calendar.
	database.DB.Model(&models.ScheduledSession{}).
		Where("status IN ? AND scheduled_at > ? AND ((user1_id = ? AND user2_id = ?) OR (user1_id = ? AND user2_id = ?))",
			[]string{models.ScheduledPending, models.ScheduledConfirmed}, time.Now(),
			userID, otherID, otherID, userID).
		Updates(map[string]interface{}{
			"status":          models.ScheduledCancelled,
			"cancelled_by_id": userID,
		})

	return nil
}

// ListFriends returns every accepted friendship for a user, newest
// first, with the other person's profile preloaded.
func ListFriends(userID uuid.UUID) ([]models.Friendship, error) {
	var out []models.Friendship
	err := database.DB.
		Preload("Requester").Preload("Addressee").
		Where("status = ? AND (requester_id = ? OR addressee_id = ?)",
			models.FriendAccepted, userID, userID).
		Order("responded_at DESC NULLS LAST, created_at DESC").
		Find(&out).Error
	return out, err
}

// ListFriendRequests returns the pending requests waiting on this user
// (incoming) and the ones they are waiting on (outgoing).
func ListFriendRequests(userID uuid.UUID) (incoming, outgoing []models.Friendship, err error) {
	err = database.DB.
		Preload("Requester").
		Where("status = ? AND addressee_id = ?", models.FriendPending, userID).
		Order("created_at DESC").
		Find(&incoming).Error
	if err != nil {
		return nil, nil, err
	}

	err = database.DB.
		Preload("Addressee").
		Where("status = ? AND requester_id = ?", models.FriendPending, userID).
		Order("created_at DESC").
		Find(&outgoing).Error
	return incoming, outgoing, err
}

// PendingRequestCount backs the badge on the profile menu.
func PendingRequestCount(userID uuid.UUID) int64 {
	var n int64
	database.DB.Model(&models.Friendship{}).
		Where("status = ? AND addressee_id = ?", models.FriendPending, userID).
		Count(&n)
	return n
}

// FriendshipStateWith describes how the viewer relates to another user.
// The rate screen uses it to decide which button to show.
type FriendshipStateWith struct {
	Status string `json:"status"` // none | pending_incoming | pending_outgoing | accepted | declined
	// RequestID is set for the pending states so the client can act on it.
	RequestID *uuid.UUID `json:"request_id,omitempty"`
	// CanRequest is false when a request would be refused anyway - the
	// button is hidden rather than shown and then rejected.
	CanRequest bool `json:"can_request"`
}

// FriendshipWith computes the viewer's relationship to another user.
func FriendshipWith(viewerID, otherID uuid.UUID) FriendshipStateWith {
	if viewerID == otherID {
		return FriendshipStateWith{Status: "none", CanRequest: false}
	}

	f, err := GetFriendship(viewerID, otherID)
	if err != nil {
		return FriendshipStateWith{
			Status:     "none",
			CanRequest: HaveSpokenTogether(viewerID, otherID),
		}
	}

	switch f.Status {
	case models.FriendAccepted:
		return FriendshipStateWith{Status: "accepted"}
	case models.FriendPending:
		if f.RequesterID == viewerID {
			return FriendshipStateWith{Status: "pending_outgoing", RequestID: &f.ID}
		}
		return FriendshipStateWith{Status: "pending_incoming", RequestID: &f.ID}
	default:
		// Declined: only offer the button again to the person who said
		// no, or to the rejected side once the cooldown has run out.
		canRetry := f.AddresseeID == viewerID ||
			(f.RespondedAt != nil && time.Since(*f.RespondedAt) >= declineCooldown)
		return FriendshipStateWith{Status: "declined", CanRequest: canRetry}
	}
}
