package services

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/speak-up/backend/internal/bot"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/crypto"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// globalEncryptor is lazily initialised from ENCRYPTION_MASTER_KEY.
var (
	globalEncryptor     *crypto.Encryptor
	globalEncryptorOnce sync.Once
)

func getEncryptor() *crypto.Encryptor {
	globalEncryptorOnce.Do(func() {
		if config.App.EncryptionMasterKey == "" {
			return
		}
		enc, err := crypto.NewEncryptor(config.App.EncryptionMasterKey)
		if err != nil {
			log.Error().Err(err).Msg("Failed to init Groq key encryptor")
			return
		}
		globalEncryptor = enc
	})
	return globalEncryptor
}

var keyMu sync.Mutex

// GetActiveKey returns the best available API key for the given type.
// Priority: lowest priority number, not rate-limited, is_active.
// If all DB keys are exhausted, falls back to .env key.
func GetActiveKey(keyType string) (string, *uuid.UUID) {
	return GetActiveKeyRR(keyType)
}

// RecordKeyUsage marks a successful use of a key.
func RecordKeyUsage(keyID *uuid.UUID) {
	if keyID == nil {
		return
	}
	now := time.Now()
	database.DB.Model(&models.GroqAPIKey{}).
		Where("id = ?", *keyID).
		Updates(map[string]interface{}{
			"total_requests": database.DB.Raw("total_requests + 1"),
			"last_used_at":   now,
		})
}

// RecordKeyError marks a failed use and handles rate limiting.
func RecordKeyError(keyID *uuid.UUID, errMsg string, isRateLimit bool) {
	if keyID == nil {
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"failed_requests": database.DB.Raw("failed_requests + 1"),
		"last_error_at":   now,
		"last_error":      truncate(errMsg, 500),
	}

	if isRateLimit {
		// Rate limited - disable for 60 seconds
		cooldown := now.Add(60 * time.Second)
		updates["is_rate_limited"] = true
		updates["rate_limited_until"] = cooldown
		log.Warn().Str("key_id", keyID.String()).Msg("Groq key rate limited - switching to next")
	}

	database.DB.Model(&models.GroqAPIKey{}).Where("id = ?", *keyID).Updates(updates)

	// Check if ALL keys are now rate-limited or dead
	go checkAllKeysHealth()
}

// checkAllKeysHealth alerts admin if no working keys remain.
func checkAllKeysHealth() {
	var activeCount int64
	database.DB.Model(&models.GroqAPIKey{}).
		Where("is_active = ? AND (is_rate_limited = ? OR rate_limited_until < ?)", true, false, time.Now()).
		Count(&activeCount)

	if activeCount == 0 {
		// Check .env fallback
		if config.App.GroqAPIKey == "" {
			log.Error().Msg("ALL Groq API keys exhausted! AI features disabled.")
			bot.AlertAdmin("🚨 Groq API keys tugadi!", "Barcha keylar rate-limited yoki o'chirilgan.\nAdmin paneldan yangi key qo'shing.\n\n/admin → AI Keys")
		}
	}
}

// decryptKeyIfNeeded returns the plain-text API key. If the key is
// stored encrypted (encryption_version == 1), it uses the global
// encryptor. Legacy plain-text keys (version 0) are returned as-is.
func decryptKeyIfNeeded(k *models.GroqAPIKey) (string, error) {
	switch k.EncryptionVersion {
	case 0:
		return k.Key, nil
	case 1:
		enc := getEncryptor()
		if enc == nil {
			return "", fmt.Errorf("no encryptor configured but key %s is encrypted", k.ID)
		}
		return enc.Decrypt(k.EncryptedKey)
	default:
		return "", fmt.Errorf("unsupported encryption version %d", k.EncryptionVersion)
	}
}

// MigratePlainKeysToEncrypted encrypts all version-0 keys in the DB.
// Safe to call repeatedly - already-encrypted keys are skipped.
func MigratePlainKeysToEncrypted() (int, error) {
	enc := getEncryptor()
	if enc == nil {
		return 0, nil // no master key configured - nothing to do
	}

	var plainKeys []models.GroqAPIKey
	if err := database.DB.
		Where("encryption_version = 0 AND key IS NOT NULL AND key != ''").
		Find(&plainKeys).Error; err != nil {
		return 0, err
	}

	migrated := 0
	for _, k := range plainKeys {
		encrypted, err := enc.Encrypt(k.Key)
		if err != nil {
			log.Warn().Err(err).Str("key_id", k.ID.String()).Msg("failed to encrypt key, skipping")
			continue
		}

		// Verify the round trip BEFORE trusting the ciphertext. The next
		// statement erases the only plaintext copy we have; if encryption
		// or the master key were subtly wrong, skipping this check would
		// destroy the key with no way back.
		if back, derr := enc.Decrypt(encrypted); derr != nil || back != k.Key {
			log.Error().Err(derr).Str("key_id", k.ID.String()).
				Msg("encrypt/decrypt round trip failed - leaving key as plaintext")
			continue
		}

		// Clear the plaintext column in the same update. Writing the
		// ciphertext while leaving `key` populated would encrypt nothing
		// in practice - the plaintext would still be sitting in the row,
		// in every backup, for anyone who reached the database.
		// decryptKeyIfNeeded reads `encrypted_key` once version is 1, so
		// nothing depends on `key` from here on.
		if err := database.DB.Model(&k).Updates(map[string]interface{}{
			"encrypted_key":      encrypted,
			"encryption_version": 1,
			"key":                "",
		}).Error; err != nil {
			log.Warn().Err(err).Str("key_id", k.ID.String()).Msg("failed to update encrypted key")
			continue
		}
		migrated++
	}

	if migrated > 0 {
		InvalidateKeyCache()
	}
	return migrated, nil
}

// AddGroqKey adds a new API key to the database. When a master
// encryption key is configured, the key is stored encrypted (version 1).
func AddGroqKey(key, label, keyType string, priority int) (*models.GroqAPIKey, error) {
	if key == "" {
		return nil, fmt.Errorf("key cannot be empty")
	}

	// Check duplicate
	var existing models.GroqAPIKey
	if err := database.DB.Where("key = ?", key).First(&existing).Error; err == nil {
		return nil, fmt.Errorf("this key already exists")
	}

	newKey := &models.GroqAPIKey{
		Key:      key,
		Label:    label,
		KeyType:  keyType,
		IsActive: true,
		Priority: priority,
	}

	// Encrypt if master key is available.
	if enc := getEncryptor(); enc != nil {
		encrypted, err := enc.Encrypt(key)
		if err == nil {
			newKey.EncryptedKey = encrypted
			newKey.EncryptionVersion = 1
		}
	}

	if err := database.DB.Create(newKey).Error; err != nil {
		return nil, err
	}

	newKey.KeyMasked = newKey.MaskKey()
	return newKey, nil
}

// ListGroqKeys returns all keys with masked values.
func ListGroqKeys() ([]models.GroqAPIKey, error) {
	var keys []models.GroqAPIKey
	if err := database.DB.Order("priority ASC, created_at ASC").Find(&keys).Error; err != nil {
		return nil, err
	}
	for i := range keys {
		keys[i].KeyMasked = keys[i].MaskKey()
	}
	return keys, nil
}

// DeleteGroqKey removes a key.
func DeleteGroqKey(id uuid.UUID) error {
	result := database.DB.Where("id = ?", id).Delete(&models.GroqAPIKey{})
	if result.RowsAffected == 0 {
		return fmt.Errorf("key not found")
	}
	return nil
}

// ToggleGroqKey enables/disables a key.
func ToggleGroqKey(id uuid.UUID) (*models.GroqAPIKey, error) {
	var key models.GroqAPIKey
	if err := database.DB.First(&key, "id = ?", id).Error; err != nil {
		return nil, err
	}

	key.IsActive = !key.IsActive
	database.DB.Model(&key).Update("is_active", key.IsActive)
	key.KeyMasked = key.MaskKey()
	return &key, nil
}

// ResetKeyRateLimit manually clears rate limit on a key.
func ResetKeyRateLimit(id uuid.UUID) error {
	return database.DB.Model(&models.GroqAPIKey{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"is_rate_limited":    false,
			"rate_limited_until": nil,
		}).Error
}

// GetKeyStats returns summary stats about all keys.
func GetKeyStats() map[string]interface{} {
	var total, active, rateLimited int64
	database.DB.Model(&models.GroqAPIKey{}).Count(&total)
	database.DB.Model(&models.GroqAPIKey{}).Where("is_active = ?", true).Count(&active)
	database.DB.Model(&models.GroqAPIKey{}).Where("is_rate_limited = ?", true).Count(&rateLimited)

	// .env fallback status
	envWhisper := config.App.GroqAPIKey != ""
	envLLM := config.App.GroqLLMAPIKey != ""

	return map[string]interface{}{
		"total_keys":      total,
		"active_keys":     active,
		"rate_limited":    rateLimited,
		"env_whisper_set": envWhisper,
		"env_llm_set":     envLLM,
	}
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ─── Auto-failover retry infrastructure ─────────────────────────────
//
// TryWithKeys iterates every usable API key of the given type in priority
// order, calling fn for each one until it returns nil. Every failure is
// classified and the offending key is parked with a class-appropriate
// cooldown so the SAME exhausted key isn't picked back up on the next
// retry in this loop (or the next request).
//
// The point: the user never sees "429 try again in 6m47s" as long as we
// have other keys to fall back on. A cold /ai/improve call on a fresh
// pod with 3 keys might traverse all 3 before succeeding - that's ~15s
// at worst, but the user just sees their improved speech appear.
//
// If the error is classified as FailureInput (bad audio, prompt too long,
// etc. - same result with any key) we stop immediately. No point burning
// the rest of the pool on a request that will fail the same way every time.

// KeyFailureKind is the coarse bucket we place each error into so the
// right cooldown can be applied. The labels read from the error body
// Groq returns plus the HTTP status code.
type KeyFailureKind int

const (
	// FailureInput - request itself is bad; every key will reject it.
	// Stop iterating, return the error to the user.
	FailureInput KeyFailureKind = iota
	// FailureTransient - network/5xx/timeout; key probably fine, try next.
	FailureTransient
	// FailureRateLimitMinute - per-minute RPM/TPM exceeded. Short cooldown.
	FailureRateLimitMinute
	// FailureRateLimitDay - per-day TPD/RPD exceeded. Long cooldown until
	// the daily window resets (or whatever "retry after" the error states).
	FailureRateLimitDay
	// FailurePermanent - 401/403/model blocked. Key is broken until the
	// admin fixes it in the Groq console. Park for 24h.
	FailurePermanent
)

// retryAfterRx matches "try again in 6m47.808s" or "try again in 12s".
// Groq embeds this inside the JSON error body on 429s.
var retryAfterRx = regexp.MustCompile(`(?i)try again in (?:(\d+)m)?([\d.]+)s`)

// classifyKeyError buckets the raw error string from the HTTP client
// into one of the KeyFailureKind constants. The matching is intentionally
// forgiving (lowercased contains checks) because Groq's error wording
// drifts over time.
func classifyKeyError(errMsg string) KeyFailureKind {
	m := strings.ToLower(errMsg)

	// A model the key cannot reach is NOT a bad request. Groq reports it
	// as invalid_request_error (404 model_not_found / decommissioned),
	// which the input-error branch below would swallow - aborting the
	// whole call without trying the remaining keys and without parking
	// the broken one. It is per-key and stays broken until a human
	// changes the model or the account's access, so treat it as
	// permanent: park this key, fail over to the next.
	if strings.Contains(m, "model_not_found") ||
		strings.Contains(m, "model_decommissioned") ||
		strings.Contains(m, "does not exist or you do not have access") {
		return FailurePermanent
	}

	// 400-class request errors that aren't about the key.
	//   "could not process file - is it a valid media file?"
	//   "request body too large"
	//   "message is too long"
	if strings.Contains(m, "could not process file") ||
		strings.Contains(m, "is it a valid media file") ||
		strings.Contains(m, "request body too large") ||
		strings.Contains(m, "request too large") ||
		strings.Contains(m, " 413") ||
		strings.Contains(m, "message is too long") ||
		strings.Contains(m, "invalid_request_error") && !strings.Contains(m, "429") {
		return FailureInput
	}

	// 401/403 - the key is recognised but can't do what we asked. Usually
	// the account admin has disabled the model or the key was revoked.
	// Same result forever until a human fixes the Groq console.
	if strings.Contains(m, " 401") || strings.Contains(m, " 403") ||
		strings.Contains(m, "blocked at the project") ||
		strings.Contains(m, "permission") ||
		strings.Contains(m, "invalid api key") ||
		strings.Contains(m, "organization_restricted") {
		return FailurePermanent
	}

	// 429 - split into "per day" vs "per minute" because the cooldowns
	// are wildly different (hours vs seconds).
	if strings.Contains(m, "429") || strings.Contains(m, "rate_limit") {
		if strings.Contains(m, "per day") ||
			strings.Contains(m, "tpd") ||
			strings.Contains(m, "rpd") {
			return FailureRateLimitDay
		}
		return FailureRateLimitMinute
	}

	// Network hiccups, 5xx, timeouts, etc.
	return FailureTransient
}

// notTheKeysFault reports failures that are ours rather than the key's:
// a deadline WE imposed, and JSON the model shaped oddly. Both used to
// park a perfectly healthy key for 30 seconds, and with several keys in
// rotation one slow uploader could bench most of the pool for everyone
// else.
func notTheKeysFault(errMsg string) bool {
	m := strings.ToLower(errMsg)
	return strings.Contains(m, "context deadline exceeded") ||
		strings.Contains(m, "client.timeout") ||
		strings.Contains(m, "llm json error")
}

// parseRetryAfter pulls the "try again in 6m47s" hint out of a Groq
// error message and returns it as a Duration. Returns 0 if the hint
// isn't there.
func parseRetryAfter(msg string) time.Duration {
	m := retryAfterRx.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	var d time.Duration
	if m[1] != "" {
		if mins, err := strconv.Atoi(m[1]); err == nil {
			d += time.Duration(mins) * time.Minute
		}
	}
	if secs, err := strconv.ParseFloat(m[2], 64); err == nil {
		d += time.Duration(secs * float64(time.Second))
	}
	return d
}

// cooldownFor returns how long a key should be parked after a failure
// of the given kind. Uses the Groq-supplied "try again in …" hint
// whenever possible, clamped to sane bounds.
func cooldownFor(kind KeyFailureKind, errMsg string) time.Duration {
	// A timeout we set, or output we failed to parse, is not evidence
	// against the key. Count it, but leave the key in rotation.
	if notTheKeysFault(errMsg) {
		return 0
	}

	hinted := parseRetryAfter(errMsg)

	switch kind {
	case FailureRateLimitDay:
		// TPD window resets at UTC midnight; hinted value can be up to
		// ~24h. Clamp to [10m, 2h] so a single request doesn't retire
		// the key for a quarter of the day even on a verbose error -
		// the next request will re-park it if it's still exhausted.
		if hinted > 0 {
			if hinted < 10*time.Minute {
				return 10 * time.Minute
			}
			if hinted > 2*time.Hour {
				return 2 * time.Hour
			}
			return hinted
		}
		return 30 * time.Minute

	case FailureRateLimitMinute:
		// Usually 5-30 seconds per Groq. Never cool a key longer than
		// 5 minutes on a minute-level limit - the window resets fast.
		if hinted > 0 {
			if hinted > 5*time.Minute {
				return 5 * time.Minute
			}
			return hinted + 2*time.Second // tiny buffer past the hint
		}
		return 60 * time.Second

	case FailurePermanent:
		// 24h is long enough that an admin will have noticed and either
		// fixed the Groq console or removed the key by hand.
		return 24 * time.Hour

	case FailureTransient:
		return 30 * time.Second

	default:
		return 0
	}
}

// recordKeyFailure is the classified version of RecordKeyError. It picks
// the right cooldown for the error kind instead of always using 60s.
func recordKeyFailure(keyID *uuid.UUID, errMsg string, kind KeyFailureKind) {
	if keyID == nil {
		return
	}

	now := time.Now()
	updates := map[string]interface{}{
		"failed_requests": database.DB.Raw("failed_requests + 1"),
		"last_error_at":   now,
		"last_error":      truncate(errMsg, 500),
	}

	if cd := cooldownFor(kind, errMsg); cd > 0 {
		updates["is_rate_limited"] = true
		updates["rate_limited_until"] = now.Add(cd)
		log.Warn().
			Str("key_id", keyID.String()).
			Dur("cooldown", cd).
			Int("kind", int(kind)).
			Msg("Groq key parked - switching to next")
	}

	database.DB.Model(&models.GroqAPIKey{}).Where("id = ?", *keyID).Updates(updates)

	go checkAllKeysHealth()
}

// UsableKey is one row from ListUsableKeys. ID is nil for the .env fallback
// so callers know not to record usage/errors against a DB row.
type UsableKey struct {
	Key   string
	ID    *uuid.UUID
	Label string
}

// ListUsableKeys returns every active, non-cooldown API key of the given
// type sorted by priority. Expired cooldowns are cleared on the way out
// so a key that served its penance comes right back into rotation.
// The .env fallback key (if configured) is appended last.
func ListUsableKeys(keyType string) []UsableKey {
	keyMu.Lock()
	defer keyMu.Unlock()

	var keys []models.GroqAPIKey
	database.DB.
		Where("is_active = ? AND (key_type = ? OR key_type = ?)", true, keyType, "both").
		Order("total_requests ASC, priority ASC").
		Find(&keys)

	now := time.Now()
	out := make([]UsableKey, 0, len(keys)+1)
	for i := range keys {
		k := &keys[i]
		// Un-park expired cooldowns eagerly - otherwise keys that served
		// their cooldown silently stay benched until the next explicit
		// ResetKeyRateLimit.
		if k.IsRateLimited && k.RateLimitedUntil != nil && k.RateLimitedUntil.Before(now) {
			database.DB.Model(k).Updates(map[string]interface{}{
				"is_rate_limited":    false,
				"rate_limited_until": nil,
			})
			k.IsRateLimited = false
		}
		if k.IsRateLimited {
			continue
		}
		plain, err := decryptKeyIfNeeded(k)
		if err != nil {
			log.Warn().Err(err).Str("key_id", k.ID.String()).Msg("skip key: decrypt failed")
			continue
		}
		id := k.ID
		out = append(out, UsableKey{Key: plain, ID: &id, Label: k.Label})
	}

	// .env fallbacks go last. A user with one DB key and one .env key
	// exhausts the DB key first, then tries .env as last resort.
	if keyType == "llm" && config.App.GroqLLMAPIKey != "" {
		out = append(out, UsableKey{Key: config.App.GroqLLMAPIKey, ID: nil, Label: "env:llm"})
	}
	if (keyType == "whisper" || keyType == "both") && config.App.GroqAPIKey != "" {
		out = append(out, UsableKey{Key: config.App.GroqAPIKey, ID: nil, Label: "env:whisper"})
	}

	return out
}

// maxKeyAttempts bounds how many keys one request may burn through.
// Three is enough to ride out a rate-limited or dead key while keeping
// the worst case bearable for the person waiting.
const maxKeyAttempts = 3

// TryWithKeys runs fn against each usable key of the given type in order
// until one succeeds. Every failure is classified, the failing key is
// parked with an appropriate cooldown, and the next key is tried. If the
// error is FailureInput (bad user input, same outcome with every key) we
// stop immediately and bubble it up - no sense burning the whole pool on
// a doomed request.
//
// Returns nil on first success. Returns the last observed error if every
// usable key fails. Returns a distinct sentinel if no keys are available
// at all (every key disabled/rate-limited).
func TryWithKeys(keyType string, fn func(apiKey string) error) error {
	keys := ListUsableKeys(keyType)
	if len(keys) == 0 {
		go checkAllKeysHealth()
		return fmt.Errorf("%s: hozircha mavjud API key yo'q", keyType)
	}

	var lastErr error
	for i, k := range keys {
		// Cap the cascade. Failing over is worth doing once or twice -
		// a rate-limited key, a bad key - but walking the whole pool is
		// not: an AI check re-uploads the entire recording on every
		// attempt, so with eight keys a user on a weak connection would
		// wait minutes before being told it failed. The ledger shows
		// exactly that happening - the same handful of timeouts recorded
		// against every whisper key in turn.
		if i >= maxKeyAttempts {
			log.Warn().
				Int("tried", i).
				Int("available", len(keys)).
				Str("key_type", keyType).
				Msg("giving up after the attempt cap rather than burning the pool")
			break
		}

		err := fn(k.Key)
		if err == nil {
			RecordKeyUsage(k.ID)
			if i > 0 {
				log.Info().
					Str("key_label", k.Label).
					Int("attempt", i+1).
					Msg("auto-failover succeeded on backup key")
			}
			return nil
		}
		lastErr = err

		kind := classifyKeyError(err.Error())
		if kind == FailureInput {
			// Don't punish the key for a bad request.
			log.Debug().Err(err).Msg("input-level error, not a key problem")
			return err
		}

		recordKeyFailure(k.ID, err.Error(), kind)
		log.Warn().
			Err(err).
			Str("key_label", k.Label).
			Int("kind", int(kind)).
			Int("attempt", i+1).
			Int("total", len(keys)).
			Msg("key failed, trying next")
	}

	tried := len(keys)
	if tried > maxKeyAttempts {
		tried = maxKeyAttempts
	}
	return fmt.Errorf("all %d %s keys failed: %w", tried, keyType, lastErr)
}
