package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GroqAPIKey stores API keys for Groq (Whisper + LLM).
// Multiple keys can be added - system auto-rotates when one hits rate limit.
type GroqAPIKey struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Key               string `gorm:"size:256;not null" json:"-"`             // API key plain (legacy)
	EncryptedKey      string `gorm:"type:text" json:"-"`                    // AES-GCM encrypted
	EncryptionVersion int    `gorm:"default:0" json:"-"`                    // 0=plain, 1=AES
	KeyMasked         string `gorm:"-" json:"key_masked"`                   // gsk_xxxx...xxxx (computed)
	Label             string `gorm:"size:128" json:"label"`                 // "Whisper key 1", "LLM backup"
	KeyType           string `gorm:"size:32;default:'both'" json:"key_type"` // whisper | llm | both
	IsActive  bool    `gorm:"default:true" json:"is_active"`
	Priority  int     `gorm:"default:0" json:"priority"`           // lower = used first

	// Usage tracking
	TotalRequests  int        `gorm:"default:0" json:"total_requests"`
	FailedRequests int        `gorm:"default:0" json:"failed_requests"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	LastErrorAt    *time.Time `json:"last_error_at"`
	LastError      string     `gorm:"size:512" json:"last_error"`

	// Rate limit tracking
	IsRateLimited    bool       `gorm:"default:false" json:"is_rate_limited"`
	RateLimitedUntil *time.Time `json:"rate_limited_until"`
}

func (k *GroqAPIKey) BeforeCreate(tx *gorm.DB) error {
	if k.ID == uuid.Nil {
		k.ID = uuid.New()
	}
	return nil
}

// MaskKey returns gsk_xxxx...xxxx format for display.
func (k *GroqAPIKey) MaskKey() string {
	if len(k.Key) < 12 {
		return "***"
	}
	return k.Key[:8] + "..." + k.Key[len(k.Key)-4:]
}
