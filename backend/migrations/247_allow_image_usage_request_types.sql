-- Images execution mode is independent of the stream/openai_ws_mode fields.
-- Historical async calls cannot be identified reliably; do not relabel old rows.
ALTER TABLE usage_logs
    DROP CONSTRAINT IF EXISTS usage_logs_request_type_check;

ALTER TABLE usage_logs
    ADD CONSTRAINT usage_logs_request_type_check
    CHECK (request_type >= 0 AND request_type <= 7);

COMMENT ON COLUMN usage_logs.request_type IS '0=unknown, 1=sync, 2=stream, 3=ws_v2, 4=cyber, 5=live, 6=image_sync, 7=image_async';
COMMENT ON COLUMN ops_error_logs.request_type IS '0=unknown, 1=sync, 2=stream, 3=ws_v2, 4=cyber, 5=live, 6=image_sync, 7=image_async';
