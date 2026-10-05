ALTER TABLE groq_api_keys
    DROP COLUMN IF EXISTS encrypted_key,
    DROP COLUMN IF EXISTS encryption_version;

DROP INDEX CONCURRENTLY IF EXISTS idx_groq_keys_encryption_version;
