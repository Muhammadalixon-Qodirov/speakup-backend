package services

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/speak-up/backend/internal/config"
	"github.com/speak-up/backend/internal/database"
	"github.com/speak-up/backend/internal/models"
)

// In-memory key cache — DB query faqat 30 sekundda 1 marta.
// Har request da DB ga bormasdan xotiradan key oladi.
var (
	cachedWhisperKeys []cachedKey
	cachedLLMKeys     []cachedKey
	keyCacheTime      time.Time
	keyCacheTTL       = 30 * time.Second
	whisperCounter    uint64
	llmCounter        uint64
	cacheMu           sync.Mutex
)

type cachedKey struct {
	ID       uuid.UUID
	PlainKey string
}

// refreshKeyCache loads keys from DB into memory (max once per 30s).
func refreshKeyCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if time.Since(keyCacheTime) < keyCacheTTL {
		return
	}

	var allKeys []models.GroqAPIKey
	database.DB.
		Where("is_active = ? AND (is_rate_limited = ? OR rate_limited_until < ?)", true, false, time.Now()).
		Order("priority ASC").
		Find(&allKeys)

	cachedWhisperKeys = nil
	cachedLLMKeys = nil

	for _, k := range allKeys {
		plain, err := decryptKeyIfNeeded(&k)
		if err != nil {
			continue
		}
		ck := cachedKey{ID: k.ID, PlainKey: plain}
		switch k.KeyType {
		case "whisper":
			cachedWhisperKeys = append(cachedWhisperKeys, ck)
		case "llm":
			cachedLLMKeys = append(cachedLLMKeys, ck)
		case "both":
			cachedWhisperKeys = append(cachedWhisperKeys, ck)
			cachedLLMKeys = append(cachedLLMKeys, ck)
		}
	}

	keyCacheTime = time.Now()
}

// InvalidateKeyCache forces reload on next GetActiveKey call.
func InvalidateKeyCache() {
	cacheMu.Lock()
	keyCacheTime = time.Time{}
	cacheMu.Unlock()
}

// GetActiveKeyRR returns next key using round-robin from cache.
// No DB query per request — only every 30 seconds.
func GetActiveKeyRR(keyType string) (string, *uuid.UUID) {
	refreshKeyCache()

	cacheMu.Lock()
	defer cacheMu.Unlock()

	var keys []cachedKey
	var counter *uint64

	if keyType == "whisper" {
		keys = cachedWhisperKeys
		counter = &whisperCounter
	} else {
		keys = cachedLLMKeys
		counter = &llmCounter
	}

	if len(keys) == 0 {
		if keyType == "llm" && config.App.GroqLLMAPIKey != "" {
			return config.App.GroqLLMAPIKey, nil
		}
		if config.App.GroqAPIKey != "" {
			return config.App.GroqAPIKey, nil
		}
		return "", nil
	}

	idx := int(*counter) % len(keys)
	*counter++

	k := keys[idx]
	return k.PlainKey, &k.ID
}
