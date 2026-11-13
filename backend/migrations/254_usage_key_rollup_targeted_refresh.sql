-- Reduce derived-statistics writes without changing billing or source history.
-- Preserve durable dirty events and exact aggregation; avoid whole-hour churn.
CREATE OR REPLACE FUNCTION usage_key_rollup_capture_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO usage_key_rollup_events(bucket_start,api_key_id,user_id)
    WITH old_values AS MATERIALIZED (
        SELECT date_trunc('hour',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
            api_key_id,user_id,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_ms
        FROM old_usage_logs
    ), new_values AS MATERIALIZED (
        SELECT date_trunc('hour',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS bucket_start,
            api_key_id,user_id,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_ms
        FROM new_usage_logs
    ), changed AS (
        (SELECT * FROM old_values EXCEPT ALL SELECT * FROM new_values)
        UNION ALL
        (SELECT * FROM new_values EXCEPT ALL SELECT * FROM old_values)
    )
    SELECT DISTINCT bucket_start,api_key_id,user_id FROM changed;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION process_usage_key_rollups() RETURNS integer LANGUAGE plpgsql AS $$
DECLARE
    initialized boolean;
    first_hour timestamptz;
    last_hour timestamptz;
    target_hour timestamptz;
    claimed_ids bigint[];
    full_refresh boolean;
BEGIN
    IF NOT pg_try_advisory_xact_lock(742981311) THEN RETURN 0; END IF;
    SELECT s.initialized INTO initialized FROM usage_key_rollup_state s WHERE id=1;
    IF NOT initialized THEN
        SELECT date_trunc('hour',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' INTO first_hour FROM usage_logs ORDER BY created_at LIMIT 1;
        SELECT (date_trunc('hour',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour' INTO last_hour FROM usage_logs ORDER BY created_at DESC LIMIT 1;
        IF first_hour IS NOT NULL THEN
            IF last_hour-first_hour > interval '20 years' THEN RAISE EXCEPTION 'usage rollup backfill exceeds supported coverage'; END IF;
            INSERT INTO usage_key_rollup_backfill(bucket_start)
            SELECT generate_series(first_hour,last_hour-interval '1 hour',interval '1 hour') ON CONFLICT DO NOTHING;
        END IF;
        UPDATE usage_key_rollup_state SET initialized=true,coverage_start=first_hour,coverage_end=last_hour,updated_at=clock_timestamp() WHERE id=1;
    END IF;
    IF (SELECT last_backfill_at<clock_timestamp()-interval '5 seconds' FROM usage_key_rollup_state WHERE id=1)
       AND NOT EXISTS(SELECT 1 FROM usage_key_rollup_events WHERE created_at<clock_timestamp()-interval '60 seconds') THEN
        SELECT bucket_start INTO target_hour FROM usage_key_rollup_backfill WHERE completed_at IS NULL ORDER BY bucket_start LIMIT 1;
        IF target_hour IS NOT NULL THEN UPDATE usage_key_rollup_state SET last_backfill_at=clock_timestamp() WHERE id=1; END IF;
    END IF;
    IF target_hour IS NULL THEN SELECT bucket_start INTO target_hour FROM usage_key_rollup_events ORDER BY id LIMIT 1; END IF;
    IF target_hour IS NULL THEN
        SELECT bucket_start INTO target_hour FROM usage_key_rollup_backfill WHERE completed_at IS NULL ORDER BY bucket_start LIMIT 1;
    END IF;
    IF target_hour IS NULL THEN RETURN 0; END IF;
    SELECT array_agg(id) INTO claimed_ids FROM (
        SELECT id FROM usage_key_rollup_events WHERE bucket_start=target_hour ORDER BY id LIMIT 512 FOR UPDATE
    ) picked;
    -- A never-completed hour must establish its entire baseline, including
    -- historical rows that predate event capture. Live refreshes touch only
    -- the identities of the exact claimed event IDs.
    SELECT NOT EXISTS(SELECT 1 FROM usage_key_rollup_backfill WHERE bucket_start=target_hour AND completed_at IS NOT NULL) INTO full_refresh;
    WITH identities AS MATERIALIZED (
        SELECT DISTINCT api_key_id,user_id FROM usage_key_rollup_events WHERE id=ANY(claimed_ids)
    ), expected AS MATERIALIZED (
        SELECT target_hour AS bucket_start,l.api_key_id,l.user_id,COUNT(*) AS requests,
            COALESCE(SUM(input_tokens),0) AS input_tokens,COALESCE(SUM(output_tokens),0) AS output_tokens,
            COALESCE(SUM(cache_creation_tokens),0) AS cache_creation_tokens,COALESCE(SUM(cache_read_tokens),0) AS cache_read_tokens,
            COALESCE(SUM(total_cost),0) AS total_cost,COALESCE(SUM(actual_cost),0) AS actual_cost,
            COALESCE(SUM(duration_ms::numeric),0) AS duration_sum,COUNT(duration_ms) AS duration_count
        FROM usage_logs l
        WHERE full_refresh AND l.created_at>=target_hour AND l.created_at<target_hour+interval '1 hour'
        GROUP BY l.api_key_id,l.user_id
        UNION ALL
        -- Keep each live aggregate correlated to its identity. A plain join
        -- can become a whole-hour scan when PL/pgSQL selects a generic plan.
        SELECT target_hour,i.api_key_id,i.user_id,a.*
        FROM identities i CROSS JOIN LATERAL (
            SELECT COUNT(*) AS requests,COALESCE(SUM(l.input_tokens),0) AS input_tokens,
                COALESCE(SUM(l.output_tokens),0) AS output_tokens,COALESCE(SUM(l.cache_creation_tokens),0) AS cache_creation_tokens,
                COALESCE(SUM(l.cache_read_tokens),0) AS cache_read_tokens,COALESCE(SUM(l.total_cost),0) AS total_cost,
                COALESCE(SUM(l.actual_cost),0) AS actual_cost,COALESCE(SUM(l.duration_ms::numeric),0) AS duration_sum,COUNT(l.duration_ms) AS duration_count
            FROM usage_logs l
            WHERE l.api_key_id=i.api_key_id AND l.user_id=i.user_id
                AND l.created_at>=target_hour AND l.created_at<target_hour+interval '1 hour'
            HAVING COUNT(*)>0
        ) a WHERE NOT full_refresh
    ), removed AS (
        DELETE FROM usage_key_hourly_rollups r
        WHERE r.bucket_start=target_hour
            AND (full_refresh OR EXISTS(SELECT 1 FROM identities i WHERE i.api_key_id=r.api_key_id AND i.user_id=r.user_id))
            AND NOT EXISTS(SELECT 1 FROM expected e WHERE e.api_key_id=r.api_key_id AND e.user_id=r.user_id)
        RETURNING 1
    )
    INSERT INTO usage_key_hourly_rollups AS r(bucket_start,api_key_id,user_id,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_sum,duration_count)
    SELECT * FROM expected
    ON CONFLICT(bucket_start,api_key_id,user_id) DO UPDATE SET
        requests=EXCLUDED.requests,input_tokens=EXCLUDED.input_tokens,output_tokens=EXCLUDED.output_tokens,
        cache_creation_tokens=EXCLUDED.cache_creation_tokens,cache_read_tokens=EXCLUDED.cache_read_tokens,
        total_cost=EXCLUDED.total_cost,actual_cost=EXCLUDED.actual_cost,
        duration_sum=EXCLUDED.duration_sum,duration_count=EXCLUDED.duration_count,computed_at=clock_timestamp()
    WHERE ROW(r.requests,r.input_tokens,r.output_tokens,r.cache_creation_tokens,r.cache_read_tokens,r.total_cost,r.actual_cost,r.duration_sum,r.duration_count)
        IS DISTINCT FROM ROW(EXCLUDED.requests,EXCLUDED.input_tokens,EXCLUDED.output_tokens,EXCLUDED.cache_creation_tokens,EXCLUDED.cache_read_tokens,EXCLUDED.total_cost,EXCLUDED.actual_cost,EXCLUDED.duration_sum,EXCLUDED.duration_count);
    -- Never delete unclaimed events: their writers may have committed after
    -- the aggregation snapshot, including a lower sequence ID committing late.
    DELETE FROM usage_key_rollup_events WHERE id=ANY(claimed_ids);
    IF full_refresh THEN
        INSERT INTO usage_key_rollup_backfill(bucket_start,completed_at) VALUES(target_hour,clock_timestamp())
        ON CONFLICT(bucket_start) DO UPDATE SET completed_at=EXCLUDED.completed_at;
    END IF;
    UPDATE usage_key_rollup_state SET updated_at=clock_timestamp() WHERE id=1;
    RETURN 1;
END $$;
