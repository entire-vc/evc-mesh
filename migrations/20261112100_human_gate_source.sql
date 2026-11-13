-- +goose Up
-- Regression: which door armed the live human gate. A gate the server armed from a
-- "❓ Blocking" marker may be reclassified hard→soft by the rule that marker gets now
-- (ReconcileMarkerGateClasses); a gate armed through the API (set_human_gate, explicit
-- class) never may. Author and reason alone cannot tell the two apart: an API arm may
-- carry the same ones. NULL = not recorded: armed before this column, or by a raw
-- PATCH/UI flip (task_repo SetHumanGate/Update reset it whenever human_gate changes).
-- human_gate_source_comment_id names the marker comment that armed it (source 'marker'
-- only): a re-ask by the same author with the same question line re-arms from a newer
-- comment, and the reclassification must not apply the verdict it read from the older one.
ALTER TABLE tasks ADD COLUMN human_gate_source text
    CONSTRAINT tasks_human_gate_source_check CHECK (human_gate_source IN ('api', 'marker'));
ALTER TABLE tasks ADD COLUMN human_gate_source_comment_id uuid;

-- +goose Down
ALTER TABLE tasks DROP COLUMN human_gate_source_comment_id;
ALTER TABLE tasks DROP COLUMN human_gate_source;
