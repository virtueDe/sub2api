-- Durable Gemini image settlement facts. Intentionally no cascading foreign
-- keys: deleting an identity must not erase evidence of an unresolved charge.
CREATE TABLE usage_settlements (
    id bigserial PRIMARY KEY,
    request_id text NOT NULL,
    api_key_id bigint NOT NULL,
    user_id bigint NOT NULL,
    state text NOT NULL DEFAULT 'created'
        CHECK (state IN ('created','ready','charged','complete','needs_review')),
    snapshot jsonb,
    attempts integer NOT NULL DEFAULT 0,
    last_error_code text,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    charged_at timestamptz,
    UNIQUE (request_id,api_key_id),
    CHECK (snapshot IS NULL OR octet_length(snapshot::text)<=65536),
    CHECK (state NOT IN ('ready','charged','complete') OR snapshot IS NOT NULL)
);
CREATE INDEX usage_settlements_retry ON usage_settlements(next_attempt_at,id) WHERE state IN ('ready','charged');
CREATE INDEX usage_settlements_created ON usage_settlements(created_at,id) WHERE state='created';
CREATE INDEX usage_settlements_review ON usage_settlements(updated_at) WHERE state='needs_review';
CREATE INDEX usage_settlements_unfinished ON usage_settlements(id) WHERE state<>'complete';

CREATE FUNCTION protect_usage_settlement_facts() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.snapshot IS NOT NULL AND NEW.snapshot IS DISTINCT FROM OLD.snapshot THEN
        RAISE EXCEPTION 'settlement facts are immutable';
    END IF;
    IF (OLD.request_id,OLD.api_key_id,OLD.user_id) IS DISTINCT FROM (NEW.request_id,NEW.api_key_id,NEW.user_id) THEN
        RAISE EXCEPTION 'settlement identity is immutable';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER usage_settlement_immutable BEFORE UPDATE ON usage_settlements
FOR EACH ROW EXECUTE FUNCTION protect_usage_settlement_facts();
