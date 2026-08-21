-- +goose Up
-- gate_class mechanism (task #b1d5c742, contract docs/human-gate-decision-recorded.md §5).
-- human_gate_class classifies an ARMED gate as 'hard' (never released by timeout — money,
-- irreversible actions, external comms) or 'soft' (auto-released by timeout, but the
-- release does NOT answer the question — see SweepExpiredSoftGates in the service layer).
-- DEFAULT 'hard' is deliberate fail-closed: every existing gate and every gate armed
-- through the normal marker flow starts hard until something explicitly reclassifies it
-- (policy of WHICH questions are soft is a separate, not-yet-built card).
ALTER TABLE tasks ADD COLUMN human_gate_class TEXT NOT NULL DEFAULT 'hard'
    CHECK (human_gate_class IN ('hard', 'soft'));

-- When the gate was last armed (human_gate flipped false->true). NULL for a task that has
-- never been armed. Used by the soft-timeout sweep to find gates past their window; a hard
-- gate is excluded from that sweep by the human_gate_class predicate alone, never by this
-- timestamp, so no observation window can accidentally release one.
ALTER TABLE tasks ADD COLUMN human_gate_armed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE tasks DROP COLUMN human_gate_armed_at;
ALTER TABLE tasks DROP COLUMN human_gate_class;
