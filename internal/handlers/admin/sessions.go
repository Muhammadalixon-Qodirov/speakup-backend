package admin

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

// ListSessions handles GET /admin/sessions?page=1&limit=20&status=&user_id=
func ListSessions(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	status := c.Query("status") // active, ended, cancelled
	userID := c.Query("user_id")

	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	query := database.DB.Model(&models.Session{}).
		Preload("User1").Preload("User2")

	if status != "" {
		query = query.Where("status = ?", status)
	}
	if userID != "" {
		uid, _ := uuid.Parse(userID)
		query = query.Where("user1_id = ? OR user2_id = ?", uid, uid)
	}

	var total int64
	query.Count(&total)

	var sessions []models.Session
	query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&sessions)

	return utils.Success(c, fiber.Map{
		"sessions": sessions,
		"pagination": fiber.Map{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"total_pages": (total + int64(limit) - 1) / int64(limit),
		},
	})
}

// GetSession handles GET /admin/sessions/:id
func GetSession(c *fiber.Ctx) error {
	sessionID := c.Params("id")
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return utils.BadRequest(c, "Invalid session ID")
	}

	var session models.Session
	err = database.DB.
		Preload("User1").Preload("User2").Preload("Ratings").
		First(&session, "id = ?", sid).Error
	if err != nil {
		return utils.NotFound(c, "Session not found")
	}

	return utils.Success(c, session)
}

// ListReports handles GET /admin/reports?page=1&limit=20
// Shows sessions with low ratings (potential toxic users)
func ListReports(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	offset := (page - 1) * limit

	var ratings []models.SessionRating
	var total int64

	query := database.DB.Model(&models.SessionRating{}).
		Where("rating <= 2 OR feedback IS NOT NULL")

	query.Count(&total)

	database.DB.
		Where("rating <= 2 OR feedback IS NOT NULL").
		Preload("Session").
		Order("created_at DESC").
		Offset(offset).Limit(limit).
		Find(&ratings)

	// Batch-load all rater/ratee users in ONE query (fixes N+1)
	type ReportEntry struct {
		models.SessionRating
		RaterName string `json:"rater_name"`
		RateeName string `json:"ratee_name"`
	}

	// Collect all user IDs
	userIDSet := make(map[uuid.UUID]bool)
	for _, r := range ratings {
		userIDSet[r.RaterID] = true
		userIDSet[r.RateeID] = true
	}
	userIDs := make([]uuid.UUID, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}

	// Single query to load all users
	var users []models.User
	if len(userIDs) > 0 {
		database.DB.Where("id IN ?", userIDs).Find(&users)
	}
	userMap := make(map[uuid.UUID]models.User)
	for _, u := range users {
		userMap[u.ID] = u
	}

	var entries []ReportEntry
	for _, r := range ratings {
		rater := userMap[r.RaterID]
		ratee := userMap[r.RateeID]
		entries = append(entries, ReportEntry{
			SessionRating: r,
			RaterName:     rater.DisplayName(),
			RateeName:     ratee.DisplayName(),
		})
	}

	return utils.Success(c, fiber.Map{
		"reports": entries,
		"pagination": fiber.Map{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"total_pages": (total + int64(limit) - 1) / int64(limit),
		},
	})
}
