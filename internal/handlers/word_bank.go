package handlers

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

type AddWordRequest struct {
	SessionID   *string `json:"session_id"`
	Word        string  `json:"word" validate:"required"`
	Translation *string `json:"translation"`
	Context     *string `json:"context"`
}

// GetWords handles GET /wordbank?session_id=xxx
func GetWords(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	query := database.DB.
		Where("user_id = ?", user.ID).
		Order("created_at DESC")

	if sid := c.Query("session_id"); sid != "" {
		query = query.Where("session_id = ?", sid)
	}

	var words []models.WordBank
	if err := query.Find(&words).Error; err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, words)
}

// AddWord handles POST /wordbank
func AddWord(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req AddWordRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}

	word := strings.TrimSpace(req.Word)
	if word == "" {
		return utils.BadRequest(c, "Word is required")
	}

	entry := models.WordBank{
		UserID:      user.ID,
		Word:        strings.ToLower(word),
		Translation: req.Translation,
		Context:     req.Context,
	}

	if req.SessionID != nil {
		sid, err := uuid.Parse(*req.SessionID)
		if err == nil {
			entry.SessionID = &sid
		}
	}

	if err := database.DB.Create(&entry).Error; err != nil {
		return utils.InternalError(c)
	}

	return utils.Created(c, entry)
}

type UpdateWordRequest struct {
	Word        *string `json:"word"`
	Translation *string `json:"translation"`
	Context     *string `json:"context"`
}

// UpdateWord handles PUT /wordbank/:id
func UpdateWord(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	wordID := c.Params("id")

	wid, err := uuid.Parse(wordID)
	if err != nil {
		return utils.BadRequest(c, "Invalid word ID")
	}

	var req UpdateWordRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}

	updates := map[string]interface{}{}
	if req.Word != nil {
		w := strings.TrimSpace(*req.Word)
		if w == "" {
			return utils.BadRequest(c, "Word cannot be empty")
		}
		updates["word"] = strings.ToLower(w)
	}
	if req.Translation != nil {
		updates["translation"] = *req.Translation
	}
	if req.Context != nil {
		updates["context"] = *req.Context
	}
	if len(updates) == 0 {
		return utils.BadRequest(c, "No fields to update")
	}

	result := database.DB.Model(&models.WordBank{}).
		Where("id = ? AND user_id = ?", wid, user.ID).
		Updates(updates)
	if result.RowsAffected == 0 {
		return utils.NotFound(c, "Word not found")
	}

	var entry models.WordBank
	if err := database.DB.First(&entry, "id = ?", wid).Error; err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, entry)
}

// DeleteWord handles DELETE /wordbank/:id
func DeleteWord(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	wordID := c.Params("id")

	wid, err := uuid.Parse(wordID)
	if err != nil {
		return utils.BadRequest(c, "Invalid word ID")
	}

	result := database.DB.
		Where("id = ? AND user_id = ?", wid, user.ID).
		Delete(&models.WordBank{})

	if result.RowsAffected == 0 {
		return utils.NotFound(c, "Word not found")
	}

	return utils.SuccessMessage(c, "Word deleted")
}
