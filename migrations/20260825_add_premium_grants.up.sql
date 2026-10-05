-- Premium ledger: every hand-granted subscription with the reason it was
-- given. Premium is not sold through a payment provider here - an admin
-- flips it on after a bank transfer, or gives it away - so without this
-- table a paid account and a favour look identical after the fact.
CREATE TABLE IF NOT EXISTS premium_grants (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ,

    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    granted_by_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    granted_by_name     VARCHAR(128) NOT NULL DEFAULT '',

    -- sale | friend | partner | promo | compensation | test
    kind                VARCHAR(24) NOT NULL,
    -- Whole so'm, and only ever set for kind = 'sale'.
    amount_uzs          INTEGER NOT NULL DEFAULT 0,
    note                VARCHAR(512) NOT NULL DEFAULT '',
    months              INTEGER NOT NULL DEFAULT 0,

    -- Premium stacks, so the window before the grant is kept: without it
    -- three months added to an existing year reads as a fresh sale.
    previous_expires_at TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ NOT NULL,

    discount_percent    INTEGER NOT NULL DEFAULT 0,
    coupons_used        INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_premium_grants_user       ON premium_grants (user_id);
CREATE INDEX IF NOT EXISTS idx_premium_grants_kind       ON premium_grants (kind);
CREATE INDEX IF NOT EXISTS idx_premium_grants_granted_by ON premium_grants (granted_by_id);
CREATE INDEX IF NOT EXISTS idx_premium_grants_deleted_at ON premium_grants (deleted_at);
-- The ledger is always read newest-first, usually narrowed to one kind.
CREATE INDEX IF NOT EXISTS idx_premium_grants_created    ON premium_grants (created_at DESC);
