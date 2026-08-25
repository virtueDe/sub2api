CREATE TABLE IF NOT EXISTS check_in_records (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    check_in_date DATE NOT NULL,
    reward NUMERIC(20, 8) NOT NULL,
    request_count INTEGER NOT NULL DEFAULT 0,
    daily_spend NUMERIC(20, 8) NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT check_in_records_user_date_unique UNIQUE (user_id, check_in_date)
);

CREATE INDEX IF NOT EXISTS check_in_records_date_idx ON check_in_records (check_in_date);
CREATE INDEX IF NOT EXISTS check_in_records_user_date_idx ON check_in_records (user_id, check_in_date DESC);
