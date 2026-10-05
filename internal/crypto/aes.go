// Package crypto provides AES-256-GCM encryption helpers for
// at-rest secret storage (API keys, tokens, etc.).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Encryptor wraps an AES-256-GCM AEAD so callers don't have to manage
// nonces, ciphertext layout, or base64 encoding themselves.
type Encryptor struct {
	gcm cipher.AEAD
}

// NewEncryptor builds an Encryptor from a 64-character hex master key
// (= 32 bytes = AES-256). Returns an error if the key is malformed.
func NewEncryptor(masterKeyHex string) (*Encryptor, error) {
	if len(masterKeyHex) != 64 {
		return nil, fmt.Errorf("master key must be 64 hex chars (32 bytes), got %d", len(masterKeyHex))
	}

	key, err := hex.DecodeString(masterKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex master key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	return &Encryptor{gcm: gcm}, nil
}

// Encrypt returns base64(nonce ‖ ciphertext ‖ tag).
func (e *Encryptor) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", errors.New("empty plaintext")
	}

	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := e.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt.
func (e *Encryptor) Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", errors.New("empty ciphertext")
	}

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}

	nonceSize := e.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := e.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
