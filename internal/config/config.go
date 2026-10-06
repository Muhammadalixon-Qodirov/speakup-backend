package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

// Config holds all application configuration.
// All values are loaded from environment variables (.env file).
type Config struct {
	// App
	AppEnv    string
	SecretKey string
	Debug     bool
	Port      string

	// Database
	DatabaseURL         string
	DatabasePoolSize    int
	DatabaseMaxOverflow int

	// Redis
	RedisURL string

	// Telegram
	BotToken      string
	BotUsername   string
	WebhookURL    string
	WebhookSecret string
	MiniAppURL    string

	// Mandatory channel subscription ("force subscribe") gate.
	// RequiredChannel is stored without the leading "@".
	RequiredChannel    string
	ChannelGateEnabled bool

	// JWT
	JWTSecretKey  string
	JWTAlgorithm  string
	JWTExpireDays int

	// Daily Limit
	FreeDailyLimitMinutes int

	// Pronunciation trainer, free-tier limits. The sidecar runs on 2 CPU
	// cores it shares with live voice rooms, so free usage is capped to keep
	// those cores free - this is also what premium unlocks.
	//   PronFreeDailyAttempts: checks per rolling 24h for a free user.
	//   PronFreeMaxSentences:  longest passage a free user may request (1-4).
	// Premium users bypass both. 0 attempts means "no limit even for free".
	PronFreeDailyAttempts int
	PronFreeMaxSentences  int

	// Payments - Payme
	PaymeMerchantID    string
	PaymeSecretKey     string
	PaymeTestSecretKey string
	PaymeIsTest        bool

	// Payments - Click
	ClickServiceID  string
	ClickMerchantID string
	ClickSecretKey  string

	// Premium price
	PremiumPriceUZS   int
	PremiumPriceTiyin int

	// CORS
	AllowedOrigins []string

	// AI (Groq)
	GroqAPIKey       string
	GroqLLMAPIKey    string
	GroqWhisperModel string
	GroqLLMModel     string

	// Encryption
	EncryptionMasterKey string // 64 hex chars (32 bytes) for AES-256-GCM

	// TURN / coturn
	TurnHost             string
	TurnStaticAuthSecret string

	// UploadDir is where user-supplied files (currently only partner
	// centre logos) are written. Must be a Docker volume in production,
	// otherwise every redeploy wipes the logos.
	UploadDir string

	// TalaffuzURL is the pronunciation sidecar (phoneme recogniser + TTS).
	// It listens only on the internal Docker network - no host port - so the
	// default is the compose service name. Empty disables the feature, which
	// is what a dev box without the sidecar running wants.
	TalaffuzURL string

	// Operations: comma-separated Telegram chat IDs that should receive
	// critical alerts (panics, scheduler failures, low disk, etc.). Set
	// via ADMIN_ALERT_CHAT_IDS env var. The first ID can be your own
	// chat with the bot, additional IDs let you fan out to a team.
	AdminAlertChatIDs []int64
}

// Global app config - loaded once at startup, used everywhere
var App *Config

// Load reads .env file and populates the global Config.
func Load() {
	if err := godotenv.Load(); err != nil {
		log.Warn().Msg(".env file not found, using system env vars")
	}

	App = &Config{
		// App
		AppEnv:    getEnv("APP_ENV", "development"),
		SecretKey: getEnv("APP_SECRET_KEY", "change-me-in-production-min-32-chars"),
		Debug:     getEnvBool("DEBUG", true),
		Port:      getEnv("PORT", "8000"),

		// Database
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://speak-up:speak-up_secret@localhost:5432/speak-up_db?sslmode=disable"),
		DatabasePoolSize:    getEnvInt("DATABASE_POOL_SIZE", 20),
		DatabaseMaxOverflow: getEnvInt("DATABASE_MAX_OVERFLOW", 10),

		// Redis
		RedisURL: getEnv("REDIS_URL", "redis://localhost:6379/0"),

		// Telegram
		BotToken:      getEnv("BOT_TOKEN", ""),
		BotUsername:   getEnv("BOT_USERNAME", "speak-up_bot"),
		WebhookURL:    getEnv("WEBHOOK_URL", ""),
		WebhookSecret: getEnv("TELEGRAM_WEBHOOK_SECRET", ""),
		MiniAppURL:    getEnv("MINI_APP_URL", "https://speak-up.uz"),

		// JWT
		JWTSecretKey:  getEnv("JWT_SECRET_KEY", "change-me-jwt-secret-key"),
		JWTAlgorithm:  getEnv("JWT_ALGORITHM", "HS256"),
		JWTExpireDays: getEnvInt("JWT_EXPIRE_DAYS", 30),

		// Daily Limit
		FreeDailyLimitMinutes: getEnvInt("FREE_DAILY_LIMIT_MINUTES", 10),

		// Pronunciation trainer free-tier caps (premium bypasses both).
		PronFreeDailyAttempts: getEnvInt("PRON_FREE_DAILY_ATTEMPTS", 10),
		PronFreeMaxSentences:  getEnvInt("PRON_FREE_MAX_SENTENCES", 2),

		// Mandatory channel subscription gate. Set CHANNEL_GATE_ENABLED=false
		// to switch the whole gate off without a redeploy.
		RequiredChannel:    strings.TrimPrefix(getEnv("REQUIRED_CHANNEL", "speakup_app"), "@"),
		ChannelGateEnabled: getEnvBool("CHANNEL_GATE_ENABLED", true),

		// Payments - Payme
		PaymeMerchantID:    getEnv("PAYME_MERCHANT_ID", ""),
		PaymeSecretKey:     getEnv("PAYME_SECRET_KEY", ""),
		PaymeTestSecretKey: getEnv("PAYME_TEST_SECRET_KEY", ""),
		PaymeIsTest:        getEnvBool("PAYME_IS_TEST", true),

		// Payments - Click
		ClickServiceID:  getEnv("CLICK_SERVICE_ID", ""),
		ClickMerchantID: getEnv("CLICK_MERCHANT_ID", ""),
		ClickSecretKey:  getEnv("CLICK_SECRET_KEY", ""),

		// Premium price
		PremiumPriceUZS:   getEnvInt("PREMIUM_PRICE_UZS", 30000),
		PremiumPriceTiyin: getEnvInt("PREMIUM_PRICE_TIYIN", 3000000),

		// CORS
		AllowedOrigins: strings.Split(
			getEnv("ALLOWED_ORIGINS", "http://localhost:3000"),
			",",
		),

		// AI (Groq)
		GroqAPIKey:       getEnv("GROQ_API_KEY", ""),
		GroqLLMAPIKey:    getEnv("GROQ_LLM_API_KEY", ""),
		GroqWhisperModel: getEnv("GROQ_WHISPER_MODEL", "whisper-large-v3"),
		GroqLLMModel:     getEnv("GROQ_LLM_MODEL", "openai/gpt-oss-120b"),

		// Encryption
		EncryptionMasterKey: getEnv("ENCRYPTION_MASTER_KEY", ""),

		// TURN / coturn
		TurnHost:             getEnv("TURN_HOST", ""),
		TurnStaticAuthSecret: getEnv("TURN_STATIC_AUTH_SECRET", ""),

		// Uploads
		UploadDir: getEnv("UPLOAD_DIR", "/data/uploads"),

		TalaffuzURL: getEnv("TALAFFUZ_URL", "http://talaffuz:7880"),

		// Ops
		AdminAlertChatIDs: getEnvInt64List("ADMIN_ALERT_CHAT_IDS"),
	}

	// Validate critical configuration at startup. A missing JWT secret or
	// wide-open CORS in production can silently expose the whole system.
	App.validate()

	log.Info().
		Str("env", App.AppEnv).
		Str("port", App.Port).
		Bool("debug", App.Debug).
		Msg("Config loaded")
}

// validate checks critical config values and logs warnings or fatals.
func (c *Config) validate() {
	var warnings []string
	var fatals []string

	// JWT secret
	if c.JWTSecretKey == "" {
		fatals = append(fatals, "JWT_SECRET_KEY is empty")
	} else if isWeakSecret(c.JWTSecretKey) {
		if c.IsProduction() {
			fatals = append(fatals, "JWT_SECRET_KEY uses a default/weak value in production")
		} else {
			warnings = append(warnings, "JWT_SECRET_KEY uses a default/weak value")
		}
	}

	// Bot token
	if c.BotToken == "" {
		warnings = append(warnings, "BOT_TOKEN is empty - bot features disabled")
	}

	// Webhook secret. FATAL in production, and deliberately so.
	//
	// The webhook middleware used to fail OPEN when this was unset -
	// every request to /bot/webhook was accepted. Because nothing broke,
	// the missing secret went unnoticed for weeks, and during that time
	// anyone could POST a forged Telegram update. `handleAuthStart`
	// trusts the sender ID in that payload to complete a web login, so a
	// forged update was a full account takeover with nothing but the
	// victim's Telegram ID.
	//
	// A silent warning was not enough to prevent that. Refusing to boot is.
	if c.IsProduction() && c.BotToken != "" && c.WebhookSecret == "" {
		fatals = append(fatals,
			"TELEGRAM_WEBHOOK_SECRET is empty in production - /bot/webhook would accept forged updates (account takeover)")
	}

	// CORS wildcard in production
	if c.IsProduction() {
		for _, o := range c.AllowedOrigins {
			if strings.TrimSpace(o) == "*" {
				warnings = append(warnings, "ALLOWED_ORIGINS contains '*' in production - consider restricting")
				break
			}
		}
	}

	// Database
	if c.DatabaseURL == "" {
		fatals = append(fatals, "DATABASE_URL is empty")
	}

	// Redis
	if c.RedisURL == "" {
		fatals = append(fatals, "REDIS_URL is empty")
	}

	for _, w := range warnings {
		log.Warn().Msg("Config: " + w)
	}
	if len(fatals) > 0 {
		for _, f := range fatals {
			log.Error().Msg("Config FATAL: " + f)
		}
		log.Fatal().Msg("Configuration validation failed - fix the above errors and restart")
	}
}

// isWeakSecret returns true for default or well-known weak secret values.
func isWeakSecret(s string) bool {
	weak := []string{
		"secret", "dev-secret", "dev_secret", "change-me",
		"changeme", "test", "jwt-secret", "your-secret-here",
		"supersecret", "change-me-jwt-secret-key",
		"change-me-in-production-min-32-chars",
	}
	lower := strings.ToLower(s)
	for _, w := range weak {
		if lower == w {
			return true
		}
	}
	return false
}

// IsProduction returns true if running in production environment.
func (c *Config) IsProduction() bool {
	return c.AppEnv == "production"
}

// --- Helper functions ---

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if val := os.Getenv(key); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return fallback
}

// getEnvInt64List parses a comma-separated list of int64s. Empty entries
// and parse failures are skipped silently - easier to misconfigure than
// to panic the boot path.
func getEnvInt64List(key string) []int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}
