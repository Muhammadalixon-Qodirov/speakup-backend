package handlers

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/middleware"
	"github.com/speak-up/backend/internal/utils"
)

// IceServer mirrors the browser RTCIceServer shape.
type IceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// IceConfigResponse is what the frontend receives from GET /users/ice-config.
type IceConfigResponse struct {
	IceServers          []IceServer `json:"ice_servers"`
	Username            string      `json:"username"`
	Credential          string      `json:"credential"`
	TTLSeconds          int64       `json:"ttl_seconds"`
	IceCandidatePoolSize int        `json:"ice_candidate_pool_size"`
}

// GetIceConfig handles GET /users/ice-config.
//
// Returns time-limited REST-style credentials for coturn so the frontend
// never ships a hardcoded password. The coturn server must be started with
// `use-auth-secret` + `static-auth-secret=<same secret>`.
//
// Algorithm (RFC-ish / coturn convention):
//   username   = "<unix_expiry>:<user_id>"
//   credential = base64(HMAC-SHA1(secret, username))
//   ttl        = 3600s (1 hour)
//
// Coturn validates the HMAC + rejects if `unix_expiry` is in the past.
func GetIceConfig(c *fiber.Ctx) error {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		return utils.Unauthorized(c, "Not authenticated")
	}

	secret := config.App.TurnStaticAuthSecret
	host := config.App.TurnHost

	if secret == "" || host == "" {
		// Fail-safe: return STUN-only config so audio still has a chance.
		return utils.Success(c, IceConfigResponse{
			IceServers: []IceServer{
				{URLs: []string{"stun:stun.l.google.com:19302"}},
				{URLs: []string{"stun:stun1.l.google.com:19302"}},
			},
		})
	}

	const ttl int64 = 3600 // 1 hour
	expiry := time.Now().Unix() + ttl
	username := fmt.Sprintf("%d:%s", expiry, user.ID.String())

	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	credential := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	host = strings.TrimSpace(host)
	stunURL := fmt.Sprintf("stun:%s:3478", host)
	turnUDP := fmt.Sprintf("turn:%s:3478?transport=udp", host)
	turnTCP := fmt.Sprintf("turn:%s:3478?transport=tcp", host)

	return utils.Success(c, IceConfigResponse{
		IceServers: []IceServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
			{URLs: []string{stunURL}},
			{
				URLs:       []string{turnUDP, turnTCP},
				Username:   username,
				Credential: credential,
			},
		},
		Username:             username,
		Credential:           credential,
		TTLSeconds:           ttl,
		IceCandidatePoolSize: 4,
	})
}
