-- Keep generation details for the same lifetime as their usage/error records.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS image_generation JSONB;
ALTER TABLE ops_error_logs ADD COLUMN IF NOT EXISTS image_generation JSONB;
