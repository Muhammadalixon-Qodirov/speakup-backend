-- Partner learning centres — performance indexes
-- Date: 2026-08-01
--
-- The `study_centers` table and the `rooms.center_id` column are created
-- by GORM AutoMigrate at boot, along with the single-column indexes
-- declared in the model tags. This file adds only what AutoMigrate
-- cannot express.
--
-- NOTE: CONCURRENTLY cannot be run inside a transaction block.
-- Run each statement individually via psql, not inside BEGIN/COMMIT.

-- ========== STUDY CENTERS ==========

-- SessionBanners() runs on every live session start. It filters on four
-- columns and orders by impressions; this partial index covers exactly
-- the eligible set, which is a tiny fraction of the table, so the query
-- never touches a rejected or expired partner.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_centers_banner_rotation
    ON study_centers(banner_impressions ASC)
    WHERE deleted_at IS NULL
      AND is_active = true
      AND is_advertised = true
      AND moderation_status = 'approved';

-- The admin moderation queue opens on `status=pending`.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_centers_moderation_created
    ON study_centers(moderation_status, created_at DESC)
    WHERE deleted_at IS NULL;

-- GetCenterByOwner: called on every /centers/mine request.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_centers_owner
    ON study_centers(owner_id, created_at ASC)
    WHERE deleted_at IS NULL;

-- ========== ROOMS ==========

-- CenterRooms: a centre's group list. Partial, because most rooms have
-- no centre and there is no point indexing those NULLs.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_rooms_center
    ON rooms(center_id, created_at ASC)
    WHERE center_id IS NOT NULL;
