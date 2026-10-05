package services

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
	"gorm.io/gorm"
)

// TelegramUser represents the user data extracted from Telegram initData.
type TelegramUser struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	IsPremium    bool   `json:"is_premium"`
	PhotoURL     string `json:"photo_url"`
}

// VerifyTelegramInitData verifies Telegram WebApp initData using HMAC-SHA256.
// Returns parsed TelegramUser if valid.
// Deprecated: prefer VerifyTelegramInitDataWithParam which also returns the
// start_param carried alongside the init data.
func VerifyTelegramInitData(initData string) (*TelegramUser, error) {
	u, _, err := VerifyTelegramInitDataWithParam(initData)
	return u, err
}

// VerifyTelegramInitDataWithParam verifies Telegram WebApp initData and
// returns the authenticated user along with the `start_param` value (empty
// string when none was supplied). start_param is what Telegram includes
// when you open the Mini App via `t.me/bot?startapp=VALUE`.
func VerifyTelegramInitDataWithParam(initData string) (*TelegramUser, string, error) {
	parsed, err := url.ParseQuery(initData)
	if err != nil {
		return nil, "", errors.New("invalid init data format")
	}

	receivedHash := parsed.Get("hash")
	if receivedHash == "" {
		return nil, "", errors.New("missing hash")
	}

	// Build check string - all params except hash, sorted alphabetically
	var params []string
	for k, v := range parsed {
		if k != "hash" {
			params = append(params, fmt.Sprintf("%s=%s", k, v[0]))
		}
	}
	sort.Strings(params)
	checkString := strings.Join(params, "\n")

	// Create secret key: HMAC-SHA256("WebAppData", bot_token)
	secretKeyMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretKeyMAC.Write([]byte(config.App.BotToken))
	secretKey := secretKeyMAC.Sum(nil)

	// Calculate expected hash
	expectedMAC := hmac.New(sha256.New, secretKey)
	expectedMAC.Write([]byte(checkString))
	expectedHash := hex.EncodeToString(expectedMAC.Sum(nil))

	if !hmac.Equal([]byte(expectedHash), []byte(receivedHash)) {
		return nil, "", errors.New("invalid hash")
	}

	// Check auth_date - always enforced. Old initData payloads must
	// not be replayable against ANY environment. 1 hour tolerance is
	// generous enough for laggy networks but tight enough that a
	// captured initData can't be reused indefinitely.
	authDate, err := strconv.ParseInt(parsed.Get("auth_date"), 10, 64)
	if err != nil || authDate == 0 {
		return nil, "", errors.New("missing auth_date")
	}
	if time.Now().Unix()-authDate > 3600 {
		return nil, "", errors.New("auth data expired")
	}

	// Parse user JSON
	userStr := parsed.Get("user")
	if userStr == "" {
		return nil, "", errors.New("missing user data")
	}

	var tgUser TelegramUser
	if err := json.Unmarshal([]byte(userStr), &tgUser); err != nil {
		return nil, "", errors.New("invalid user data")
	}

	startParam := parsed.Get("start_param")
	return &tgUser, startParam, nil
}

// UpsertUser creates a new user or updates existing one from Telegram data.
// Deprecated: prefer UpsertUserWithRef which also honours referral links.
func UpsertUser(tgUser *TelegramUser) (*models.User, error) {
	return UpsertUserWithRef(tgUser, "")
}

// UpsertUserWithRef is like UpsertUser but attaches a referrer when this is
// the first time we see the user AND `startParam` decodes to a valid
// referral code (`ref_CODE`). Subsequent logins never overwrite
// `referred_by` - the attribution is frozen at signup.
func UpsertUserWithRef(tgUser *TelegramUser, startParam string) (*models.User, error) {
	var user models.User
	err := database.DB.Where("telegram_id = ?", tgUser.ID).First(&user).Error

	// Backfill a referral code for legacy rows on any login path.
	backfillCode := func(u *models.User) {
		if u.ReferralCode == "" {
			u.ReferralCode = models.GenerateReferralCode()
			database.DB.Model(u).Update("referral_code", u.ReferralCode)
		}
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		user = models.User{
			TelegramID:   tgUser.ID,
			FirstName:    tgUser.FirstName,
			LastName:     strPtr(tgUser.LastName),
			Username:     strPtr(tgUser.Username),
			LanguageCode: strPtr(tgUser.LanguageCode),
			IsTgPremium:  tgUser.IsPremium,
			PhotoURL:     strPtr(tgUser.PhotoURL),
		}

		// 1. Try the start_param passed via initData (Mini App deep link).
		code := parseRefCode(startParam)
		// 2. Fall back to a pending referral stored by the bot's /start
		//    handler - used when the user followed t.me/bot?start=ref_X,
		//    pressed Start, and only then opened the Mini App.
		if code == "" && PendingReferralLookup != nil {
			code = PendingReferralLookup(tgUser.ID)
		}

		if code != "" {
			var referrer models.User
			if err := database.DB.
				Where("referral_code = ?", code).
				First(&referrer).Error; err == nil && referrer.ID != uuid.Nil {
				// Never self-refer.
				if referrer.TelegramID != tgUser.ID {
					referrerID := referrer.ID
					user.ReferredBy = &referrerID
				}
			}
		}

		if err := database.DB.Create(&user).Error; err != nil {
			return nil, err
		}
		return &user, nil
	}

	if err != nil {
		return nil, err
	}

	backfillCode(&user)

	updates := map[string]interface{}{
		"first_name":    tgUser.FirstName,
		"last_name":     strPtr(tgUser.LastName),
		"username":      strPtr(tgUser.Username),
		"photo_url":     strPtr(tgUser.PhotoURL),
		"is_tg_premium": tgUser.IsPremium,
	}
	if err := database.DB.Model(&user).Updates(updates).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// parseRefCode extracts the referral code out of a Telegram start_param.
// Accepts "ref_CODE" or "CODE" (legacy short form). Empty string if neither.
func parseRefCode(startParam string) string {
	s := strings.TrimSpace(startParam)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "ref_") {
		return strings.ToUpper(strings.TrimPrefix(s, "ref_"))
	}
	// Other namespaced start_params are not referral codes. Without this
	// guard a room invite (`room_ABC123`) would be uppercased and looked
	// up in the referral table - harmless today because it never matches,
	// but it would silently attribute a signup the moment a referral code
	// happened to collide.
	if strings.Contains(s, "_") {
		return ""
	}
	// Plain code (no prefix) - accept it as-is.
	return strings.ToUpper(s)
}

// PendingReferralLookup is injected at startup from the bot package to
// break the would-be import cycle services → bot. It returns the referral
// code stored earlier for a given telegram ID (and deletes it on read).
var PendingReferralLookup func(tgID int64) string

// AuthSource identifies how a JWT was minted.
// "miniapp" → Telegram Mini App (initData flow)
// "web"     → Browser / bot OTP flow
// ""        → Legacy token minted before src claim was added
const (
	AuthSourceMiniApp = "miniapp"
	AuthSourceWeb     = "web"
)

// CreateJWT generates a JWT token for the given user ID.
// Deprecated: prefer CreateJWTWithSource so the auth source is recorded.
func CreateJWT(userID uuid.UUID) (string, error) {
	return CreateJWTWithSource(userID, "")
}

// CreateJWTWithSource mints a JWT with an `src` claim recording whether the
// user authenticated via Telegram Mini App or via the web/bot OTP flow.
// The claim is signed alongside `sub` and cannot be tampered with.
func CreateJWTWithSource(userID uuid.UUID, src string) (string, error) {
	claims := jwt.MapClaims{
		"sub": userID.String(),
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Duration(config.App.JWTExpireDays) * 24 * time.Hour).Unix(),
	}
	if src != "" {
		claims["src"] = src
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(config.App.JWTSecretKey))
}

// DecodeJWT verifies and extracts user ID from a JWT token.
func DecodeJWT(tokenString string) (string, error) {
	sub, _, err := DecodeJWTWithSource(tokenString)
	return sub, err
}

// DecodeJWTWithSource returns both the user id and the `src` claim (auth source).
// `src` may be empty for tokens minted before the claim was introduced.
func DecodeJWTWithSource(tokenString string) (string, string, error) {
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(config.App.JWTSecretKey), nil
	})

	if err != nil || !token.Valid {
		return "", "", errors.New("invalid token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", errors.New("invalid claims")
	}

	sub, ok := claims["sub"].(string)
	if !ok {
		return "", "", errors.New("invalid subject")
	}

	src, _ := claims["src"].(string)
	return sub, src, nil
}

// GetUserByID finds a user by UUID.
// GetUserByTelegramID resolves a Telegram account to our own user row.
// Used by the paths that only ever learn about a person through Telegram
// - webhook pushes, for instance - and need the internal ID to reach
// them over the socket.
func GetUserByTelegramID(tgID int64) (*models.User, error) {
	var user models.User
	if err := database.DB.Where("telegram_id = ?", tgID).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByID(id uuid.UUID) (*models.User, error) {
	var user models.User
	if err := database.DB.First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// VerifyTelegramWidgetData verifies data from Telegram Login Widget.
// Widget hash algorithm differs from WebApp initData:
//
//	secret_key = SHA256(bot_token)   ← plain SHA256, not HMAC
//	hash       = HMAC-SHA256(secret_key, data_check_string)
//
// Frontend sends: id, first_name, last_name, username, photo_url, auth_date, hash
func VerifyTelegramWidgetData(data map[string]string) (*TelegramUser, error) {
	receivedHash, ok := data["hash"]
	if !ok || receivedHash == "" {
		return nil, errors.New("missing hash")
	}

	// Build check string - all fields except hash, sorted alphabetically
	var params []string
	for k, v := range data {
		if k != "hash" {
			params = append(params, fmt.Sprintf("%s=%s", k, v))
		}
	}
	sort.Strings(params)
	checkString := strings.Join(params, "\n")

	// secret_key = SHA256(bot_token)  ← Widget uses plain SHA256, not HMAC
	h := sha256.New()
	h.Write([]byte(config.App.BotToken))
	secretKey := h.Sum(nil)

	// expected_hash = HMAC-SHA256(secret_key, check_string)
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(checkString))
	expectedHash := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(expectedHash), []byte(receivedHash)) {
		return nil, errors.New("invalid hash")
	}

	// Reject auth_date older than 24 hours
	authDate, _ := strconv.ParseInt(data["auth_date"], 10, 64)
	if time.Now().Unix()-authDate > 86400 {
		return nil, errors.New("auth data expired")
	}

	id, err := strconv.ParseInt(data["id"], 10, 64)
	if err != nil || id == 0 {
		return nil, errors.New("invalid user id")
	}

	return &TelegramUser{
		ID:        id,
		FirstName: data["first_name"],
		LastName:  data["last_name"],
		Username:  data["username"],
		PhotoURL:  data["photo_url"],
	}, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
