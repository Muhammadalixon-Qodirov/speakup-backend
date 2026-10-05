-- Rollback for 20260731_add_rooms.up.sql
--
-- Drops only the composite indexes. The tables and the
-- `sessions.room_id` column are intentionally left in place: dropping
-- room_id would destroy the record of which past sessions were classroom
-- sessions, and that data cannot be reconstructed.

DROP INDEX CONCURRENTLY IF EXISTS idx_sessions_room_status;
DROP INDEX CONCURRENTLY IF EXISTS idx_sessions_room_created;
DROP INDEX CONCURRENTLY IF EXISTS idx_sessions_public_status_created;
DROP INDEX CONCURRENTLY IF EXISTS idx_room_members_room_minutes;
DROP INDEX CONCURRENTLY IF EXISTS idx_room_members_user;
