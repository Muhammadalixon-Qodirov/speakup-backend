package services

import (
	"errors"
	"math/rand"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
)

// Prize box configuration.
const (
	PrizesPerMonth = 3
)

// PrizeOutcome describes one possible reward for a spin.
type PrizeOutcome struct {
	Type   string // "minutes" | "discount" | "premium"
	Value  int    // minutes count, discount %, premium days
	Weight int    // relative spin weight (higher = more likely)
}

// PrizeWheel - the set of possible prizes plus their weights. Lower-weight
// entries (premium especially) are intentionally rare. Tweak weights here
// to retune the economy without code changes elsewhere.
//
// Premium was retuned to be ~2x rarer: all non-premium weights were
// doubled while premium stayed at 5, so premium's share drops from
// 5/97 (~5.15%) to 5/189 (~2.65%) — roughly half the previous odds.
var PrizeWheel = []PrizeOutcome{
	{Type: "minutes", Value: 1, Weight: 64},  // very common
	{Type: "minutes", Value: 3, Weight: 48},  // common
	{Type: "minutes", Value: 5, Weight: 32},  // uncommon
	{Type: "discount", Value: 3, Weight: 24}, // -3%
	{Type: "discount", Value: 5, Weight: 16}, // -5%
	{Type: "premium", Value: 30, Weight: 5},  // 1 month - rare (~2.65%)
}

// EnsurePrizesForMonth checks that the user has 3 prize-box days for the
// current Tashkent calendar month. If not, it generates them on random
// future days inside the month. Idempotent - safe to call on every login.
func EnsurePrizesForMonth(userID string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return err
	}

	now := time.Now().In(tashkentLoc)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tashkentLoc)
	nextMonth := monthStart.AddDate(0, 1, 0)

	// How many prize boxes does this user already have for this month?
	var existing int64
	database.DB.Model(&models.Prize{}).
		Where("user_id = ? AND available_on >= ? AND available_on < ?",
			uid, monthStart, nextMonth).
		Count(&existing)

	if existing >= int64(PrizesPerMonth) {
		return nil
	}

	need := PrizesPerMonth - int(existing)

	// Pick `need` distinct days from the remaining days of this month
	// (today included). The user's own ID is mixed into the seed so two
	// different users get different schedules even on the same day.
	today := startOfTashkentDay(now)
	daysLeftInMonth := int(nextMonth.Sub(today).Hours() / 24)
	if daysLeftInMonth <= 0 {
		return nil
	}

	// Build candidate day set.
	candidates := make([]time.Time, 0, daysLeftInMonth)
	for i := 0; i < daysLeftInMonth; i++ {
		candidates = append(candidates, today.AddDate(0, 0, i))
	}

	// Remove days the user already has prizes for.
	var taken []time.Time
	database.DB.Model(&models.Prize{}).
		Where("user_id = ? AND available_on >= ? AND available_on < ?",
			uid, monthStart, nextMonth).
		Pluck("available_on", &taken)
	skip := make(map[string]bool, len(taken))
	for _, t := range taken {
		skip[t.In(tashkentLoc).Format("2006-01-02")] = true
	}
	filtered := candidates[:0]
	for _, c := range candidates {
		if !skip[c.Format("2006-01-02")] {
			filtered = append(filtered, c)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	// Deterministic seed per (user, month) so re-runs pick the same days
	// - avoids accidental drift if the cron runs twice.
	seed := int64(uid.ID()) ^ int64(now.Year()*100+int(now.Month()))
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(filtered), func(i, j int) {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	})
	if need > len(filtered) {
		need = len(filtered)
	}
	chosen := filtered[:need]
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].Before(chosen[j]) })

	// Insert.
	for _, day := range chosen {
		row := models.Prize{
			UserID:      uid,
			AvailableOn: day,
			Status:      "pending",
		}
		database.DB.Create(&row)
	}

	return nil
}

// GetTodayPrize returns the user's pending prize box for today (Tashkent
// calendar day), or nil if none.
func GetTodayPrize(userID string) (*models.Prize, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, err
	}

	today := startOfTashkentDay(time.Now())
	tomorrow := today.AddDate(0, 0, 1)

	var p models.Prize
	err = database.DB.
		Where("user_id = ? AND status = ? AND available_on >= ? AND available_on < ?",
			uid, "pending", today, tomorrow).
		First(&p).Error
	if err != nil {
		return nil, nil // not found is not an error here
	}
	return &p, nil
}

// ListUserPrizes returns the user's prizes for the current month.
func ListUserPrizes(userID string) ([]models.Prize, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, err
	}

	now := time.Now().In(tashkentLoc)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, tashkentLoc)
	nextMonth := monthStart.AddDate(0, 1, 0)

	var prizes []models.Prize
	err = database.DB.
		Where("user_id = ? AND available_on >= ? AND available_on < ?",
			uid, monthStart, nextMonth).
		Order("available_on ASC").
		Find(&prizes).Error
	return prizes, err
}

// MaxStackedDiscountPercent caps the total discount a user can stack from
// prize-box coupons. The wheel emits 3% and 5% only, so realistic monthly
// max is 15%, but we hard-cap at 50% to bound any future bug or wheel
// retune from accidentally giving away free premium.
const MaxStackedDiscountPercent = 50

// ActiveDiscountSummary describes a user's currently usable discount
// coupons. The same shape is returned to user and admin endpoints so the
// frontend can render a uniform price preview.
type ActiveDiscountSummary struct {
	TotalPercent int            `json:"total_percent"` // capped sum
	Count        int            `json:"count"`         // number of coupons
	Coupons      []models.Prize `json:"coupons"`
}

// GetActiveDiscounts returns ALL of the user's opened-but-unused discount
// coupons. Stacks naturally - winning multiple discounts in a month all
// pool into the next premium purchase. Each prize_value is verified to
// have come from the wheel (so any future bug that injects a rogue value
// can't multiply real money off).
func GetActiveDiscounts(userID string) (*ActiveDiscountSummary, error) {
	return getActiveDiscountsTx(database.DB, userID)
}

func getActiveDiscountsTx(db *gorm.DB, userID string) (*ActiveDiscountSummary, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, err
	}

	var prizes []models.Prize
	err = db.
		Where("user_id = ? AND status = ? AND prize_type = ? AND used_at IS NULL",
			uid, "opened", "discount").
		Order("opened_at ASC").
		Find(&prizes).Error
	if err != nil {
		return &ActiveDiscountSummary{}, nil
	}

	total := 0
	valid := prizes[:0]
	for _, p := range prizes {
		if p.PrizeValue == nil {
			continue
		}
		// Defense-in-depth: only honour values the wheel can actually
		// emit. Anything else means the row was tampered with at the DB
		// level - ignore it AND log it so an admin notices.
		if !isWheelDiscountValue(*p.PrizeValue) {
			log.Warn().
				Str("user_id", userID).
				Str("prize_id", p.ID.String()).
				Int("prize_value", *p.PrizeValue).
				Msg("discount coupon with non-wheel value; ignoring")
			continue
		}
		total += *p.PrizeValue
		valid = append(valid, p)
	}
	if total > MaxStackedDiscountPercent {
		total = MaxStackedDiscountPercent
	}

	return &ActiveDiscountSummary{
		TotalPercent: total,
		Count:        len(valid),
		Coupons:      valid,
	}, nil
}

// isWheelDiscountValue verifies a discount percentage is one of the values
// PrizeWheel can actually emit. Used as a safety filter so even if a rogue
// prize_value lands in the DB it can't be honoured.
func isWheelDiscountValue(v int) bool {
	for _, p := range PrizeWheel {
		if p.Type == "discount" && p.Value == v {
			return true
		}
	}
	return false
}

// GetActiveDiscount is kept for the admin response shape that wants a
// single summary - returns the same struct as GetActiveDiscounts.
func GetActiveDiscount(userID string) (*ActiveDiscountSummary, error) {
	return GetActiveDiscounts(userID)
}

// ConsumeAllActiveDiscounts burns every unused discount coupon the user
// has. Atomic - either all flip to used or nothing changes. Thin
// wrapper around the *Tx variant for callers without an existing
// transaction.
func ConsumeAllActiveDiscounts(userID string) (*ActiveDiscountSummary, error) {
	return ConsumeAllActiveDiscountsTx(database.DB, userID)
}

// ConsumeAllActiveDiscountsTx is the transactional flavor - use this
// when the caller already runs inside a database.DB.Transaction so
// premium activation + coupon burn commit/rollback together.
func ConsumeAllActiveDiscountsTx(db *gorm.DB, userID string) (*ActiveDiscountSummary, error) {
	summary, err := getActiveDiscountsTx(db, userID)
	if err != nil || summary == nil || summary.Count == 0 {
		return summary, err
	}

	uid, _ := uuid.Parse(userID)
	now := time.Now()
	res := db.Model(&models.Prize{}).
		Where("user_id = ? AND status = ? AND prize_type = ? AND used_at IS NULL",
			uid, "opened", "discount").
		Update("used_at", now)
	if res.Error != nil {
		return nil, res.Error
	}
	return summary, nil
}

// SpinPrize selects a random prize from the wheel, weighted by `Weight`.
func SpinPrize() PrizeOutcome {
	total := 0
	for _, p := range PrizeWheel {
		total += p.Weight
	}
	if total <= 0 {
		return PrizeWheel[0]
	}
	pick := rand.Intn(total)
	cum := 0
	for _, p := range PrizeWheel {
		cum += p.Weight
		if pick < cum {
			return p
		}
	}
	return PrizeWheel[len(PrizeWheel)-1]
}

// OpenPrize spins, applies the result to the user, marks the box opened
// and returns the outcome. Returns an error if the prize doesn't exist,
// belongs to another user, isn't available today, or has already been opened.
func OpenPrize(prizeID string, userID string) (*PrizeOutcome, error) {
	pid, err := uuid.Parse(prizeID)
	if err != nil {
		return nil, errors.New("invalid prize id")
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, errors.New("invalid user id")
	}

	var prize models.Prize
	if err := database.DB.First(&prize, "id = ?", pid).Error; err != nil {
		return nil, errors.New("prize not found")
	}
	if prize.UserID != uid {
		return nil, errors.New("prize not yours")
	}
	if prize.Status != "pending" {
		return nil, errors.New("already opened")
	}

	today := startOfTashkentDay(time.Now())
	tomorrow := today.AddDate(0, 0, 1)
	available := startOfTashkentDay(prize.AvailableOn)
	if !available.Before(tomorrow) || available.Add(24*time.Hour).Before(today) {
		// available_on must be today (Tashkent).
		if !available.Equal(today) {
			return nil, errors.New("not available today")
		}
	}

	out := SpinPrize()

	now := time.Now()
	updates := map[string]interface{}{
		"status":      "opened",
		"prize_type":  out.Type,
		"prize_value": out.Value,
		"opened_at":   now,
	}
	if err := database.DB.Model(&prize).Updates(updates).Error; err != nil {
		return nil, err
	}

	// Apply the prize.
	switch out.Type {
	case "minutes":
		// Minute prizes are credited via the daily-limit bonus tracker.
		// The handler sums recently-opened minute prizes and adds them to
		// `minutes_limit` for the next 24 h. No work to do here besides
		// the row update we just performed above.
	case "premium":
		expiry := now.AddDate(0, 0, out.Value)
		database.DB.Model(&models.User{}).
			Where("id = ?", uid).
			Updates(map[string]interface{}{
				"is_premium":         true,
				"premium_expires_at": expiry,
			})
	case "discount":
		// Discounts are surfaced via a separate endpoint at payment time;
		// store the most recent percentage as a Redis key with 30d TTL.
		// Keeping it in Redis keeps the payments path zero-coupling.
		// (Implemented as part of the payments overhaul if/when needed.)
	}

	return &out, nil
}
