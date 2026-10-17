-- +goose Up
-- Allow-list: an explicit row overrides the trust-level default
-- (builtin/official/verified allowed, community/custom not).
CREATE TABLE instance_extension_policy (
    extension_id VARCHAR(64) PRIMARY KEY,
    allowed      BOOLEAN NOT NULL,
    updated_by   UUID,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Single-row instance flags; the CHECK keeps it a singleton.
CREATE TABLE instance_extension_config (
    id          BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    auto_update BOOLEAN NOT NULL DEFAULT false,
    updated_by  UUID,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO instance_extension_config (id) VALUES (true);

CREATE TABLE instance_extension_journal (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_id     UUID,
    extension_id VARCHAR(64) NOT NULL DEFAULT '',
    action       VARCHAR(32) NOT NULL,
    details      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_instance_extension_journal_created ON instance_extension_journal (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS instance_extension_journal;
DROP TABLE IF EXISTS instance_extension_config;
DROP TABLE IF EXISTS instance_extension_policy;
