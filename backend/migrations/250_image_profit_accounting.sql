-- Append-only procurement prices and immutable daily accounting revisions.
-- No changes to customer balances or original usage records.
CREATE TABLE IF NOT EXISTS image_procurement_prices (
    id BIGSERIAL PRIMARY KEY,
    scope_type VARCHAR(16) NOT NULL CHECK (scope_type IN ('account', 'channel')),
    scope_id BIGINT NOT NULL CHECK (scope_id > 0),
    model VARCHAR(200) NOT NULL,
    resolution VARCHAR(8) NOT NULL CHECK (resolution IN ('1K', '2K', '4K')),
    unit_price_usd NUMERIC(20,10) NOT NULL CHECK (unit_price_usd >= 0),
    effective_at TIMESTAMPTZ NOT NULL,
    note VARCHAR(500) NOT NULL,
    created_by BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_image_procurement_price_lookup
    ON image_procurement_prices(scope_type, scope_id, model, resolution, effective_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS image_profit_reports (
    id BIGSERIAL PRIMARY KEY,
    report_date DATE NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    status VARCHAR(16) NOT NULL CHECK (status IN ('complete', 'incomplete')),
    result JSONB NOT NULL,
    reason VARCHAR(500) NOT NULL,
    created_by BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(report_date, revision)
);
CREATE INDEX IF NOT EXISTS idx_image_profit_reports_date
    ON image_profit_reports(report_date DESC, revision DESC);
