DROP TABLE IF EXISTS friendships;

ALTER TABLE scheduled_sessions DROP COLUMN IF EXISTS note;
ALTER TABLE scheduled_sessions DROP COLUMN IF EXISTS confirmed_at;
ALTER TABLE scheduled_sessions DROP COLUMN IF EXISTS cancelled_by_id;
ALTER TABLE scheduled_sessions DROP COLUMN IF EXISTS reminded_at;
ALTER TABLE scheduled_sessions DROP COLUMN IF EXISTS start_notified_at;
