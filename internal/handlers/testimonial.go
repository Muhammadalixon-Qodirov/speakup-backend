package handlers

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

const (
	testimonialMinLen = 8
	testimonialMaxLen = 400
)

// PublicTestimonial is one approved review shown in the rotating carousel.
// Only safe, public fields - never the user id or anything sensitive.
type PublicTestimonial struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	PhotoURL           *string   `json:"photo_url"`
	Level              *string   `json:"level"`
	IsPremium          bool      `json:"is_premium"`
	PinnedSpeakingBand *float64  `json:"pinned_speaking_band"`
	Text               string    `json:"text"`
	Rating             int       `json:"rating"`
	CreatedAt          time.Time `json:"created_at"`
}

// MyTestimonial is the caller's own review + its moderation status.
type MyTestimonial struct {
	Text   string `json:"text"`
	Rating int    `json:"rating"`
	Status string `json:"status"`
}

type submitTestimonialReq struct {
	Text   string `json:"text"`
	Rating int    `json:"rating"`
}

// SubmitTestimonial handles POST /testimonials. One review per user: a
// resubmission overwrites the previous one and resets it to "pending" so an
// edited review is re-moderated. The review is invisible until approved.
func SubmitTestimonial(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	var req submitTestimonialReq
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid body")
	}
	text := strings.TrimSpace(req.Text)
	n := utf8.RuneCountInString(text)
	if n < testimonialMinLen || n > testimonialMaxLen {
		return utils.BadRequest(c, "Review length out of range")
	}
	rating := req.Rating
	if rating < 1 || rating > 5 {
		rating = 5
	}

	tm := models.Testimonial{
		UserID:     user.ID,
		Text:       text,
		Rating:     rating,
		Status:     "pending",
		ReviewedAt: nil,
	}
	// Upsert on the unique user_id: insert, or overwrite an existing review
	// (back to pending) so the user always has exactly one, freshly moderated.
	if err := database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"text":        text,
			"rating":      rating,
			"status":      "pending",
			"reviewed_at": nil,
			"updated_at":  time.Now(),
			"deleted_at":  nil,
		}),
	}).Create(&tm).Error; err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, MyTestimonial{Text: text, Rating: rating, Status: "pending"})
}

// GetMyTestimonial handles GET /users/me/testimonial - the caller's own
// review + status, or null if they haven't written one.
func GetMyTestimonial(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}
	var tm models.Testimonial
	err := database.DB.Where("user_id = ?", user.ID).First(&tm).Error
	if err == gorm.ErrRecordNotFound {
		return utils.Success(c, nil)
	}
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, MyTestimonial{Text: tm.Text, Rating: tm.Rating, Status: tm.Status})
}

// GetPublicTestimonials handles GET /testimonials - approved reviews for the
// rotating carousel, newest first.
func GetPublicTestimonials(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 30)
	if limit > 50 {
		limit = 50
	}
	if limit < 1 {
		limit = 30
	}

	var rows []models.Testimonial
	if err := database.DB.
		Preload("User").
		Where("status = ?", "approved").
		Order("reviewed_at DESC NULLS LAST, created_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return utils.Error(c, fiber.StatusInternalServerError, "Failed to load reviews")
	}

	out := make([]PublicTestimonial, 0, len(rows))
	for _, t := range rows {
		if t.User.ID == uuid.Nil || t.User.IsBanned {
			continue
		}
		out = append(out, PublicTestimonial{
			ID:                 t.ID.String(),
			Name:               t.User.DisplayName(),
			PhotoURL:           t.User.PhotoURL,
			Level:              t.User.Level,
			IsPremium:          t.User.IsPremium,
			PinnedSpeakingBand: t.User.PinnedSpeakingBand,
			Text:               t.Text,
			Rating:             t.Rating,
			CreatedAt:          t.CreatedAt,
		})
	}
	return utils.Success(c, out)
}

// --- Admin moderation ---

// AdminTestimonial is one row in the admin moderation list.
type AdminTestimonial struct {
	ID        string    `json:"id"`
	UserName  string    `json:"user_name"`
	Text      string    `json:"text"`
	Rating    int       `json:"rating"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// AdminListTestimonials handles GET /admin/testimonials?status=pending.
func AdminListTestimonials(c *fiber.Ctx) error {
	status := c.Query("status", "pending")
	limit := c.QueryInt("limit", 50)
	if limit > 200 {
		limit = 200
	}

	q := database.DB.Preload("User").Order("created_at DESC").Limit(limit)
	if status != "all" {
		q = q.Where("status = ?", status)
	}
	var rows []models.Testimonial
	if err := q.Find(&rows).Error; err != nil {
		return utils.InternalError(c)
	}

	out := make([]AdminTestimonial, 0, len(rows))
	for _, t := range rows {
		name := "—"
		if t.User.ID != uuid.Nil {
			name = t.User.DisplayName()
		}
		out = append(out, AdminTestimonial{
			ID:        t.ID.String(),
			UserName:  name,
			Text:      t.Text,
			Rating:    t.Rating,
			Status:    t.Status,
			CreatedAt: t.CreatedAt,
		})
	}
	return utils.Success(c, out)
}

type moderateReq struct {
	Status string `json:"status"` // "approved" | "rejected"
}

// AdminModerateTestimonial handles PUT /admin/testimonials/:id.
func AdminModerateTestimonial(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Invalid id")
	}
	var req moderateReq
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid body")
	}
	if req.Status != "approved" && req.Status != "rejected" && req.Status != "pending" {
		return utils.BadRequest(c, "Invalid status")
	}

	now := time.Now()
	if err := database.DB.Model(&models.Testimonial{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{"status": req.Status, "reviewed_at": now}).Error; err != nil {
		return utils.InternalError(c)
	}
	return utils.SuccessMessage(c, "Updated")
}
