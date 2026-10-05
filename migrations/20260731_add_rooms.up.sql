-- Private teacher rooms - performance indexes
-- Date: 2026-07-31
--
-- The `rooms` / `room_members` tables and the `sessions.room_id` column
-- are created by GORM AutoMigrate at boot, together with the single-column
-- indexes declared in the model tags. This file adds only the COMPOSITE
-- indexes AutoMigrate cannot express, which back the three hot room
-- queries.
--
-- NOTE: CONCURRENTLY cannot be run inside a transaction block.
-- Run each statement individually via psql, not inside BEGIN/COMMIT.

-- ========== SESSIONS ==========

-- ActiveRoomSpeakers: "who in this room is on a call right now".
-- Runs on every live-panel broadcast, so it must not scan.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_room_status
    ON sessions(room_id, status) WHERE room_id IS NOT NULL;

-- ListRoomSessions + RoomReport: the lesson log and the attendance
-- report, both ordered/filtered by time inside one room.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_room_created
    ON sessions(room_id, created_at DESC) WHERE room_id IS NOT NULL;

-- The public leaderboard now filters `room_id IS NULL` so classroom
-- minutes can't win the weekly prize. A partial index keeps that scan
-- limited to public-queue rows.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_public_status_created
    ON sessions(status, created_at DESC) WHERE room_id IS NULL;

-- ========== ROOM MEMBERS ==========

-- ListRoomMembers orders the roster by owner-first, then activity.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_room_members_room_minutes
    ON room_members(room_id, total_minutes DESC);

-- ListMyRooms: "which rooms does this student attend".
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_room_members_user
    ON room_members(user_id) WHERE deleted_at IS NULL;
