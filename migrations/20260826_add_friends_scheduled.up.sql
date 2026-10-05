-- Friends and scheduled speaking appointments.
--
-- Friendship is the gate on scheduling: you can only book time with
-- somebody who agreed to it, and you can only ask somebody you have
-- actually spoken to (enforced in the service layer).
CREATE TABLE IF NOT EXISTS friendships (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ,

    requester_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    addressee_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status       VARCHAR(16) NOT NULL DEFAULT 'pending',

    -- The two ids sorted and joined, so a duplicate is caught whichever
    -- way round it is sent. Without it A->B and B->A would both exist
    -- and every friend list would show the same person twice.
    pair_key     VARCHAR(80) NOT NULL,
    responded_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_friendships_pair      ON friendships (pair_key);
CREATE INDEX        IF NOT EXISTS idx_friendships_requester ON friendships (requester_id);
CREATE INDEX        IF NOT EXISTS idx_friendships_addressee ON friendships (addressee_id);
CREATE INDEX        IF NOT EXISTS idx_friendships_status    ON friendships (status);
CREATE INDEX        IF NOT EXISTS idx_friendships_deleted   ON friendships (deleted_at);

-- scheduled_sessions already existed (unused, unrouted). These are the
-- columns the working feature needs.
ALTER TABLE scheduled_sessions ADD COLUMN IF NOT EXISTS note              VARCHAR(256) NOT NULL DEFAULT '';
ALTER TABLE scheduled_sessions ADD COLUMN IF NOT EXISTS confirmed_at      TIMESTAMPTZ;
ALTER TABLE scheduled_sessions ADD COLUMN IF NOT EXISTS cancelled_by_id   UUID REFERENCES users(id) ON DELETE SET NULL;
-- Idempotence stamps for the once-a-minute reminder sweep: without them
-- a restart mid-tick would re-send the same nudge every minute.
ALTER TABLE scheduled_sessions ADD COLUMN IF NOT EXISTS reminded_at       TIMESTAMPTZ;
ALTER TABLE scheduled_sessions ADD COLUMN IF NOT EXISTS start_notified_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_scheduled_user1  ON scheduled_sessions (user1_id);
CREATE INDEX IF NOT EXISTS idx_scheduled_user2  ON scheduled_sessions (user2_id);
-- The sweep scans by time within the two live statuses every minute.
CREATE INDEX IF NOT EXISTS idx_scheduled_sweep  ON scheduled_sessions (status, scheduled_at);
