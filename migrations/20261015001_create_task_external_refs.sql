-- +goose Up
-- Binding of a card to an object in an external system (spec 03 §8, 06 §4): the
-- external side (e.g. an approval request) names itself with ref and pins its
-- content with hash. The hash is write-once — it is what makes the later
-- task.gate.decided answer about THAT request and not a rewritten one.
CREATE TABLE IF NOT EXISTS task_external_refs (
    task_id UUID PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    ref     TEXT NOT NULL,
    hash    TEXT NOT NULL,
    set_by  UUID,
    set_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS task_external_refs;
