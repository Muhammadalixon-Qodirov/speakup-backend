-- Rollback for 20260801_add_study_centers.up.sql
--
-- Drops only the indexes. The `study_centers` table and
-- `rooms.center_id` are intentionally left alone: dropping them would
-- destroy partner profiles, uploaded-logo references and the banner
-- impression/click history a partnership is billed on.

DROP INDEX CONCURRENTLY IF EXISTS idx_centers_banner_rotation;
DROP INDEX CONCURRENTLY IF EXISTS idx_centers_moderation_created;
DROP INDEX CONCURRENTLY IF EXISTS idx_centers_owner;
DROP INDEX CONCURRENTLY IF EXISTS idx_rooms_center;
