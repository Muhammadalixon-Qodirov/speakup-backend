package services

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
)

const otpSessionTTL = 10 * time.Minute

// OTPSessionResult is returned when a new session is created.
type OTPSessionResult struct {
	SessionToken string `json:"session_token"`
	BotURL       string `json:"bot_url"`
}

// GenerateOTPSession creates a pending auth session and returns the bot URL.
// No phone or password needed - Telegram bot handles everything.
func GenerateOTPSession() (*OTPSessionResult, error) {
	// 28 bytes → 56 hex chars → "auth_" + 56 = 61 chars (Telegram limit: 64) ✅
	raw := make([]byte, 28)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("token yaratishda xatolik: %w", err)
	}
	token := hex.EncodeToString(raw)

	session := models.AuthOTPSession{
		SessionToken: token,
		Status:       "pending",
		ExpiresAt:    time.Now().Add(otpSessionTTL),
	}
	if err := database.DB.Create(&session).Error; err != nil {
		return nil, fmt.Errorf("sessiya saqlashda xatolik: %w", err)
	}

	botURL := fmt.Sprintf("https://t.me/%s?start=auth_%s", config.App.BotUsername, token)
	return &OTPSessionResult{SessionToken: token, BotURL: botURL}, nil
}

// CheckOTPSessionResult is the full response for check-session.
type CheckOTPSessionResult struct {
	Status string       `json:"status"`
	Token  string       `json:"token,omitempty"`
	User   *models.User `json:"user,omitempty"`
}

// CheckOTPSession returns session status.
// If completed → generates JWT and returns user.
func CheckOTPSession(sessionToken string) (*CheckOTPSessionResult, error) {
	var session models.AuthOTPSession
	err := database.DB.Where("session_token = ?", sessionToken).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("Sessiya topilmadi")
	}
	if err != nil {
		return nil, err
	}
	if session.IsExpired() {
		return nil, errors.New("Sessiya muddati tugagan")
	}

	if session.Status == "pending" {
		return &CheckOTPSessionResult{Status: "pending"}, nil
	}

	if session.Status == "completed" {
		// Find user by telegram_id
		var user models.User
		err := database.DB.Where("telegram_id = ?", session.TelegramID).First(&user).Error
		if err != nil {
			return nil, errors.New("Foydalanuvchi topilmadi")
		}

		// Generate JWT - tag as web (browser / bot OTP) login.
		jwt, err := CreateJWTWithSource(user.ID, AuthSourceWeb)
		if err != nil {
			return nil, errors.New("Token yaratishda xatolik")
		}

		// Mark session as used (one-time)
		database.DB.Model(&session).UpdateColumn("status", "used")

		return &CheckOTPSessionResult{
			Status: "completed",
			Token:  jwt,
			User:   &user,
		}, nil
	}

	return nil, errors.New("Sessiya allaqachon ishlatilgan")
}

// LevelsInGroup returns A1-C2 levels for a group name.
func LevelsInGroup(group string) []string {
	switch group {
	case "basic":
		return []string{"A1", "A2"}
	case "independent":
		return []string{"B1", "B2"}
	case "proficient":
		return []string{"C1", "C2"}
	default:
		return []string{"A1", "A2", "B1", "B2", "C1", "C2"}
	}
}
