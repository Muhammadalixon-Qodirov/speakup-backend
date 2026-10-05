-- Performance indexes - zero-downtime migration
-- Date: 2026-04-17
-- NOTE: CONCURRENTLY cannot be run inside a transaction block.
-- Run each statement individually via psql, not inside BEGIN/COMMIT.

-- ========== USERS ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_referred_by
    ON users(referred_by) WHERE referred_by IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_is_premium_expires
    ON users(is_premium, premium_expires_at) WHERE is_premium = true;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_language_level_active
    ON users(level, last_active_at DESC)
    WHERE telegram_id IS NOT NULL;

-- ========== SESSIONS ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_user1_status
    ON sessions(user1_id, status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_user2_status
    ON sessions(user2_id, status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_status_created
    ON sessions(status, created_at DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_created_at
    ON sessions(created_at DESC);

-- ========== PAYMENTS ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_payments_status
    ON payments(status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_payments_user_status
    ON payments(user_id, status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_payments_created_at
    ON payments(created_at DESC);

-- ========== PRIZES ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prizes_available_on
    ON prizes(available_on);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prizes_user_available
    ON prizes(user_id, available_on DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_prizes_status
    ON prizes(status);

-- ========== FEEDBACK TICKETS ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feedback_tickets_status
    ON feedback_tickets(status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feedback_tickets_user_status
    ON feedback_tickets(user_id, status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feedback_tickets_created_at
    ON feedback_tickets(created_at DESC);

-- ========== GROQ API KEYS ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_groq_keys_active_type
    ON groq_api_keys(is_active, key_type) WHERE is_active = true;

-- ========== PROMO CODES ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_promo_codes_active_expires
    ON promo_codes(is_active, expires_at) WHERE is_active = true;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_promo_redemptions_user
    ON promo_redemptions(user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_promo_redemptions_code
    ON promo_redemptions(promo_code_id);

-- ========== AI REPORTS ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_speaking_reports_user_created
    ON speaking_reports(user_id, created_at DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_full_test_reports_status
    ON full_test_reports(status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_full_test_reports_user
    ON full_test_reports(user_id, created_at DESC);

-- ========== AUTH OTP ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_otp_sessions_expires
    ON auth_otp_sessions(expires_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_otp_sessions_phone
    ON auth_otp_sessions(phone);

-- ========== SCHEDULED SESSIONS ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_scheduled_sessions_status
    ON scheduled_sessions(status);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_scheduled_sessions_user_time
    ON scheduled_sessions(user1_id, scheduled_at);

-- ========== WORD BANK ==========
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_word_bank_user_created
    ON word_bank(user_id, created_at DESC);
