package handlers

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MeResponse embeds the User and adds the auth source. Go's JSON marshaler
// flattens the embedded struct so existing fields remain at the top level.
type MeResponse struct {
	models.User
	AuthSource string `json:"auth_source"`
}

type UpdateProfileRequest struct {
	FirstName *string  `json:"first_name"`
	Level     *string  `json:"level"`     // A1, A2, B1, B2, C1, C2
	Region    *string  `json:"region"`
	Interests []string `json:"interests"`
	Gender    *string  `json:"gender"`    // "male" | "female"
}

// GetMyProfile handles GET /users/me.
// Returns the user plus the `auth_source` field extracted from the JWT,
// so the frontend knows (authoritatively) whether the session was opened
// via Telegram Mini App or via the web / bot OTP flow.
func GetMyProfile(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	return utils.Success(c, MeResponse{
		User:       *user,
		AuthSource: middleware.GetAuthSource(c),
	})
}

// UpdateProfile handles PUT /users/me
// All changes (fields + interests) run inside a single transaction with
// SELECT FOR UPDATE to prevent lost updates from concurrent requests.
func UpdateProfile(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req UpdateProfileRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	// Validate before entering the transaction.
	if req.Level != nil {
		valid := map[string]bool{"A1": true, "A2": true, "B1": true, "B2": true, "C1": true, "C2": true}
		if !valid[*req.Level] {
			return utils.BadRequest(c, "Daraja A1, A2, B1, B2, C1 yoki C2 bo'lishi kerak")
		}
	}
	if req.Gender != nil {
		g := *req.Gender
		if g != "male" && g != "female" && g != "" {
			return utils.BadRequest(c, "Jins 'male' yoki 'female' bo'lishi kerak")
		}
	}

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// Lock the user row to prevent lost updates.
		var locked models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&locked, "id = ?", user.ID).Error; err != nil {
			return err
		}

		updates := map[string]interface{}{}
		if req.FirstName != nil {
			updates["first_name"] = *req.FirstName
		}
		if req.Level != nil {
			updates["level"] = *req.Level
		}
		if req.Region != nil {
			updates["region"] = *req.Region
		}
		if req.Gender != nil {
			g := *req.Gender
			if g == "" {
				updates["gender"] = nil
			} else {
				updates["gender"] = g
			}
		}
		// Mark as onboarded if level and region are set
		if req.Level != nil && req.Region != nil {
			updates["is_onboarded"] = true
		} else if !locked.IsOnboarded && locked.Level != nil && req.Region != nil {
			updates["is_onboarded"] = true
		} else if !locked.IsOnboarded && req.Level != nil && locked.Region != nil {
			updates["is_onboarded"] = true
		}

		if len(updates) > 0 {
			if err := tx.Model(&locked).Updates(updates).Error; err != nil {
				return err
			}
		}

		// Interests (pq.StringArray needs direct assignment)
		if req.Interests != nil {
			if err := tx.Exec("UPDATE users SET interests = ? WHERE id = ?",
				pq.StringArray(req.Interests), locked.ID).Error; err != nil {
				return err
			}
		}

		// Reload into the outer user pointer so the response is fresh.
		return tx.First(user, "id = ?", user.ID).Error
	})

	if txErr != nil {
		log.Error().Err(txErr).Msg("Failed to update profile")
		return utils.InternalError(c)
	}

	return utils.Success(c, user)
}

// PinSpeakingRequest carries the report ID the user wants to display on
// their profile. A nil/empty report_id clears the pin.
type PinSpeakingRequest struct {
	ReportID *string `json:"report_id"`
}

// SetPinnedSpeaking handles PUT /users/me/pinned-speaking.
// Pins (or clears) an AI Speaking report so its band score appears as
// a badge on the user's profile. The report MUST belong to the caller -
// we validate ownership before storing the denormalised band score.
func SetPinnedSpeaking(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var req PinSpeakingRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	updates := map[string]interface{}{
		"pinned_speaking_band":       nil,
		"pinned_speaking_report_id":  nil,
		"pinned_speaking_at":         nil,
	}

	if req.ReportID != nil && *req.ReportID != "" {
		rid, err := uuid.Parse(*req.ReportID)
		if err != nil {
			return utils.BadRequest(c, "Noto'g'ri report ID")
		}

		// Try simple speaking report first, then full test report.
		var band float64
		var found bool

		var simple models.SpeakingReport
		if database.DB.Where("id = ? AND user_id = ?", rid, user.ID).First(&simple).Error == nil {
			band = simple.OverallBand
			found = true
		}

		if !found {
			var full models.FullTestReport
			if database.DB.Where("id = ? AND user_id = ?", rid, user.ID).First(&full).Error == nil {
				band = full.OverallBand
				found = true
			}
		}

		if !found {
			return utils.NotFound(c, "Report topilmadi")
		}

		now := time.Now()
		updates["pinned_speaking_band"] = band
		updates["pinned_speaking_report_id"] = rid
		updates["pinned_speaking_at"] = now
	}

	if err := database.DB.Model(user).Updates(updates).Error; err != nil {
		log.Error().Err(err).Msg("Failed to update pinned speaking")
		return utils.InternalError(c)
	}

	if err := database.DB.First(user, "id = ?", user.ID).Error; err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, MeResponse{
		User:       *user,
		AuthSource: middleware.GetAuthSource(c),
	})
}

// GetDailyUsage handles GET /users/daily-usage
func GetDailyUsage(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	usage, err := services.GetDailyUsage(user)
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, usage)
}

// GetMySessions handles GET /users/me/sessions?page=1&limit=20
// Returns session history with partner info.
func GetMySessions(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}
	offset := (page - 1) * limit

	// Only preload the partner side (the other user). Preloading both
	// User1 and User2 would double the per-row work; the current user
	// is already in `user`.
	var sessions []models.Session
	err := database.DB.
		Where("(user1_id = ? OR user2_id = ?) AND status = ?", user.ID, user.ID, "ended").
		Preload("User1", "id <> ?", user.ID).
		Preload("User2", "id <> ?", user.ID).
		Preload("Ratings", "rater_id = ?", user.ID).
		Order("ended_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&sessions).Error
	if err != nil {
		return utils.InternalError(c)
	}

	type PartnerInfo struct {
		ID        string  `json:"id"`
		FirstName string  `json:"first_name"`
		LastName  *string `json:"last_name"`
		PhotoURL  *string `json:"photo_url"`
		Level     *string `json:"level"`
		IsPremium bool    `json:"is_premium"`
	}

	type SessionItem struct {
		ID              string      `json:"id"`
		Partner         PartnerInfo `json:"partner"`
		Topic           *string     `json:"topic"`
		DurationSeconds int         `json:"duration_seconds"`
		DurationMinutes int         `json:"duration_minutes"`
		Status          string      `json:"status"`
		StartedAt       interface{} `json:"started_at"`
		EndedAt         interface{} `json:"ended_at"`
		MyRating        *int        `json:"my_rating"`
	}

	result := make([]SessionItem, 0, len(sessions))
	for _, s := range sessions {
		// Determine partner
		partner := s.User2
		if s.User1ID != user.ID {
			partner = s.User1
		}

		item := SessionItem{
			ID: s.ID.String(),
			Partner: PartnerInfo{
				ID:        partner.ID.String(),
				FirstName: partner.FirstName,
				LastName:  partner.LastName,
				PhotoURL:  partner.PhotoURL,
				Level:     partner.Level,
				IsPremium: partner.IsPremium,
			},
			Topic:           s.Topic,
			DurationSeconds: s.DurationSeconds,
			DurationMinutes: s.DurationMinutes(),
			Status:          s.Status,
			StartedAt:       s.StartedAt,
			EndedAt:         s.EndedAt,
		}

		// My rating for this session
		if len(s.Ratings) > 0 {
			item.MyRating = &s.Ratings[0].Rating
		}

		result = append(result, item)
	}

	return utils.Success(c, result)
}

// GetMyFeedback handles GET /users/me/feedback
// Returns all ratings/feedback received by the current user.
func GetMyFeedback(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	var ratings []models.SessionRating
	err := database.DB.
		Where("ratee_id = ?", user.ID).
		Order("created_at DESC").
		Limit(50).
		Find(&ratings).Error
	if err != nil {
		return utils.InternalError(c)
	}

	return utils.Success(c, ratings)
}
