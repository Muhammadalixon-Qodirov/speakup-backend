package admin

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

// MockUserInfo is the embedded author info for every mock row.
type MockUserInfo struct {
	ID         uuid.UUID `json:"id"`
	FirstName  string    `json:"first_name"`
	LastName   *string   `json:"last_name"`
	Username   *string   `json:"username"`
	PhotoURL   *string   `json:"photo_url"`
	Level      *string   `json:"level"`
	TelegramID int64     `json:"telegram_id"`
	IsPremium  bool      `json:"is_premium"`
}

// MockRow is a single row in the admin mock list. Combines both
// full_test_reports and speaking_reports into one shape so the UI can
// show a single unified timeline.
type MockRow struct {
	ID          uuid.UUID    `json:"id"`
	Kind        string       `json:"kind"` // "full" | "simple"
	CreatedAt   time.Time    `json:"created_at"`
	OverallBand float64      `json:"overall_band"`
	Topic       string       `json:"topic"`
	Status      string       `json:"status,omitempty"`
	DurationSec int          `json:"duration_sec"`
	WordCount   int          `json:"word_count"`
	User        MockUserInfo `json:"user"`
}

// ListMocks handles GET /admin/mocks?page=1&limit=20&kind=&user_id=
// Merges full_test_reports + speaking_reports into a single paginated feed
// ordered by created_at DESC.
func ListMocks(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	kind := c.Query("kind")     // "full" | "simple" | ""
	userID := c.Query("user_id")

	if limit > 100 {
		limit = 100
	}
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit

	var uid uuid.UUID
	if userID != "" {
		parsed, err := uuid.Parse(userID)
		if err != nil {
			return utils.BadRequest(c, "Invalid user_id")
		}
		uid = parsed
	}

	// Fetch each kind (bounded to page+offset scan) then merge in-memory.
	// Realistic volumes (a few thousand rows) keep this cheap; if either
	// table balloons we can switch to a UNION ALL sub-query.
	fullQ := database.DB.Model(&models.FullTestReport{}).
		Where("status = ?", "completed")
	simpleQ := database.DB.Model(&models.SpeakingReport{})

	if userID != "" {
		fullQ = fullQ.Where("user_id = ?", uid)
		simpleQ = simpleQ.Where("user_id = ?", uid)
	}

	var fullCount, simpleCount int64
	if kind == "" || kind == "full" {
		fullQ.Count(&fullCount)
	}
	if kind == "" || kind == "simple" {
		simpleQ.Count(&simpleCount)
	}
	total := fullCount + simpleCount

	// Load enough from each side to satisfy the requested page.
	// Upper-bound: offset+limit rows from each side, merged and sliced.
	fetchCap := offset + limit

	var fullRows []models.FullTestReport
	var simpleRows []models.SpeakingReport

	if (kind == "" || kind == "full") && fetchCap > 0 {
		fullQ.Order("created_at DESC").Limit(fetchCap).Find(&fullRows)
	}
	if (kind == "" || kind == "simple") && fetchCap > 0 {
		simpleQ.Order("created_at DESC").Limit(fetchCap).Find(&simpleRows)
	}

	// Collect user IDs and batch-load in one query to avoid N+1.
	userIDSet := make(map[uuid.UUID]bool)
	for _, r := range fullRows {
		userIDSet[r.UserID] = true
	}
	for _, r := range simpleRows {
		userIDSet[r.UserID] = true
	}
	userIDs := make([]uuid.UUID, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}
	var users []models.User
	if len(userIDs) > 0 {
		database.DB.Where("id IN ?", userIDs).Find(&users)
	}
	userMap := make(map[uuid.UUID]models.User, len(users))
	for _, u := range users {
		userMap[u.ID] = u
	}

	rows := make([]MockRow, 0, len(fullRows)+len(simpleRows))
	for _, r := range fullRows {
		u := userMap[r.UserID]
		rows = append(rows, MockRow{
			ID:          r.ID,
			Kind:        "full",
			CreatedAt:   r.CreatedAt,
			OverallBand: r.OverallBand,
			Topic:       r.TopicID,
			Status:      r.Status,
			DurationSec: r.TotalDuration,
			WordCount:   r.TotalWords,
			User:        makeUserInfo(u),
		})
	}
	for _, r := range simpleRows {
		u := userMap[r.UserID]
		rows = append(rows, MockRow{
			ID:          r.ID,
			Kind:        "simple",
			CreatedAt:   r.CreatedAt,
			OverallBand: r.OverallBand,
			Topic:       r.Topic,
			DurationSec: r.DurationSec,
			WordCount:   r.WordCount,
			User:        makeUserInfo(u),
		})
	}

	// Merge-sort by created_at DESC, then slice into the requested page.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].CreatedAt.After(rows[j-1].CreatedAt); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	if offset > len(rows) {
		rows = []MockRow{}
	} else {
		end := offset + limit
		if end > len(rows) {
			end = len(rows)
		}
		rows = rows[offset:end]
	}

	return utils.Success(c, fiber.Map{
		"mocks": rows,
		"pagination": fiber.Map{
			"page":        page,
			"limit":       limit,
			"total":       total,
			"total_pages": (total + int64(limit) - 1) / int64(limit),
		},
		"stats": fiber.Map{
			"full_count":   fullCount,
			"simple_count": simpleCount,
		},
	})
}

// GetMock handles GET /admin/mocks/:kind/:id — full report detail.
func GetMock(c *fiber.Ctx) error {
	kind := c.Params("kind")
	rawID := c.Params("id")

	rid, err := uuid.Parse(rawID)
	if err != nil {
		return utils.BadRequest(c, "Invalid ID")
	}

	switch kind {
	case "full":
		var r models.FullTestReport
		if err := database.DB.First(&r, "id = ?", rid).Error; err != nil {
			return utils.NotFound(c, "Mock not found")
		}
		var u models.User
		database.DB.First(&u, "id = ?", r.UserID)
		return utils.Success(c, fiber.Map{
			"kind":   "full",
			"report": r,
			"user":   makeUserInfo(u),
		})
	case "simple":
		var r models.SpeakingReport
		if err := database.DB.First(&r, "id = ?", rid).Error; err != nil {
			return utils.NotFound(c, "Mock not found")
		}
		var u models.User
		database.DB.First(&u, "id = ?", r.UserID)
		return utils.Success(c, fiber.Map{
			"kind":   "simple",
			"report": r,
			"user":   makeUserInfo(u),
		})
	default:
		return utils.BadRequest(c, "kind must be 'full' or 'simple'")
	}
}

func makeUserInfo(u models.User) MockUserInfo {
	return MockUserInfo{
		ID:         u.ID,
		FirstName:  u.FirstName,
		LastName:   u.LastName,
		Username:   u.Username,
		PhotoURL:   u.PhotoURL,
		Level:      u.Level,
		TelegramID: u.TelegramID,
		IsPremium:  u.IsPremium,
	}
}
