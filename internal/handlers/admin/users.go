package admin

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/safego"
	"github.com/speak-up/backend/internal/services"
	"github.com/speak-up/backend/internal/utils"
	"gorm.io/gorm"
)

// ListUsers handles GET /admin/users?page=1&limit=20&search=&level=&status=
func ListUsers(c *fiber.Ctx) error {
	page := c.QueryInt("page", 1)
	limit := c.QueryInt("limit", 20)
	search := c.Query("search")
	level := c.Query("level")
	status := c.Query("status") // active, banned, premium, all

	if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	query := database.DB.Model(&models.User{})

	// Search by name, username, telegram_id
	if search != "" {
		query = query.Where(
			"first_name ILIKE ? OR last_name ILIKE ? OR username ILIKE ? OR CAST(telegram_id AS TEXT) LIKE ?",
			"%"+search+"%", "%"+search+"%", "%"+search+"%", "%"+search+"%",
		)
	}

	// Filter by level
	if level != "" {
		query = query.Where("level = ?", level)
	}

	// Filter by status
	switch status {
	case "active":
		query = query.Where("is_active = ? AND is_banned = ?", true, false)
	case "banned":
		query = query.Where("is_banned = ?", true)
	case "premium":
		query = query.Where("is_premium = ?", true)
	}

	// Count total
	var total int64
	query.Count(&total)

	// Get paginated results
	var users []models.User
	query.Order("created_at DESC").Offset(offset).Limit(limit).Find(&users)

	// Referral counts for these users (one query)
	userIDs := make([]uuid.UUID, len(users))
	for i := range users {
		userIDs[i] = users[i].ID
	}
	type refRow struct {
		ReferredBy uuid.UUID `json:"referred_by"`
		Count      int       `json:"count"`
	}
	var refCounts []refRow
	if len(userIDs) > 0 {
		database.DB.Raw(`
			SELECT referred_by, COUNT(*) as count
			FROM users
			WHERE referred_by IN ?
			GROUP BY referred_by
		`, userIDs).Scan(&refCounts)
	}
	refMap := make(map[string]int, len(refCounts))
	for _, r := range refCounts {
		refMap[r.ReferredBy.String()] = r.Count
	}

	// Build response with referral_count
	type userWithRef struct {
		models.User
		ReferralCount int `json:"referral_count"`
	}
	result := make([]userWithRef, len(users))
	for i, u := range users {
		result[i] = userWithRef{User: u, ReferralCount: refMap[u.ID.String()]}
	}

	return utils.Success(c, fiber.Map{
		"users": result,
		"pagination": fiber.Map{
			"page":       page,
			"limit":      limit,
			"total":      total,
			"total_pages": (total + int64(limit) - 1) / int64(limit),
		},
	})
}

// GetUser handles GET /admin/users/:id
func GetUser(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	var user models.User
	if err := database.DB.First(&user, "id = ?", uid).Error; err != nil {
		return utils.NotFound(c, "User not found")
	}

	// Get user's sessions count
	var sessionCount int64
	database.DB.Model(&models.Session{}).
		Where("user1_id = ? OR user2_id = ?", uid, uid).
		Count(&sessionCount)

	// Get user's ratings
	var ratings []models.SessionRating
	database.DB.Where("ratee_id = ?", uid).Order("created_at DESC").Limit(10).Find(&ratings)

	// Get user's payments
	var payments []models.Payment
	database.DB.Where("user_id = ?", uid).Order("created_at DESC").Find(&payments)

	// Get word bank count
	var wordCount int64
	database.DB.Model(&models.WordBank{}).Where("user_id = ?", uid).Count(&wordCount)

	// Active (unused) discount coupon, if any - shown to the admin in the
	// grant-premium dialog so they know the coupon will be burned.
	activeDiscount, _ := services.GetActiveDiscount(uid.String())

	// Why this account has premium, and on whose word. Answering that on
	// the user's own page is the point of the ledger.
	var premiumGrants []models.PremiumGrant
	database.DB.Where("user_id = ?", uid).
		Order("created_at DESC").
		Limit(20).
		Find(&premiumGrants)

	return utils.Success(c, fiber.Map{
		"user":            user,
		"session_count":   sessionCount,
		"recent_ratings":  ratings,
		"payments":        payments,
		"word_count":      wordCount,
		"active_discount": activeDiscount,
		"premium_grants":  premiumGrants,
	})
}

// UpdateUser handles PUT /admin/users/:id
func UpdateUser(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	var req struct {
		FirstName *string `json:"first_name"`
		Level     *string `json:"level"`
		Region    *string `json:"region"`
		IsActive  *bool   `json:"is_active"`
		IsBanned  *bool   `json:"is_banned"`
		IsPremium *bool   `json:"is_premium"`
		IsAdmin   *bool   `json:"is_admin"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid request body")
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
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}
	if req.IsBanned != nil {
		updates["is_banned"] = *req.IsBanned
	}
	if req.IsPremium != nil {
		updates["is_premium"] = *req.IsPremium
		if *req.IsPremium {
			// Set premium expiry 30 days from now
			expiry := time.Now().Add(30 * 24 * time.Hour)
			updates["premium_expires_at"] = expiry
		} else {
			updates["premium_expires_at"] = nil
		}
	}
	if req.IsAdmin != nil {
		updates["is_admin"] = *req.IsAdmin
	}

	if len(updates) == 0 {
		return utils.BadRequest(c, "No fields to update")
	}

	result := database.DB.Model(&models.User{}).Where("id = ?", uid).Updates(updates)
	if result.RowsAffected == 0 {
		return utils.NotFound(c, "User not found")
	}

	var user models.User
	database.DB.First(&user, "id = ?", uid)

	return utils.Success(c, user)
}

// BanUser handles POST /admin/users/:id/ban
func BanUser(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	result := database.DB.Model(&models.User{}).
		Where("id = ?", uid).
		Update("is_banned", true)

	if result.RowsAffected == 0 {
		return utils.NotFound(c, "User not found")
	}

	return utils.SuccessMessage(c, "User banned")
}

// UnbanUser handles POST /admin/users/:id/unban
func UnbanUser(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	result := database.DB.Model(&models.User{}).
		Where("id = ?", uid).
		Update("is_banned", false)

	if result.RowsAffected == 0 {
		return utils.NotFound(c, "User not found")
	}

	return utils.SuccessMessage(c, "User unbanned")
}

// TogglePremium handles POST /admin/users/:id/toggle-premium
func TogglePremium(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	var user models.User
	if err := database.DB.First(&user, "id = ?", uid).Error; err != nil {
		return utils.NotFound(c, "User not found")
	}

	updates := map[string]interface{}{}
	if user.IsPremium {
		updates["is_premium"] = false
		updates["premium_expires_at"] = nil
	} else {
		updates["is_premium"] = true
		expiry := time.Now().Add(30 * 24 * time.Hour)
		updates["premium_expires_at"] = expiry
	}

	database.DB.Model(&user).Updates(updates)
	database.DB.First(&user, "id = ?", uid)

	return utils.Success(c, user)
}

// GrantPremium handles POST /admin/users/:id/grant-premium with body
// { "months": N } where 1 ≤ N ≤ 24. Activates premium and sets the
// expiry to "now + N × 30 days". If the user is already premium with a
// future expiry, the new window is added on top of the existing one
// (admin can stack months without users losing days). Total runway is
// capped at 10 years from now so a misclick can't grant lifetime
// premium and so AddDate() can never overflow into year 9999.
//
// The premium update + discount-coupon burn run in a single DB
// transaction so a partial failure can't leave the user premium with
// unburned coupons (or vice versa).
//
// Sends a Telegram notification to the user via the bot on success.
func GrantPremium(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	var req struct {
		Months int `json:"months"`
		// Kind is why this premium is being handed out - a sale, a
		// favour to someone's relative, a partnership. It is required:
		// the whole reason this field exists is that an unlabelled
		// grant is indistinguishable from revenue a month later.
		Kind string `json:"kind"`
		// AmountUZS is the money received, in whole so'm. Required for
		// a sale, ignored for the free kinds.
		AmountUZS int    `json:"amount_uzs"`
		Note      string `json:"note"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.BadRequest(c, "Invalid request body")
	}
	if req.Months < 1 || req.Months > 24 {
		return utils.BadRequest(c, "months must be between 1 and 24")
	}

	req.Kind = strings.TrimSpace(req.Kind)
	if !models.ValidGrantKind(req.Kind) {
		return utils.BadRequest(c, "kind noto'g'ri: sale | friend | partner | promo | compensation | test")
	}
	// A sale with no amount would sit in the ledger as free premium and
	// quietly under-report the month, so it is refused outright.
	if models.IsRevenue(req.Kind) && req.AmountUZS <= 0 {
		return utils.BadRequest(c, "Sotuv uchun summa (amount_uzs) kiritilishi shart")
	}
	if !models.IsRevenue(req.Kind) {
		// Money on a free grant is a mis-tap; drop it rather than let it
		// leak into revenue reports.
		req.AmountUZS = 0
	}
	if req.AmountUZS < 0 || req.AmountUZS > 1_000_000_000 {
		return utils.BadRequest(c, "Summa noto'g'ri")
	}
	req.Note = strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(req.Note) > 512 {
		return utils.BadRequest(c, "Izoh juda uzun")
	}

	var user models.User
	if err := database.DB.First(&user, "id = ?", uid).Error; err != nil {
		return utils.NotFound(c, "User not found")
	}

	now := time.Now()
	// Stack on top of any active window - if the user already has time
	// left, we extend it rather than reset.
	base := now
	if user.IsPremium && user.PremiumExpiresAt != nil && user.PremiumExpiresAt.After(now) {
		base = *user.PremiumExpiresAt
	}
	expiry := base.AddDate(0, req.Months, 0)

	// Hard cap: never let total runway exceed 10 years from now. Any
	// misclick (e.g. admin clicks 12mo on an already-stacked account)
	// past the ceiling is rejected explicitly.
	maxExpiry := now.AddDate(10, 0, 0)
	if expiry.After(maxExpiry) {
		return utils.BadRequest(c, "Total premium window cannot exceed 10 years")
	}

	// Previous window, captured before we overwrite it: premium STACKS,
	// so without this the ledger can't tell three months added to an
	// existing year from a fresh three-month sale.
	var previousExpiry *time.Time
	if user.PremiumExpiresAt != nil {
		prev := *user.PremiumExpiresAt
		previousExpiry = &prev
	}

	admin := middleware.GetCurrentUser(c)

	// Atomic: premium update + coupon consume + ledger row in one
	// transaction so a crash between them can't leave one half applied -
	// and in particular can never leave premium granted with no record
	// of who granted it or why.
	var consumed *services.ActiveDiscountSummary
	grant := models.PremiumGrant{
		UserID:            user.ID,
		Kind:              req.Kind,
		AmountUZS:         req.AmountUZS,
		Note:              req.Note,
		Months:            req.Months,
		PreviousExpiresAt: previousExpiry,
		ExpiresAt:         expiry,
	}
	if admin != nil {
		adminID := admin.ID
		grant.GrantedByID = &adminID
		grant.GrantedByName = admin.DisplayName()
	}

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&user).Updates(map[string]interface{}{
			"is_premium":         true,
			"premium_expires_at": expiry,
		}).Error; err != nil {
			return err
		}
		c, err := services.ConsumeAllActiveDiscountsTx(tx, user.ID.String())
		if err != nil {
			return err
		}
		consumed = c
		if consumed != nil {
			grant.DiscountPercent = consumed.TotalPercent
			grant.CouponsUsed = consumed.Count
		}
		return tx.Create(&grant).Error
	})
	if txErr != nil {
		return utils.InternalError(c)
	}

	// Reload so the response carries fresh fields.
	database.DB.First(&user, "id = ?", uid)

	// Fire-and-forget Telegram notification (panic-safe).
	if user.TelegramID != 0 {
		tgID := user.TelegramID
		months := req.Months
		exp := expiry
		safego.Go("notifyPremiumGranted", func() {
			bot.SendPremiumGranted(tgID, months, exp)
		})
	}

	resp := fiber.Map{
		"user":               user,
		"months":             req.Months,
		"premium_expires_at": expiry,
		"grant":              grant,
	}
	if consumed != nil && consumed.Count > 0 {
		resp["discount_consumed"] = consumed.TotalPercent
		resp["coupons_consumed"] = consumed.Count
	}
	return utils.Success(c, resp)
}

// GetUserActiveDiscount handles GET /admin/users/:id/active-discount.
// Lets the admin grant-premium dialog show the coupon that will be burned.
func GetUserActiveDiscount(c *fiber.Ctx) error {
	userID := c.Params("id")
	if _, err := uuid.Parse(userID); err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	prize, err := services.GetActiveDiscount(userID)
	if err != nil {
		return utils.InternalError(c)
	}
	return utils.Success(c, prize)
}

// RevokePremium handles POST /admin/users/:id/revoke-premium - clears
// the premium flag and expiry immediately, no waiting for the cron.
func RevokePremium(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	res := database.DB.Model(&models.User{}).
		Where("id = ?", uid).
		Updates(map[string]interface{}{
			"is_premium":         false,
			"premium_expires_at": nil,
		})
	if res.Error != nil {
		return utils.InternalError(c)
	}
	if res.RowsAffected == 0 {
		return utils.NotFound(c, "User not found")
	}

	var user models.User
	database.DB.First(&user, "id = ?", uid)
	return utils.Success(c, user)
}

// DeleteUser handles DELETE /admin/users/:id (soft delete)
func DeleteUser(c *fiber.Ctx) error {
	userID := c.Params("id")
	uid, err := uuid.Parse(userID)
	if err != nil {
		return utils.BadRequest(c, "Invalid user ID")
	}

	result := database.DB.Where("id = ?", uid).Delete(&models.User{})
	if result.RowsAffected == 0 {
		return utils.NotFound(c, "User not found")
	}

	return utils.SuccessMessage(c, "User deleted")
}
