-- NULL is a legacy intent, zero is a deliberately free image reservation.
ALTER TABLE usage_settlements
    ADD COLUMN reserved_amount numeric(20,8) CHECK (reserved_amount >= 0),
    ADD COLUMN reserved_subscription_id bigint,
    ADD COLUMN dispatched_at timestamptz;
ALTER TABLE usage_settlements DROP CONSTRAINT usage_settlements_state_check;
ALTER TABLE usage_settlements ADD CONSTRAINT usage_settlements_state_check
    CHECK (state IN ('created','ready','charged','complete','needs_review','release_pending','released'));
ALTER TABLE usage_settlements ADD CONSTRAINT usage_settlement_reservation_check
    CHECK (reserved_subscription_id IS NULL OR reserved_amount IS NOT NULL);
CREATE INDEX usage_settlements_key_holds ON usage_settlements(api_key_id)
    WHERE reserved_amount IS NOT NULL AND state IN ('created','ready','needs_review','release_pending');
CREATE INDEX usage_settlements_subscription_holds ON usage_settlements(reserved_subscription_id)
    WHERE reserved_amount IS NOT NULL AND state IN ('created','ready','needs_review','release_pending');
CREATE INDEX usage_settlements_release_pending ON usage_settlements(updated_at,id)
    WHERE state='release_pending' OR (state='released' AND last_error_code='cache_invalidation_pending');

CREATE OR REPLACE FUNCTION protect_usage_settlement_facts() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.snapshot IS NOT NULL AND NEW.snapshot IS DISTINCT FROM OLD.snapshot THEN
        RAISE EXCEPTION 'settlement facts are immutable';
    END IF;
    IF (OLD.request_id,OLD.api_key_id,OLD.user_id,OLD.reserved_amount,OLD.reserved_subscription_id)
       IS DISTINCT FROM
       (NEW.request_id,NEW.api_key_id,NEW.user_id,NEW.reserved_amount,NEW.reserved_subscription_id) THEN
        RAISE EXCEPTION 'settlement identity and reservation are immutable';
    END IF;
    RETURN NEW;
END $$;
