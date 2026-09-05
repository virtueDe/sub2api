-- Mark exclusive standard groups that belong to the global paid entitlement policy.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS is_paid_entitlement BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS group_is_paid_entitlement
    ON groups (is_paid_entitlement);
