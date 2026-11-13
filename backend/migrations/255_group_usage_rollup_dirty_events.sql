-- Business mutations append invalidations without taking the consumer's lock.
-- An event covers a whole UTC day (not a representative timestamp); consumers
-- and readers include every configured-calendar day intersecting that range.
CREATE TABLE usage_group_rollup_events (
    id bigserial PRIMARY KEY,
    bucket_start timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (bucket_start = date_trunc('day', bucket_start AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
);
CREATE INDEX usage_group_rollup_events_bucket ON usage_group_rollup_events(bucket_start, id);

DROP TRIGGER usage_logs_group_rollup_invalidate_insert ON usage_logs;
DROP TRIGGER usage_logs_group_rollup_invalidate_update ON usage_logs;
DROP TRIGGER usage_logs_group_rollup_invalidate_delete ON usage_logs;
DROP FUNCTION invalidate_group_usage_rollup_state_after_insert();
DROP FUNCTION invalidate_group_usage_rollup_state();

CREATE FUNCTION usage_group_rollup_capture_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_group_rollup_events(bucket_start)
    SELECT DISTINCT date_trunc('day', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
    FROM inserted_usage_logs WHERE group_id IS NOT NULL;
    RETURN NULL;
END $$;

CREATE FUNCTION usage_group_rollup_capture_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_group_rollup_events(bucket_start)
    SELECT DISTINCT date_trunc('day', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
    FROM deleted_usage_logs WHERE group_id IS NOT NULL;
    RETURN NULL;
END $$;

CREATE FUNCTION usage_group_rollup_capture_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_group_rollup_events(bucket_start)
    WITH old_values AS MATERIALIZED (
        SELECT created_at,group_id,actual_cost FROM old_usage_logs WHERE group_id IS NOT NULL
    ), new_values AS MATERIALIZED (
        SELECT created_at,group_id,actual_cost FROM new_usage_logs WHERE group_id IS NOT NULL
    ), changed AS (
        (SELECT * FROM old_values EXCEPT ALL SELECT * FROM new_values)
        UNION ALL
        (SELECT * FROM new_values EXCEPT ALL SELECT * FROM old_values)
    )
    SELECT DISTINCT date_trunc('day', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' FROM changed;
    RETURN NULL;
END $$;

-- Statement transition tables cover bulk DML and cross-partition UPDATEs.
-- Install on leaves as well: a statement addressed to a leaf does not invoke
-- its parent's statement trigger. Inherited row triggers were removed above.
CREATE FUNCTION install_usage_group_rollup_partition_triggers(target regclass) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('LOCK TABLE %s IN SHARE ROW EXCLUSIVE MODE', target);
    IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=target AND tgname='usage_group_rollup_insert') THEN
        EXECUTE format('CREATE TRIGGER usage_group_rollup_insert AFTER INSERT ON %s REFERENCING NEW TABLE AS inserted_usage_logs FOR EACH STATEMENT EXECUTE FUNCTION usage_group_rollup_capture_insert()', target);
    END IF;
    IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=target AND tgname='usage_group_rollup_update') THEN
        EXECUTE format('CREATE TRIGGER usage_group_rollup_update AFTER UPDATE ON %s REFERENCING OLD TABLE AS old_usage_logs NEW TABLE AS new_usage_logs FOR EACH STATEMENT EXECUTE FUNCTION usage_group_rollup_capture_update()', target);
    END IF;
    IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=target AND tgname='usage_group_rollup_delete') THEN
        EXECUTE format('CREATE TRIGGER usage_group_rollup_delete AFTER DELETE ON %s REFERENCING OLD TABLE AS deleted_usage_logs FOR EACH STATEMENT EXECUTE FUNCTION usage_group_rollup_capture_delete()', target);
    END IF;
END $$;
SELECT install_usage_group_rollup_partition_triggers('usage_logs'::regclass);
DO $$ DECLARE relation regclass; BEGIN
    FOR relation IN SELECT relid FROM pg_partition_tree('usage_logs'::regclass) WHERE level > 0 LOOP
        PERFORM install_usage_group_rollup_partition_triggers(relation);
    END LOOP;
END $$;

-- Old leaf-targeted inserts were not captured. Rebuild the derived baseline
-- in bounded days; do not certify old buckets that may have missed changes.
DELETE FROM usage_group_daily_rollups;
UPDATE usage_group_rollup_state SET closed_before=DATE '1970-01-01',
    retained_from=TIMESTAMPTZ '1970-01-01 00:00:00+00',updated_at=clock_timestamp() WHERE id=1;

CREATE FUNCTION process_group_usage_rollups(target_before timestamptz, target_timezone text)
RETURNS integer LANGUAGE plpgsql AS $$
DECLARE
    current_state usage_group_rollup_state%ROWTYPE;
    today_date date := (target_before AT TIME ZONE target_timezone)::date;
    earliest timestamptz;
    first_date date;
    end_date date;
    event_bucket timestamptz;
    claimed_ids bigint[];
    baseline boolean;
BEGIN
    -- Only consumers take this lock. Partition creators use 742981312 and
    -- key-rollup consumers use 742981311; neither is coupled to this worker.
    IF NOT pg_try_advisory_xact_lock(742981313) THEN RETURN 0; END IF;
    IF target_before IS DISTINCT FROM today_date::timestamp AT TIME ZONE target_timezone THEN
        RAISE EXCEPTION 'group rollup target must be a local midnight';
    END IF;
    SELECT * INTO STRICT current_state FROM usage_group_rollup_state WHERE id=1;
    IF current_state.timezone_name IS DISTINCT FROM target_timezone
       OR current_state.closed_before=DATE '1970-01-01' THEN
        SELECT COALESCE(MIN(created_at),target_before) INTO earliest FROM usage_logs;
        DELETE FROM usage_group_daily_rollups;
        current_state.closed_before := LEAST((earliest AT TIME ZONE target_timezone)::date,today_date);
        UPDATE usage_group_rollup_state SET closed_before=current_state.closed_before,
            retained_from=earliest,timezone_name=target_timezone,updated_at=clock_timestamp() WHERE id=1;
    ELSIF current_state.closed_before>today_date THEN
        RAISE EXCEPTION 'group rollup watermark is in the future';
    END IF;

    baseline := current_state.closed_before<today_date;
    IF baseline THEN
        first_date := current_state.closed_before;
        end_date := first_date+1;
        -- Baseline publication never acknowledges events. A writer committing
        -- after this day's source snapshot must still be visible as dirty.
    ELSE
        SELECT bucket_start INTO event_bucket FROM usage_group_rollup_events ORDER BY id LIMIT 1;
        IF event_bucket IS NULL THEN RETURN 0; END IF;
        SELECT array_agg(id) INTO claimed_ids FROM (
            SELECT id FROM usage_group_rollup_events WHERE bucket_start=event_bucket ORDER BY id LIMIT 512 FOR UPDATE
        ) picked;
        first_date := (event_bucket AT TIME ZONE target_timezone)::date;
        end_date := LEAST(((event_bucket+interval '24 hours'-interval '1 microsecond') AT TIME ZONE target_timezone)::date+1,today_date);
    END IF;

    IF first_date<end_date THEN
        -- Claim before reading source. Every later commit keeps its event.
        -- Converting each midnight separately preserves 23/25-hour DST days.
        WITH expected AS MATERIALIZED (
            SELECT (created_at AT TIME ZONE target_timezone)::date AS bucket_date,
                group_id,COALESCE(SUM(actual_cost),0) AS actual_cost
            FROM usage_logs WHERE group_id IS NOT NULL
                AND created_at>=first_date::timestamp AT TIME ZONE target_timezone
                AND created_at<end_date::timestamp AT TIME ZONE target_timezone
            GROUP BY 1,2
        ), removed AS (
            DELETE FROM usage_group_daily_rollups r
            WHERE r.bucket_date>=first_date AND r.bucket_date<end_date
                AND NOT EXISTS(SELECT 1 FROM expected e WHERE e.bucket_date=r.bucket_date AND e.group_id=r.group_id)
            RETURNING 1
        )
        INSERT INTO usage_group_daily_rollups AS r(bucket_date,group_id,actual_cost,computed_at)
        SELECT bucket_date,group_id,actual_cost,clock_timestamp() FROM expected
        ON CONFLICT(bucket_date,group_id) DO UPDATE SET actual_cost=EXCLUDED.actual_cost,computed_at=EXCLUDED.computed_at
        WHERE r.actual_cost IS DISTINCT FROM EXCLUDED.actual_cost;
    END IF;
    -- Tail/future events may be acknowledged: reads use source there and every
    -- future baseline day is always computed from all source rows.
    DELETE FROM usage_group_rollup_events WHERE id=ANY(claimed_ids);
    UPDATE usage_group_rollup_state SET
        closed_before=CASE WHEN baseline THEN end_date ELSE closed_before END,
        updated_at=clock_timestamp() WHERE id=1;
    RETURN 1;
END $$;
