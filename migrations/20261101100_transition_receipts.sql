-- +goose Up

-- Opt-in policy for the receipt-bound product done transition. One row per
-- project; a project without a row, or with enabled=false, behaves exactly as
-- before and the new endpoint refuses it. The policy names who may produce a
-- transition (producer_id bound to a credential actor) and who may be named as
-- the independent accepting identity; it carries no blanket exemption.
CREATE TABLE transition_receipt_policies (
    project_id          UUID        PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled             BOOLEAN     NOT NULL DEFAULT false,
    policy_version      INT         NOT NULL DEFAULT 1 CHECK (policy_version > 0),
    -- [{"producer_id": "...", "actor_id": "<uuid>"}]
    allowed_producers   JSONB       NOT NULL DEFAULT '[]',
    allowed_acceptors   UUID[]      NOT NULL DEFAULT '{}',
    -- explicit exemption types usable with an artifact ref: research, trivial
    allowed_exemptions  TEXT[]      NOT NULL DEFAULT '{}',
    updated_by          UUID,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Immutable record of a committed transition. Deliberately without a foreign
-- key to tasks or vcs_links: the evidence is a snapshot, and a later edit or
-- removal of task/VCS metadata must not reach it.
CREATE TABLE task_transition_receipts (
    id                  UUID        PRIMARY KEY,
    task_id             UUID        NOT NULL,
    project_id          UUID        NOT NULL,
    domain              TEXT        NOT NULL,
    task_class          TEXT        NOT NULL,
    operation           TEXT        NOT NULL,
    from_status_id      UUID        NOT NULL,
    from_version        BIGINT      NOT NULL,
    to_status_id        UUID        NOT NULL,
    to_version          BIGINT      NOT NULL,
    producer_id         TEXT        NOT NULL,
    producer_session    TEXT        NOT NULL DEFAULT '',
    source_ref          TEXT        NOT NULL DEFAULT '',
    source_hash         TEXT        NOT NULL DEFAULT '',
    source_trust        TEXT        NOT NULL CHECK (source_trust IN ('claimed')),
    credential_actor_id UUID        NOT NULL,
    credential_actor_type TEXT      NOT NULL,
    policy_version      INT         NOT NULL,
    idempotency_key     TEXT        NOT NULL,
    payload_digest      TEXT        NOT NULL,
    accepting_actor_id  UUID        NOT NULL,
    acceptance_ref      TEXT        NOT NULL DEFAULT '',
    evidence_kind       TEXT        NOT NULL CHECK (evidence_kind IN ('vcs', 'exemption')),
    evidence            JSONB       NOT NULL,
    evidence_hash       TEXT        NOT NULL,
    reason              TEXT        NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (task_id, operation, producer_id, idempotency_key)
);

CREATE INDEX idx_task_transition_receipts_task ON task_transition_receipts (task_id, created_at);

-- +goose StatementBegin
CREATE FUNCTION task_transition_receipts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'task_transition_receipts is append-only' USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER task_transition_receipts_no_change
    BEFORE UPDATE OR DELETE ON task_transition_receipts
    FOR EACH ROW EXECUTE FUNCTION task_transition_receipts_immutable();

-- +goose Down
DROP TRIGGER IF EXISTS task_transition_receipts_no_change ON task_transition_receipts;
DROP FUNCTION IF EXISTS task_transition_receipts_immutable();
DROP TABLE IF EXISTS task_transition_receipts;
DROP TABLE IF EXISTS transition_receipt_policies;
