-- Additive: plain text key column remains for backward compatibility.
-- Phase 1: add encrypted columns, auto-migrate fills them on startup.
-- Phase 2 (future PR): drop plain key column after verifying all keys encrypted.

ALTER TABLE groq_api_keys
    ADD COLUMN IF NOT EXISTS encrypted_key TEXT,
    ADD COLUMN IF NOT EXISTS encryption_version INT DEFAULT 0;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_groq_keys_encryption_version
    ON groq_api_keys(encryption_version);
