-- +goose Up
-- Which repositories an agent may work in. Both columns are
-- NULL until the first write: NULL means "not configured", and the runner keeps
-- its legacy list. A configured empty array means "no repository at all".
-- The pair is written only by PUT /agents/{id}/repository-access, which bumps
-- the revision by one under a row lock (compare-and-swap on the old revision).
-- No generic agent UPDATE names these columns, and agentSelectCols does not
-- read them, so a binary built before this migration is unaffected.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS repository_access JSONB,
    ADD COLUMN IF NOT EXISTS repository_access_revision BIGINT;

ALTER TABLE agents
    ADD CONSTRAINT agents_repository_access_shape CHECK (
        -- Written so no branch evaluates to NULL: a NULL CHECK passes.
        (repository_access IS NULL) = (repository_access_revision IS NULL)
        AND (repository_access IS NULL
             OR (jsonb_typeof(repository_access) = 'array' AND repository_access_revision >= 1))
    );

-- +goose Down
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_repository_access_shape;
ALTER TABLE agents
    DROP COLUMN IF EXISTS repository_access_revision,
    DROP COLUMN IF EXISTS repository_access;
