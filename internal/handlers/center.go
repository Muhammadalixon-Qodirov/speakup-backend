package handlers

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
)

// Partner learning centre endpoints.
//
// Three audiences, three access levels:
//   - the centre's own admin edits its profile   (/centers/mine)
//   - any user views an approved centre's card   (/centers/:id)
//   - any user in a session fetches the rotation (/session-banners)
//
// Creating a centre and approving its content are admin-only and live in
// handlers/admin/centers.go.

func centerErrorStatus(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, services.ErrCenterNotFound):
		return utils.ErrorWithCode(c, fiber.StatusNotFound, "CENTER_NOT_FOUND", "Markaz topilmadi")
	case errors.Is(err, services.ErrNotCenterOwner):
		return utils.ErrorWithCode(c, fiber.StatusForbidden, "NOT_CENTER_OWNER", "Faqat markaz admini uchun")
	case errors.Is(err, services.ErrBadLogo):
		return utils.ErrorWithCode(c, fiber.StatusBadRequest, "BAD_LOGO", err.Error())
	default:
		return utils.BadRequest(c, err.Error())
	}
}

// requireMyCenter resolves the centre the caller administers.
func requireMyCenter(c *fiber.Ctx) (*models.StudyCenter, *models.User, error) {
	user := middleware.GetCurrentUser(c)
	center, err := services.GetCenterByOwner(user.ID)
	if err != nil {
		return nil, nil, err
	}
	return center, user, nil
}

// GetMyCenter handles GET /centers/mine
//
// The centre-admin dashboard header. 404 for everyone who doesn't run a
// centre, which is how the frontend decides whether to show the section
// at all.
func GetMyCenter(c *fiber.Ctx) error {
	center, _, err := requireMyCenter(c)
	if err != nil {
		return centerErrorStatus(c, err)
	}

	rooms, _ := services.CenterRooms(center.ID)
	roomsOut := make([]fiber.Map, 0, len(rooms))
	for i := range rooms {
		roomsOut = append(roomsOut, fiber.Map{
			"id":             rooms[i].ID,
			"name":           rooms[i].Name,
			"member_count":   rooms[i].MemberCount,
			"total_sessions": rooms[i].TotalSessions,
			"total_minutes":  rooms[i].TotalMinutes,
			"is_open":        rooms[i].IsOpen(),
		})
	}

	return utils.Success(c, fiber.Map{
		"center": center,
		"rooms":  roomsOut,
		// Spelled out so the dashboard can explain the banner's state
		// instead of leaving the centre guessing why it isn't showing.
		"banner_eligible": center.BannerEligible(),
		"contract_live":   center.ContractLive(),
	})
}

type updateCenterRequest struct {
	Name         *string            `json:"name"`
	About        *string            `json:"about"`
	Courses      *models.CourseList `json:"courses"`
	Phone        *string            `json:"phone"`
	Address      *string            `json:"address"`
	TelegramURL  *string            `json:"telegram_url"`
	InstagramURL *string            `json:"instagram_url"`
	WebsiteURL   *string            `json:"website_url"`
	SignupURL    *string            `json:"signup_url"`
}

// UpdateMyCenter handles PUT /centers/mine
//
// Editing name, about or courses sends the profile back for review, and
// the response says so - a centre that isn't told will assume its banner
// is still running.
func UpdateMyCenter(c *fiber.Ctx) error {
	center, _, err := requireMyCenter(c)
	if err != nil {
		return centerErrorStatus(c, err)
	}

	var req updateCenterRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Noto'g'ri so'rov")
	}

	updated, needsReview, err := services.UpdateCenterProfile(center.ID, services.CenterProfileInput{
		Name:         req.Name,
		About:        req.About,
		Courses:      req.Courses,
		Phone:        req.Phone,
		Address:      req.Address,
		TelegramURL:  req.TelegramURL,
		InstagramURL: req.InstagramURL,
		WebsiteURL:   req.WebsiteURL,
		SignupURL:    req.SignupURL,
	})
	if err != nil {
		return centerErrorStatus(c, err)
	}

	return utils.Success(c, fiber.Map{
		"center":       updated,
		"needs_review": needsReview,
		"message":      reviewMessage(needsReview),
	})
}

func reviewMessage(needsReview bool) string {
	if needsReview {
		return "Saqlandi. O'zgarish tasdiqlanguncha banner ko'rinmaydi."
	}
	return "Saqlandi."
}

// UploadCenterLogo handles POST /centers/mine/logo (multipart, field "logo")
func UploadCenterLogo(c *fiber.Ctx) error {
	center, _, err := requireMyCenter(c)
	if err != nil {
		return centerErrorStatus(c, err)
	}

	fh, ferr := c.FormFile("logo")
	if ferr != nil {
		return utils.BadRequest(c, "Rasm yuborilmadi")
	}

	path, serr := services.SaveCenterLogo(center.ID, fh)
	if serr != nil {
		return centerErrorStatus(c, serr)
	}

	return utils.Success(c, fiber.Map{
		"logo_url":     path,
		"needs_review": true,
		"message":      "Logotip yuklandi. Tasdiqlanguncha banner ko'rinmaydi.",
	})
}

// SubmitCenterForReview handles POST /centers/mine/submit
func SubmitCenterForReview(c *fiber.Ctx) error {
	center, _, err := requireMyCenter(c)
	if err != nil {
		return centerErrorStatus(c, err)
	}

	updated, serr := services.SubmitCenterForReview(center.ID)
	if serr != nil {
		return utils.BadRequest(c, serr.Error())
	}

	return utils.Success(c, updated)
}

// GetSessionBanners handles GET /session-banners
//
// Called once when a live session opens; the client then rotates through
// the returned list locally. Returning a list rather than one banner per
// request keeps this off the hot path entirely.
func GetSessionBanners(c *fiber.Ctx) error {
	return utils.Success(c, services.SessionBanners())
}

// GetCenterProfile handles GET /centers/:id
//
// The card behind a banner tap. Only approved, in-contract centres
// resolve, so a link shared in a chat stops working when the partnership
// does.
func GetCenterProfile(c *fiber.Ctx) error {
	centerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Markaz ID noto'g'ri")
	}

	center, cerr := services.PublicCenterProfile(centerID)
	if cerr != nil {
		return centerErrorStatus(c, cerr)
	}

	return utils.Success(c, fiber.Map{
		"id":            center.ID,
		"name":          center.Name,
		"logo_url":      center.LogoURL,
		"about":         center.About,
		"courses":       center.Courses,
		"phone":         center.Phone,
		"address":       center.Address,
		"telegram_url":  center.TelegramURL,
		"instagram_url": center.InstagramURL,
		"website_url":   center.WebsiteURL,
		"signup_url":    center.SignupURL,
	})
}

// TrackBannerClick handles POST /centers/:id/click
//
// Clicks are the number a partner is actually sold on, so they are
// counted server-side and deduplicated per user per hour.
func TrackBannerClick(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)

	centerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.BadRequest(c, "Markaz ID noto'g'ri")
	}

	counted := services.RecordBannerClick(centerID, user.ID.String())

	return utils.Success(c, fiber.Map{"counted": counted})
}
