-- +goose Up
-- Consent recorded at connect time (spec 03 §3): which capabilities the
-- workspace admin accepted, when, and who. Additive and nullable: a row with no
-- consent recorded (everything connected before this migration) keeps working,
-- and ResolveStatus treats NULL as "declared capabilities".
ALTER TABLE integration_configs
    ADD COLUMN IF NOT EXISTS consented_capabilities JSONB,
    ADD COLUMN IF NOT EXISTS consented_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS consented_by UUID;

-- +goose Down
ALTER TABLE integration_configs
    DROP COLUMN IF EXISTS consented_by,
    DROP COLUMN IF EXISTS consented_at,
    DROP COLUMN IF EXISTS consented_capabilities;
