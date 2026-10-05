package services

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
)

// MaxTotalDiscountPercent caps campaign + coupons together. A campaign
// alone is admin-controlled and trusted, but once wheel coupons stack on
// top the total can run away - 90% is the floor under the price no
// combination may cross.
const MaxTotalDiscountPercent = 90

var (
	ErrCampaignPercent = errors.New("chegirma 1 dan 90 gacha bo'lishi kerak")
	ErrCampaignTitle   = errors.New("aksiya nomi kiritilmagan")
	ErrCampaignTheme   = errors.New("noma'lum mavzu (theme)")
	ErrCampaignWindow  = errors.New("tugash sanasi boshlanish sanasidan keyin bo'lishi kerak")
	ErrCampaignNotFnd  = errors.New("aksiya topilmadi")
	ErrPriceRange      = errors.New("narx 1 000 dan 100 000 000 gacha bo'lishi kerak")
)

// PremiumPricing is the full answer to "what does this user pay right
// now, and why". One struct so the purchase sheet needs a single
// request and can never render a price and a reason that disagree.
type PremiumPricing struct {
	BasePriceUZS  int `json:"base_price_uzs"`
	FinalPriceUZS int `json:"final_price_uzs"`
	// SavingsUZS is base - final, precomputed so the client is not the
	// place where the arithmetic could drift.
	SavingsUZS int `json:"savings_uzs"`

	// Campaign is the live admin campaign, if any.
	Campaign *models.PremiumCampaign `json:"campaign"`

	// The three discount numbers kept apart: the client explains the
	// campaign and the coupons differently, and the total is what was
	// actually applied to the price.
	CampaignPercent int `json:"campaign_percent"`
	CouponPercent   int `json:"coupon_percent"`
	TotalPercent    int `json:"total_percent"`
	CouponCount     int `json:"coupon_count"`

	// CouponsApplied is false when the user holds coupons that this
	// campaign does not stack with - they are untouched and still worth
	// using later, and the client says so instead of hiding them.
	CouponsApplied bool `json:"coupons_applied"`

	// Payment destination, from the same settings row.
	CardNumber    string `json:"card_number"`
	CardOwner     string `json:"card_owner"`
	AdminUsername string `json:"admin_username"`
}

// GetPremiumSettings returns the singleton settings row, creating it on
// first call with the values that used to be compiled into the client.
func GetPremiumSettings() (*models.PremiumSettings, error) {
	var s models.PremiumSettings
	err := database.DB.Order("created_at ASC").First(&s).Error
	if err == nil {
		return &s, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	s = models.PremiumSettings{
		BasePriceUZS:  models.DefaultPremiumPriceUZS,
		CardNumber:    models.DefaultPremiumCard,
		CardOwner:     models.DefaultPremiumOwner,
		AdminUsername: models.DefaultPremiumAdminTG,
	}
	if err := database.DB.Create(&s).Error; err != nil {
		// A parallel first request may have won the race; re-read
		// rather than failing the page.
		var again models.PremiumSettings
		if e2 := database.DB.Order("created_at ASC").First(&again).Error; e2 == nil {
			return &again, nil
		}
		return nil, err
	}
	return &s, nil
}

// UpdatePremiumSettings patches the singleton. Callers pass only the
// fields they mean to change; validation lives here so both the admin
// API and any future caller get the same guardrails.
func UpdatePremiumSettings(
	price *int,
	cardNumber, cardOwner, adminUsername *string,
	admin *models.User,
) (*models.PremiumSettings, error) {
	s, err := GetPremiumSettings()
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if price != nil {
		if *price < 1000 || *price > 100_000_000 {
			return nil, ErrPriceRange
		}
		updates["base_price_uzs"] = *price
	}
	if cardNumber != nil {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, *cardNumber)
		if len(digits) < 12 || len(digits) > 19 {
			return nil, errors.New("karta raqami 12-19 ta raqamdan iborat bo'lishi kerak")
		}
		updates["card_number"] = digits
	}
	if cardOwner != nil {
		name := strings.TrimSpace(*cardOwner)
		if name == "" {
			return nil, errors.New("karta egasi kiritilmagan")
		}
		updates["card_owner"] = name
	}
	if adminUsername != nil {
		// Accept "@name" and "t.me/name" alike; store the bare handle so
		// the client can build any link shape it wants.
		u := strings.TrimSpace(*adminUsername)
		u = strings.TrimPrefix(u, "https://")
		u = strings.TrimPrefix(u, "http://")
		u = strings.TrimPrefix(u, "t.me/")
		u = strings.TrimPrefix(u, "@")
		u = strings.Trim(u, "/")
		if u == "" {
			return nil, errors.New("admin username kiritilmagan")
		}
		updates["admin_username"] = u
	}
	if len(updates) == 0 {
		return s, nil
	}

	if admin != nil {
		id := admin.ID
		updates["updated_by_id"] = &id
		updates["updated_by_name"] = admin.DisplayName()
	}

	if err := database.DB.Model(s).Updates(updates).Error; err != nil {
		return nil, err
	}
	return GetPremiumSettings()
}

// ActivePremiumCampaign returns the campaign that should be running
// right now, or nil. When several overlap the biggest discount wins -
// an admin who leaves an old campaign switched on can only ever give
// the user a better deal than intended, never a worse one.
func ActivePremiumCampaign() (*models.PremiumCampaign, error) {
	now := time.Now()

	var rows []models.PremiumCampaign
	err := database.DB.
		Where("is_active = ?", true).
		Where("starts_at IS NULL OR starts_at <= ?", now).
		Where("ends_at IS NULL OR ends_at > ?", now).
		Order("discount_percent DESC, created_at DESC").
		Limit(20).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].IsLive(now) {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// ResolvePremiumPricing computes what this specific user pays.
//
// Two discount sources meet here and they are deliberately NOT always
// summed: a campaign is a marketing decision the admin priced, while
// wheel coupons are something the user earned. When the campaign does
// not stack, the user is charged the better of the two and the coupons
// stay unspent - losing an earned coupon to a discount you would have
// got anyway is the kind of thing users notice and resent.
//
// userID may be empty, in which case only the campaign applies.
func ResolvePremiumPricing(userID string) (*PremiumPricing, error) {
	settings, err := GetPremiumSettings()
	if err != nil {
		return nil, err
	}
	campaign, err := ActivePremiumCampaign()
	if err != nil {
		return nil, err
	}

	out := &PremiumPricing{
		BasePriceUZS:  settings.BasePriceUZS,
		Campaign:      campaign,
		CardNumber:    settings.CardNumber,
		CardOwner:     settings.CardOwner,
		AdminUsername: settings.AdminUsername,
	}
	if campaign != nil {
		out.CampaignPercent = campaign.DiscountPercent
	}

	if userID != "" {
		if summary, err := GetActiveDiscounts(userID); err == nil && summary != nil {
			out.CouponPercent = summary.TotalPercent
			out.CouponCount = summary.Count
		}
	}

	switch {
	case out.CouponPercent == 0:
		out.TotalPercent = out.CampaignPercent
	case campaign == nil:
		out.TotalPercent = out.CouponPercent
		out.CouponsApplied = true
	case campaign.StacksWithCoupons:
		out.TotalPercent = out.CampaignPercent + out.CouponPercent
		out.CouponsApplied = true
	case out.CouponPercent >= out.CampaignPercent:
		out.TotalPercent = out.CouponPercent
		out.CouponsApplied = true
	default:
		// Campaign alone beats the coupons - keep them for next time.
		out.TotalPercent = out.CampaignPercent
	}
	if out.TotalPercent > MaxTotalDiscountPercent {
		out.TotalPercent = MaxTotalDiscountPercent
	}

	out.FinalPriceUZS = ApplyDiscountUZS(settings.BasePriceUZS, out.TotalPercent)
	out.SavingsUZS = settings.BasePriceUZS - out.FinalPriceUZS
	return out, nil
}

// ApplyDiscountUZS takes percent off and rounds to the nearest 100 so'm.
// Rounding is deliberate: this price is typed into a bank transfer by
// hand, and "24 000" is transferable in a way "23 970" is not.
func ApplyDiscountUZS(base, percent int) int {
	if percent <= 0 {
		return base
	}
	if percent > MaxTotalDiscountPercent {
		percent = MaxTotalDiscountPercent
	}
	raw := float64(base) * (1 - float64(percent)/100)
	rounded := int((raw+50)/100) * 100
	if rounded < 0 {
		rounded = 0
	}
	return rounded
}

// ── Campaign CRUD (admin) ──────────────────────────────────────────

// ListPremiumCampaigns returns every campaign newest-first, live ones
// included - the admin table shows past campaigns too, because "what
// did we run last Navro'z" is the main input to planning the next one.
func ListPremiumCampaigns() ([]models.PremiumCampaign, error) {
	var rows []models.PremiumCampaign
	err := database.DB.Order("created_at DESC").Limit(200).Find(&rows).Error
	return rows, err
}

// CampaignInput is the writable shape of a campaign. Pointer fields are
// "leave alone" on update; on create the zero values are used.
type CampaignInput struct {
	Title             *string    `json:"title"`
	Reason            *string    `json:"reason"`
	Emoji             *string    `json:"emoji"`
	Theme             *string    `json:"theme"`
	DiscountPercent   *int       `json:"discount_percent"`
	StartsAt          *time.Time `json:"starts_at"`
	EndsAt            *time.Time `json:"ends_at"`
	ClearStartsAt     bool       `json:"clear_starts_at"`
	ClearEndsAt       bool       `json:"clear_ends_at"`
	IsActive          *bool      `json:"is_active"`
	StacksWithCoupons *bool      `json:"stacks_with_coupons"`
}

func CreatePremiumCampaign(in CampaignInput, admin *models.User) (*models.PremiumCampaign, error) {
	c := models.PremiumCampaign{
		Theme:    models.ThemeHoliday,
		IsActive: true,
	}
	if in.Title != nil {
		c.Title = strings.TrimSpace(*in.Title)
	}
	if in.Reason != nil {
		c.Reason = strings.TrimSpace(*in.Reason)
	}
	if in.Emoji != nil {
		c.Emoji = strings.TrimSpace(*in.Emoji)
	}
	if in.Theme != nil && strings.TrimSpace(*in.Theme) != "" {
		c.Theme = strings.TrimSpace(*in.Theme)
	}
	if in.DiscountPercent != nil {
		c.DiscountPercent = *in.DiscountPercent
	}
	c.StartsAt = in.StartsAt
	c.EndsAt = in.EndsAt
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}
	if in.StacksWithCoupons != nil {
		c.StacksWithCoupons = *in.StacksWithCoupons
	}
	if admin != nil {
		id := admin.ID
		c.CreatedByID = &id
		c.CreatedByName = admin.DisplayName()
	}

	if err := validateCampaign(&c); err != nil {
		return nil, err
	}
	if err := database.DB.Create(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func UpdatePremiumCampaign(id uuid.UUID, in CampaignInput) (*models.PremiumCampaign, error) {
	var c models.PremiumCampaign
	if err := database.DB.First(&c, "id = ?", id).Error; err != nil {
		return nil, ErrCampaignNotFnd
	}

	if in.Title != nil {
		c.Title = strings.TrimSpace(*in.Title)
	}
	if in.Reason != nil {
		c.Reason = strings.TrimSpace(*in.Reason)
	}
	if in.Emoji != nil {
		c.Emoji = strings.TrimSpace(*in.Emoji)
	}
	if in.Theme != nil {
		c.Theme = strings.TrimSpace(*in.Theme)
	}
	if in.DiscountPercent != nil {
		c.DiscountPercent = *in.DiscountPercent
	}
	if in.ClearStartsAt {
		c.StartsAt = nil
	} else if in.StartsAt != nil {
		c.StartsAt = in.StartsAt
	}
	if in.ClearEndsAt {
		c.EndsAt = nil
	} else if in.EndsAt != nil {
		c.EndsAt = in.EndsAt
	}
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}
	if in.StacksWithCoupons != nil {
		c.StacksWithCoupons = *in.StacksWithCoupons
	}

	if err := validateCampaign(&c); err != nil {
		return nil, err
	}
	// Write the columns explicitly: Save() would also rewrite
	// created_at/created_by and clobber the audit trail, and a struct
	// update would skip the false/nil values an admin just chose.
	err := database.DB.Model(&c).Updates(map[string]interface{}{
		"title":               c.Title,
		"reason":              c.Reason,
		"emoji":               c.Emoji,
		"theme":               c.Theme,
		"discount_percent":    c.DiscountPercent,
		"starts_at":           c.StartsAt,
		"ends_at":             c.EndsAt,
		"is_active":           c.IsActive,
		"stacks_with_coupons": c.StacksWithCoupons,
	}).Error
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func DeletePremiumCampaign(id uuid.UUID) error {
	res := database.DB.Delete(&models.PremiumCampaign{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrCampaignNotFnd
	}
	return nil
}

func validateCampaign(c *models.PremiumCampaign) error {
	if strings.TrimSpace(c.Title) == "" {
		return ErrCampaignTitle
	}
	if c.DiscountPercent < 1 || c.DiscountPercent > MaxTotalDiscountPercent {
		return ErrCampaignPercent
	}
	if c.Theme == "" {
		c.Theme = models.ThemeHoliday
	}
	if !models.ValidCampaignTheme(c.Theme) {
		return ErrCampaignTheme
	}
	if c.StartsAt != nil && c.EndsAt != nil && !c.EndsAt.After(*c.StartsAt) {
		return ErrCampaignWindow
	}
	if len([]rune(c.Emoji)) > 4 {
		return errors.New("emoji juda uzun")
	}
	return nil
}
