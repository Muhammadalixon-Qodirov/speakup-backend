package handlers

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/utils"
)

// ─── User-facing ─────────────────────────────────────────────────────

type CreateFeedbackRequest struct {
	Category string `json:"category"` // "question" | "suggestion" | "complaint"
	Message  string `json:"message"`
}

var validCategories = map[string]bool{
	"question":   true,
	"suggestion": true,
	"complaint":  true,
}

// CreateFeedback handles POST /feedback - user opens a new ticket.
func CreateFeedback(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	var req CreateFeedbackRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid body")
	}

	cat := strings.TrimSpace(req.Category)
	msg := strings.TrimSpace(req.Message)
	if !validCategories[cat] {
		return utils.BadRequest(c, "Kategoriya: question | suggestion | complaint")
	}
	if len(msg) < 5 {
		return utils.BadRequest(c, "Xabar juda qisqa (kamida 5 ta belgi)")
	}
	if len(msg) > 4000 {
		return utils.BadRequest(c, "Xabar juda uzun (maks 4000 ta belgi)")
	}

	ticket := models.FeedbackTicket{
		UserID:   user.ID,
		Category: cat,
		Message:  msg,
		Status:   "sent",
	}
	if err := database.DB.Create(&ticket).Error; err != nil {
		return utils.InternalError(c)
	}

	return utils.Created(c, ticket)
}

// ListMyFeedbackTickets handles GET /users/me/feedback-tickets - returns
// the user's submitted tickets with admin replies. Side-effect: marks
// all previously unseen admin replies as seen so the profile-menu badge
// clears the moment the user opens the support page. Order matters: we
// mark BEFORE fetching, so a retry of the GET (proxy timeout, etc.)
// converges on the same state instead of bouncing the badge.
func ListMyFeedbackTickets(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	database.DB.Model(&models.FeedbackTicket{}).
		Where("user_id = ? AND admin_reply IS NOT NULL AND user_seen_reply = ?", user.ID, false).
		Update("user_seen_reply", true)

	var tickets []models.FeedbackTicket
	err := database.DB.
		Where("user_id = ?", user.ID).
		Order("created_at DESC").
		Limit(100).
		Find(&tickets).Error
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, tickets)
}

// UnreadFeedbackRepliesCount handles GET /users/me/feedback-tickets/unread-count
// Returns the number of admin-replied tickets the user hasn't seen yet -
// drives the red dot on the profile menu.
func UnreadFeedbackRepliesCount(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	var count int64
	database.DB.Model(&models.FeedbackTicket{}).
		Where("user_id = ? AND admin_reply IS NOT NULL AND user_seen_reply = ?", user.ID, false).
		Count(&count)

	return utils.Success(c, fiber.Map{"count": count})
}

// ─── Admin-facing ────────────────────────────────────────────────────

type AdminListFeedbackResponse struct {
	Tickets    []models.FeedbackTicket `json:"tickets"`
	Pagination AdminFeedbackPagination `json:"pagination"`
}

type AdminFeedbackPagination struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"total_pages"`
}

// AdminListFeedback handles GET /admin/feedback?status=&category=&page=&limit=
func AdminListFeedback(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	status := c.Query("status")
	category := c.Query("category")

	query := database.DB.Model(&models.FeedbackTicket{}).Preload("User")
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if category != "" {
		query = query.Where("category = ?", category)
	}

	var total int64
	query.Count(&total)

	var tickets []models.FeedbackTicket
	query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&tickets)

	totalPages := (total + int64(limit) - 1) / int64(limit)

	return utils.Success(c, AdminListFeedbackResponse{
		Tickets: tickets,
		Pagination: AdminFeedbackPagination{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: totalPages,
		},
	})
}

type AdminUpdateFeedbackRequest struct {
	Status     *string `json:"status"`
	AdminReply *string `json:"admin_reply"`
}

var validStatuses = map[string]bool{
	"sent":      true,
	"viewed":    true,
	"resolved":  true,
	"cancelled": true,
}

// AdminUpdateFeedback handles PUT /admin/feedback/:id - admin sets status
// and/or replies.
func AdminUpdateFeedback(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.BadRequest(c, "Invalid id")
	}

	var req AdminUpdateFeedbackRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid body")
	}

	updates := map[string]interface{}{}
	newReply := ""
	replyChanged := false
	if req.Status != nil {
		s := strings.TrimSpace(*req.Status)
		if !validStatuses[s] {
			return utils.BadRequest(c, "Status: sent | viewed | resolved | cancelled")
		}
		updates["status"] = s
	}
	if req.AdminReply != nil {
		newReply = strings.TrimSpace(*req.AdminReply)
		updates["admin_reply"] = newReply
		now := time.Now()
		updates["replied_at"] = now
		// Flip the seen flag back to false so the user gets the badge
		// again (also covers edits to an already-replied ticket).
		updates["user_seen_reply"] = false
		replyChanged = true
	}

	if len(updates) == 0 {
		return utils.BadRequest(c, "Nothing to update")
	}

	res := database.DB.Model(&models.FeedbackTicket{}).
		Where("id = ?", id).
		Updates(updates)
	if res.Error != nil {
		return utils.InternalError(c)
	}
	if res.RowsAffected == 0 {
		return utils.NotFound(c, "Ticket not found")
	}

	var ticket models.FeedbackTicket
	database.DB.Preload("User").First(&ticket, "id = ?", id)

	// Notify the user via Telegram that their ticket got a reply.
	// Fire-and-forget - a transient bot error shouldn't break the admin
	// request path. Wrapped in safego so a panic in the bot library
	// can't take down the API.
	if replyChanged && newReply != "" && ticket.User.TelegramID != 0 {
		tgID := ticket.User.TelegramID
		reply := newReply
		safego.Go("notifyAdminReply", func() {
			bot.SendAdminReplyNotification(tgID, reply)
		})
	}

	return utils.Success(c, ticket)
}
