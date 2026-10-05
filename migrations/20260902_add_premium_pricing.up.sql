-- Premium price and discount campaigns move out of the frontend source.
--
-- The month price, the destination card and the admin's Telegram handle
-- were constants in the Mini App: changing any of them meant a code
-- change and a deploy, so the people who actually decide them could not.
-- premium_settings is the single row that now holds them.
--
-- premium_campaigns is the other half of the ask: a discount an admin
-- can start whenever they like, carrying the REASON it exists ("Navro'z
-- munosabati bilan") so the user is shown an occasion rather than a bare
-- percentage.
CREATE TABLE IF NOT EXISTS premium_settings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    base_price_uzs  INTEGER NOT NULL DEFAULT 30000,
    card_number     VARCHAR(32)  NOT NULL DEFAULT '',
    card_owner      VARCHAR(128) NOT NULL DEFAULT '',
    admin_username  VARCHAR(64)  NOT NULL DEFAULT '',

    updated_by_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    updated_by_name VARCHAR(128) NOT NULL DEFAULT ''
);

-- Seed the row with exactly the values that were compiled into the
-- client, so an install that upgrades and touches nothing keeps showing
-- the same price and the same card.
INSERT INTO premium_settings (base_price_uzs, card_number, card_owner, admin_username)
SELECT 30000, '4023060514050896', 'Nozimjonov X', 'xojiakbar_nozimjonov'
WHERE NOT EXISTS (SELECT 1 FROM premium_settings);

CREATE TABLE IF NOT EXISTS premium_campaigns (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ,

    title               VARCHAR(120) NOT NULL,
    reason              VARCHAR(240) NOT NULL DEFAULT '',
    emoji               VARCHAR(16)  NOT NULL DEFAULT '',
    -- holiday | newyear | ramadan | flash | birthday | student | custom
    theme               VARCHAR(24)  NOT NULL DEFAULT 'holiday',
    discount_percent    INTEGER      NOT NULL,

    -- NULL starts_at = already running; NULL ends_at = no countdown,
    -- runs until an admin switches it off.
    starts_at           TIMESTAMPTZ,
    ends_at             TIMESTAMPTZ,
    is_active           BOOLEAN NOT NULL DEFAULT TRUE,
    -- FALSE: the user gets the better of campaign vs. their wheel
    -- coupons and keeps the coupons. TRUE: the two are added together.
    stacks_with_coupons BOOLEAN NOT NULL DEFAULT FALSE,

    created_by_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_by_name     VARCHAR(128) NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_premium_campaigns_deleted_at ON premium_campaigns (deleted_at);
-- The hot query is "which campaign is live now", run on every open of
-- the purchase sheet.
CREATE INDEX IF NOT EXISTS idx_premium_campaigns_window
    ON premium_campaigns (is_active, starts_at, ends_at);
