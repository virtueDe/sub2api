-- Derived statistics only. No billing or source history is rewritten here.
CREATE TABLE usage_key_hourly_rollups (
    bucket_start timestamptz NOT NULL,
    api_key_id bigint NOT NULL,
    user_id bigint NOT NULL,
    requests bigint NOT NULL,
    input_tokens bigint NOT NULL,
    output_tokens bigint NOT NULL,
    cache_creation_tokens bigint NOT NULL,
    cache_read_tokens bigint NOT NULL,
    total_cost numeric(30,10) NOT NULL,
    actual_cost numeric(30,10) NOT NULL,
    duration_sum numeric NOT NULL,
    duration_count bigint NOT NULL,
    computed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (bucket_start, api_key_id, user_id)
);
CREATE INDEX usage_key_hourly_key ON usage_key_hourly_rollups(api_key_id, bucket_start);
CREATE INDEX usage_key_hourly_user ON usage_key_hourly_rollups(user_id, bucket_start);

CREATE TABLE usage_key_rollup_events (
    id bigserial PRIMARY KEY,
    bucket_start timestamptz NOT NULL,
    api_key_id bigint NOT NULL,
    user_id bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX usage_key_rollup_events_bucket ON usage_key_rollup_events(bucket_start, id);
CREATE INDEX usage_key_rollup_events_key ON usage_key_rollup_events(api_key_id, bucket_start);
CREATE INDEX usage_key_rollup_events_user ON usage_key_rollup_events(user_id, bucket_start);

CREATE TABLE usage_key_rollup_state (
    id integer PRIMARY KEY CHECK (id = 1),
    version integer NOT NULL DEFAULT 1 CHECK (version = 1),
    initialized boolean NOT NULL DEFAULT false,
    integrity_failed boolean NOT NULL DEFAULT false,
    coverage_start timestamptz,
    coverage_end timestamptz,
    last_backfill_at timestamptz NOT NULL DEFAULT to_timestamp(0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO usage_key_rollup_state(id) VALUES(1);
CREATE TABLE usage_key_rollup_backfill (
    bucket_start timestamptz PRIMARY KEY,
    completed_at timestamptz
);
CREATE INDEX usage_key_rollup_backfill_pending ON usage_key_rollup_backfill(bucket_start) WHERE completed_at IS NULL;

CREATE FUNCTION usage_key_rollup_capture_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_key_rollup_events(bucket_start, api_key_id, user_id)
    SELECT DISTINCT date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', api_key_id, user_id
    FROM inserted_usage_logs;
    RETURN NULL;
END $$;

CREATE FUNCTION usage_key_rollup_capture_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_key_rollup_events(bucket_start, api_key_id, user_id)
    VALUES(date_trunc('hour', OLD.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', OLD.api_key_id, OLD.user_id);
    IF TG_OP = 'UPDATE' THEN
        INSERT INTO usage_key_rollup_events(bucket_start, api_key_id, user_id)
        VALUES(date_trunc('hour', NEW.created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', NEW.api_key_id, NEW.user_id);
    END IF;
    RETURN NULL;
END $$;

-- UPDATE transition tables include rows moved across partitions, for which
-- PostgreSQL fires DELETE/INSERT row triggers but no AFTER UPDATE row trigger.
CREATE FUNCTION usage_key_rollup_capture_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_key_rollup_events(bucket_start, api_key_id, user_id)
    SELECT date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', api_key_id, user_id
    FROM (SELECT created_at,api_key_id,user_id FROM old_usage_logs
          UNION SELECT created_at,api_key_id,user_id FROM new_usage_logs) changed
    GROUP BY 1,2,3;
    RETURN NULL;
END $$;

CREATE FUNCTION usage_key_rollup_reject_truncate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'usage_logs TRUNCATE is disabled: use bounded DELETE so rollup changes are captured';
END $$;

-- Statement triggers on each relation cover both parent-targeted bulk inserts
-- and direct inserts into existing/future leaf partitions. Parent row triggers
-- below are inherited by partitions, including FK cascades.
CREATE FUNCTION install_usage_key_rollup_partition_triggers(target regclass) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('LOCK TABLE %s IN SHARE ROW EXCLUSIVE MODE', target);
    IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=target AND tgname='usage_key_rollup_insert') THEN
        EXECUTE format('CREATE TRIGGER usage_key_rollup_insert AFTER INSERT ON %s REFERENCING NEW TABLE AS inserted_usage_logs FOR EACH STATEMENT EXECUTE FUNCTION usage_key_rollup_capture_insert()', target);
    END IF;
    IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=target AND tgname='usage_key_rollup_update') THEN
        EXECUTE format('CREATE TRIGGER usage_key_rollup_update AFTER UPDATE ON %s REFERENCING OLD TABLE AS old_usage_logs NEW TABLE AS new_usage_logs FOR EACH STATEMENT EXECUTE FUNCTION usage_key_rollup_capture_update()', target);
    END IF;
    IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=target AND tgname='usage_key_rollup_no_truncate') THEN
        EXECUTE format('CREATE TRIGGER usage_key_rollup_no_truncate BEFORE TRUNCATE ON %s FOR EACH STATEMENT EXECUTE FUNCTION usage_key_rollup_reject_truncate()', target);
    END IF;
END $$;
SELECT install_usage_key_rollup_partition_triggers('usage_logs'::regclass);
DO $$ DECLARE relation regclass; BEGIN
    FOR relation IN SELECT relid FROM pg_partition_tree('usage_logs'::regclass) WHERE level > 0 LOOP
        PERFORM install_usage_key_rollup_partition_triggers(relation);
    END LOOP;
END $$;
CREATE TRIGGER usage_key_rollup_delete AFTER DELETE ON usage_logs FOR EACH ROW EXECUTE FUNCTION usage_key_rollup_capture_change();

-- Only consumers take this lock. Business inserts append events without locking
-- any summary/state row. Rebuild and backfill share this short transaction lock.
CREATE FUNCTION process_usage_key_rollups() RETURNS integer LANGUAGE plpgsql AS $$
DECLARE
    initialized boolean;
    first_hour timestamptz;
    last_hour timestamptz;
    target_hour timestamptz;
    claimed_ids bigint[];
BEGIN
    IF NOT pg_try_advisory_xact_lock(742981311) THEN RETURN 0; END IF;
    SELECT s.initialized INTO initialized FROM usage_key_rollup_state s WHERE id=1;
    IF NOT initialized THEN
        SELECT date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' INTO first_hour FROM usage_logs ORDER BY created_at LIMIT 1;
        SELECT (date_trunc('hour', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') + interval '1 hour' INTO last_hour FROM usage_logs ORDER BY created_at DESC LIMIT 1;
        IF first_hour IS NOT NULL THEN
            IF last_hour-first_hour > interval '20 years' THEN RAISE EXCEPTION 'usage rollup backfill exceeds supported coverage'; END IF;
            INSERT INTO usage_key_rollup_backfill(bucket_start)
            SELECT generate_series(first_hour,last_hour-interval '1 hour',interval '1 hour') ON CONFLICT DO NOTHING;
        END IF;
        UPDATE usage_key_rollup_state SET initialized=true,coverage_start=first_hour,coverage_end=last_hour,updated_at=clock_timestamp() WHERE id=1;
    END IF;
    -- Consume only committed events. No MAX(id) cursor: a lower id may commit late.
    -- Backfill gets one slot per five seconds while live-event lag is healthy.
    IF (SELECT last_backfill_at < clock_timestamp()-interval '5 seconds' FROM usage_key_rollup_state WHERE id=1)
       AND NOT EXISTS(SELECT 1 FROM usage_key_rollup_events WHERE created_at<clock_timestamp()-interval '60 seconds') THEN
        SELECT bucket_start INTO target_hour FROM usage_key_rollup_backfill WHERE completed_at IS NULL ORDER BY bucket_start LIMIT 1;
        IF target_hour IS NOT NULL THEN UPDATE usage_key_rollup_state SET last_backfill_at=clock_timestamp() WHERE id=1; END IF;
    END IF;
    IF target_hour IS NULL THEN SELECT bucket_start INTO target_hour FROM usage_key_rollup_events ORDER BY id LIMIT 1; END IF;
    IF target_hour IS NOT NULL THEN
        SELECT array_agg(id) INTO claimed_ids FROM (
            SELECT id FROM usage_key_rollup_events WHERE bucket_start=target_hour ORDER BY id LIMIT 512 FOR UPDATE
        ) picked;
    ELSE
        SELECT bucket_start INTO target_hour FROM usage_key_rollup_backfill WHERE completed_at IS NULL ORDER BY bucket_start LIMIT 1;
    END IF;
    IF target_hour IS NULL THEN RETURN 0; END IF;
    -- A new READ COMMITTED statement snapshot is taken after the consumer lock.
    DELETE FROM usage_key_hourly_rollups WHERE bucket_start=target_hour;
    INSERT INTO usage_key_hourly_rollups(bucket_start,api_key_id,user_id,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_sum,duration_count)
    SELECT target_hour,api_key_id,user_id,COUNT(*),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),
      COALESCE(SUM(cache_creation_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(total_cost),0),COALESCE(SUM(actual_cost),0),COALESCE(SUM(duration_ms::numeric),0),COUNT(duration_ms)
    FROM usage_logs WHERE created_at>=target_hour AND created_at<target_hour+interval '1 hour' GROUP BY api_key_id,user_id;
    DELETE FROM usage_key_rollup_events WHERE id=ANY(claimed_ids);
    INSERT INTO usage_key_rollup_backfill(bucket_start,completed_at) VALUES(target_hour,clock_timestamp())
    ON CONFLICT(bucket_start) DO UPDATE SET completed_at=EXCLUDED.completed_at;
    UPDATE usage_key_rollup_state SET updated_at=clock_timestamp() WHERE id=1;
    RETURN 1;
END $$;
