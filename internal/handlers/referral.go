package handlers

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/models"
	"github.com/speak-up/backend/internal/utils"
)

// ReferralInfo describes the current user's referral state.
//
// Flow:
//   1. Each user has a short, unguessable `ReferralCode`.
//   2. When someone clicks `https://t.me/<bot>?startapp=ref_<code>` and the
//      Mini App opens, Telegram forwards `start_param=ref_<code>`.
//   3. On first login we attach `referred_by = referrer.id` to the new user.
//   4. For every referee whose account was created in the last 24 h we
//      credit the referrer with +5 minutes of speaking time (capped at
//      3 referees = +15 min). The bonus expires with that rolling window
//      and automatically rolls forward as old referees age out.
type ReferralInfo struct {
	Code string `json:"code"`
	Link string `json:"link"`

	// Number of referrals that count toward today's bonus (recent window).
	ActiveReferrals int `json:"active_referrals"`
	// Lifetime count - used for the "you've invited N friends" stat.
	TotalReferrals int `json:"total_referrals"`
	// Minutes credited to the daily limit right now.
	BonusMinutes int `json:"bonus_minutes"`
	// Per-referral bonus (sent so the UI can render "+2 min" labels).
	BonusPerReferral int `json:"bonus_per_referral"`
}

const (
	// Each successful referral that's still in the rolling 24h window
	// adds this many bonus minutes to the referrer's daily allowance.
	referralBonusMinutes = 2
	// No upper cap - invite as many friends as you like and stack the bonus.
	referralWindowHours = 24
)

// CountActiveReferrals returns how many users this referrer invited within
// the last 24 h. There's no cap - every successful referral contributes
// `referralBonusMinutes` minutes for the next 24 h.
func CountActiveReferrals(referrerID string) int {
	var n int64
	cutoff := time.Now().Add(-time.Duration(referralWindowHours) * time.Hour)
	database.DB.Model(&models.User{}).
		Where("referred_by = ? AND created_at >= ?", referrerID, cutoff).
		Count(&n)
	return int(n)
}

// GetMyReferral handles GET /users/me/referral.
func GetMyReferral(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	// Backfill a code in case the user was created before we added the field.
	if user.ReferralCode == "" {
		user.ReferralCode = models.GenerateReferralCode()
		database.DB.Model(user).Update("referral_code", user.ReferralCode)
	}

	// Lifetime count
	var totalReferrals int64
	database.DB.Model(&models.User{}).
		Where("referred_by = ?", user.ID).
		Count(&totalReferrals)

	active := CountActiveReferrals(user.ID.String())
	bonusMinutes := active * referralBonusMinutes

	// Use `?start=ref_X` (not `?startapp=`) so the invited user lands on the
	// bot's Start screen first. When they tap Start, the bot stores the
	// referral code against their TG ID; the Mini App then consumes it on
	// first signup via UpsertUserWithRef.
	botUsername := config.App.BotUsername
	link := fmt.Sprintf(
		"https://t.me/%s?start=ref_%s",
		botUsername,
		user.ReferralCode,
	)

	return utils.Success(c, ReferralInfo{
		Code:             user.ReferralCode,
		Link:             link,
		ActiveReferrals:  active,
		TotalReferrals:   int(totalReferrals),
		BonusMinutes:     bonusMinutes,
		BonusPerReferral: referralBonusMinutes,
	})
}
