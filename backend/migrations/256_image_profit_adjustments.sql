-- Accounting decisions are separate from customer charges and raw usage facts.
-- No FK to usage_logs: retain the audit snapshot after usage retention cleanup.
CREATE TABLE image_profit_usage_adjustments (
    usage_log_id BIGINT PRIMARY KEY CHECK (usage_log_id > 0),
    confirmed_loss BOOLEAN NOT NULL DEFAULT false,
    image_count_override INTEGER CHECK (image_count_override > 0),
    original_snapshot JSONB NOT NULL CHECK (jsonb_typeof(original_snapshot) = 'object'),
    reason VARCHAR(500) NOT NULL CHECK (length(trim(reason)) > 0),
    batch_key VARCHAR(100) NOT NULL,
    created_by BIGINT NOT NULL CHECK (created_by > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CHECK (confirmed_loss OR image_count_override IS NOT NULL)
);

-- Persist the boundary once. Historical rows, including late imports, keep their
-- old accounting eligibility. A later role change must not reprice old reports.
CREATE TABLE image_profit_accounting_policy (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    exclude_admin_from TIMESTAMPTZ NOT NULL
);

CREATE TABLE image_profit_excluded_usage (
    usage_log_id BIGINT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    usage_created_at TIMESTAMPTZ NOT NULL,
    reason VARCHAR(40) NOT NULL CHECK (reason = 'admin_usage'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE FUNCTION capture_image_profit_admin_exclusion() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.billing_type = 0 AND COALESCE(NEW.video_count, 0) = 0
       AND (NEW.image_count > 0 OR NEW.image_output_tokens > 0 OR NEW.billing_mode = 'image')
       AND NEW.created_at >= (SELECT exclude_admin_from FROM image_profit_accounting_policy WHERE id)
       AND EXISTS (SELECT 1 FROM users WHERE id = NEW.user_id AND role = 'admin') THEN
        INSERT INTO image_profit_excluded_usage(usage_log_id, user_id, usage_created_at, reason)
        VALUES (NEW.id, NEW.user_id, NEW.created_at, 'admin_usage')
        ON CONFLICT (usage_log_id) DO NOTHING;
    END IF;
    RETURN NEW;
END $$;

-- A parent row trigger is cloned onto existing and future partitions, including
-- writes addressed directly to a leaf. It reads roles without locking users.
CREATE TRIGGER usage_logs_image_profit_admin_exclusion
AFTER INSERT ON usage_logs FOR EACH ROW
EXECUTE FUNCTION capture_image_profit_admin_exclusion();

-- CREATE TRIGGER has now acquired write-conflicting locks on the parent and
-- its existing leaves. Set the boundary only after that barrier: concurrent
-- inserts must either finish before it or observe the committed trigger.
INSERT INTO image_profit_accounting_policy(id, exclude_admin_from)
VALUES (true, clock_timestamp());
