package handlers

import (
	"errors"

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

// Friends.
//
// The only entry point is "I just spoke to this person" - there is no
// search and no add-by-username, so a request always arrives with the
// context of a real conversation behind it.

// friendUserJSON is the slim profile shape every friend endpoint returns.
// Deliberately not the full user record: a friend list has no business
// carrying phone numbers, premium state or referral codes.
type friendUserJSON struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	PhotoURL *string   `json:"photo_url"`
	Level    *string   `json:"level"`
	Region   *string   `json:"region"`
	Band     *float64  `json:"pinned_speaking_band"`
}

func toFriendUser(u *models.User) *friendUserJSON {
	if u == nil {
		return nil
	}
	return &friendUserJSON{
		ID:       u.ID,
		Name:     u.DisplayName(),
		PhotoURL: u.PhotoURL,
		Level:    u.Level,
		Region:   u.Region,
		Band:     u.PinnedSpeakingBand,
	}
}

type friendshipJSON struct {
	ID        uuid.UUID       `json:"id"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
	User      *friendUserJSON `json:"user"`
}

func toFriendshipJSON(f *models.Friendship, viewerID uuid.UUID) friendshipJSON {
	return friendshipJSON{
		ID:        f.ID,
		Status:    f.Status,
		CreatedAt: f.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		User:      toFriendUser(f.OtherUser(viewerID)),
	}
}

func friendErrorStatus(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, services.ErrNoSharedSession):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "NO_SHARED_SESSION", err.Error())
	case errors.Is(err, services.ErrAlreadyFriends):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "ALREADY_FRIENDS", err.Error())
	case errors.Is(err, services.ErrRequestPending):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "REQUEST_PENDING", err.Error())
	case errors.Is(err, services.ErrRequestRejected):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "REQUEST_REJECTED", err.Error())
	case errors.Is(err, services.ErrFriendNotFound):
		return utils.ErrorWithCode(c, fiber.StatusNotFound, "REQUEST_NOT_FOUND", err.Error())
	case errors.Is(err, services.ErrNotFriends):
		return utils.ErrorWithCode(c, fiber.StatusNotFound, "NOT_FRIENDS", err.Error())
	case errors.Is(err, services.ErrFriendSelf):
		return utils.BadRequest(c, err.Error())
	default:
		return utils.InternalError(c)
	}
}

// ListFriends handles GET /friends
func ListFriends(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	friends, err := services.ListFriends(user.ID)
	if err != nil {
		return utils.InternalError(c)
	}

	out := make([]friendshipJSON, 0, len(friends))
	for i := range friends {
		out = append(out, toFriendshipJSON(&friends[i], user.ID))
	}
	return utils.Success(c, out)
}

// ListFriendRequests handles GET /friends/requests
func ListFriendRequests(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	incoming, outgoing, err := services.ListFriendRequests(user.ID)
	if err != nil {
		return utils.InternalError(c)
	}

	in := make([]friendshipJSON, 0, len(incoming))
	for i := range incoming {
		in = append(in, toFriendshipJSON(&incoming[i], user.ID))
	}
	out := make([]friendshipJSON, 0, len(outgoing))
	for i := range outgoing {
		out = append(out, toFriendshipJSON(&outgoing[i], user.ID))
	}

	return utils.Success(c, fiber.Map{
		"incoming": in,
		"outgoing": out,
	})
}

// GetFriendshipWith handles GET /friends/with/:user_id
//
// Backs the button on the rating screen: it must show "add friend",
// "request sent", "accept" or nothing at all, and guessing client-side
// from a list would be wrong for exactly the cases that matter.
func GetFriendshipWith(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	otherID, err := uuid.Parse(c.Params("user_id"))
	if err != nil {
		return utils.BadRequest(c, "Foydalanuvchi ID noto'g'ri")
	}

	return utils.Success(c, services.FriendshipWith(user.ID, otherID))
}

type friendRequestBody struct {
	UserID string `json:"user_id"`
}

// SendFriendRequest handles POST /friends/requests
func SendFriendRequest(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req friendRequestBody
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}
	targetID, err := uuid.Parse(req.UserID)
	if err != nil {
		return utils.BadRequest(c, "Foydalanuvchi ID noto'g'ri")
	}

	f, serr := services.SendFriendRequest(user.ID, targetID)
	if serr != nil {
		return friendErrorStatus(c, serr)
	}

	// A mutual request resolves straight to "accepted" - tell both sides
	// the truth rather than a pending state that never existed.
	if f.Status == models.FriendAccepted {
		notifyFriendAccepted(f, user.ID)
	} else {
		notifyFriendRequest(user, targetID)
	}

	ws.SendToUser(targetID.String(), "friend_update", fiber.Map{"reason": "request"})

	return utils.Success(c, toFriendshipJSON(f, user.ID))
}

// AcceptFriendRequest handles POST /friends/requests/:id/accept
func AcceptFriendRequest(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	reqID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "So'rov ID noto'g'ri")
	}

	f, serr := services.AcceptFriendRequest(reqID, user.ID)
	if serr != nil {
		return friendErrorStatus(c, serr)
	}

	notifyFriendAccepted(f, user.ID)
	ws.SendToUser(f.Other(user.ID).String(), "friend_update", fiber.Map{"reason": "accepted"})

	return utils.Success(c, toFriendshipJSON(f, user.ID))
}

// DeclineFriendRequest handles POST /friends/requests/:id/decline
func DeclineFriendRequest(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	reqID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "So'rov ID noto'g'ri")
	}

	// Declining is intentionally silent - the person who was turned down
	// learns it from their own list, not from a notification.
	if serr := services.DeclineFriendRequest(reqID, user.ID); serr != nil {
		return friendErrorStatus(c, serr)
	}
	return utils.SuccessMessage(c, "So'rov rad etildi")
}

// Unfriend handles DELETE /friends/:user_id
func Unfriend(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	otherID, err := uuid.Parse(c.Params("user_id"))
	if err != nil {
		return utils.BadRequest(c, "Foydalanuvchi ID noto'g'ri")
	}

	if serr := services.Unfriend(user.ID, otherID); serr != nil {
		return friendErrorStatus(c, serr)
	}

	ws.SendToUser(otherID.String(), "friend_update", fiber.Map{"reason": "removed"})
	return utils.SuccessMessage(c, "Do'stlar ro'yxatidan olib tashlandi")
}

// --- Telegram side ---

func notifyFriendRequest(from *models.User, toID uuid.UUID) {
	target, err := services.GetUserByID(toID)
	if err != nil || target == nil || target.TelegramID == 0 {
		return
	}
	name := from.DisplayName()
	tgID := target.TelegramID
	safego.Go("notifyFriendRequest", func() {
		bot.SendFriendRequest(tgID, name)
	})
}

func notifyFriendAccepted(f *models.Friendship, acceptorID uuid.UUID) {
	// The person who asked is the one waiting for an answer.
	waiterID := f.RequesterID
	if acceptorID == f.RequesterID {
		waiterID = f.AddresseeID
	}

	waiter, err := services.GetUserByID(waiterID)
	if err != nil || waiter == nil || waiter.TelegramID == 0 {
		return
	}
	acceptor, err := services.GetUserByID(acceptorID)
	if err != nil || acceptor == nil {
		return
	}

	tgID := waiter.TelegramID
	name := acceptor.DisplayName()
	safego.Go("notifyFriendAccepted", func() {
		bot.SendFriendAccepted(tgID, name)
	})
}
