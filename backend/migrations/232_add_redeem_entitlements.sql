-- Preserve the paid-card entitlement snapshot on each redeem code.
ALTER TABLE redeem_codes
    ADD COLUMN IF NOT EXISTS entitlement_profile VARCHAR(50) NOT NULL DEFAULT 'none';

ALTER TABLE redeem_codes
    ADD COLUMN IF NOT EXISTS entitlement_group_ids JSONB NOT NULL DEFAULT '[]'::jsonb;

UPDATE redeem_codes
SET entitlement_group_ids = '[]'::jsonb
WHERE entitlement_group_ids IS NULL;
