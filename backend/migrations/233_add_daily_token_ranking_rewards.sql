CREATE TABLE IF NOT EXISTS daily_token_ranking_rewards (
    id              BIGSERIAL PRIMARY KEY,
    reward_date     DATE NOT NULL,
    rank            INT NOT NULL,
    user_id         BIGINT REFERENCES users(id) ON DELETE SET NULL,
    display_name    VARCHAR(255) NOT NULL DEFAULT '',
    total_tokens    BIGINT NOT NULL DEFAULT 0,
    request_count   BIGINT NOT NULL DEFAULT 0,
    reward_amount   DECIMAL(20, 8) NOT NULL DEFAULT 0,
    status          VARCHAR(20) NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    note            TEXT NOT NULL,
    operator_id     BIGINT REFERENCES users(id) ON DELETE SET NULL,
    settled_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (reward_date, rank)
);

CREATE INDEX IF NOT EXISTS idx_daily_token_ranking_rewards_date
    ON daily_token_ranking_rewards (reward_date);
