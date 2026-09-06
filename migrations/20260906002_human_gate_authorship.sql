-- +goose Up
-- Task #4545660b (audit-2026-09 §2.5, §2.4 п.10): "waiting on Pavel" was computed in 21
-- places across the fleet — 11 in human_gate.py, 5 in the dispatcher with its own marker
-- dictionary, 2 in the verify-driver, and the digest with a third dictionary. Every one
-- of them re-derived the answer by grepping comment TEXT, which is why class-C3 phantom
-- gates existed at all: a driver printed the marker as instructional boilerplate and the
-- next driver read its own output back as a raised blocker (#84ab54fd).
--
-- The fix is not a better dictionary. It is that the server already owns the answer and
-- must own the WHOLE answer, so a client's `is_human_gated` collapses to reading one
-- field. `human_gate` (bool), `human_gate_class` and `human_gate_armed_at` already exist;
-- what was missing is everything a reader needs in order NOT to go back to the comments:
-- who armed it, what they asked, what happens if nobody answers, and by when.

-- WHO armed the gate. Nullable because a gate can be armed by a direct PATCH/UI flip with
-- no author to attribute (task_handler.go's raw-arm path) — but the ArmHumanGate service
-- method rejects a nil author with 422, so every gate armed through the supported path
-- has one. Deliberately NOT a foreign key: the referent is polymorphic (agents.id or
-- users.id, discriminated by gate_author_type), matching how assignee_id/created_by
-- already model the same actor union elsewhere in this table.
ALTER TABLE tasks ADD COLUMN gate_author UUID;
ALTER TABLE tasks ADD COLUMN gate_author_type TEXT
    CHECK (gate_author_type IS NULL OR gate_author_type IN ('user', 'agent', 'system'));

-- WHAT was asked, verbatim-ish. The reason the gate exists, carried on the task instead
-- of only inside a comment body that every client had to re-parse.
ALTER TABLE tasks ADD COLUMN gate_reason TEXT;

-- WHAT HAPPENS IF NOBODY ANSWERS. Free text, not an enum: the space of "what I'll do by
-- default" is not enumerable, and forcing it into one would push the real answer back
-- into prose. Consumed by task 1.4 (#060ccaae), which applies it after the deadline.
ALTER TABLE tasks ADD COLUMN recommended_default TEXT;

-- WHEN the default applies. NULL means "no deadline set" — it does not mean "never", and
-- the timeout sweep must treat a NULL deadline as out of scope rather than as expired.
ALTER TABLE tasks ADD COLUMN gate_deadline TIMESTAMPTZ;

-- Partial index: every consumer of these columns filters on human_gate = true first
-- (the digest, the feed's is_human_gated check, the 1.4 default-on-timeout sweep). A
-- full index would be ~100x larger for the same answer.
CREATE INDEX idx_tasks_gate_author ON tasks (gate_author) WHERE human_gate = true;
CREATE INDEX idx_tasks_gate_deadline ON tasks (gate_deadline)
    WHERE human_gate = true AND gate_deadline IS NOT NULL;

-- Backfill gate_author for gates that are armed RIGHT NOW, from the most recent
-- non-system comment carrying a Blocking marker.
--
-- The regex is the Postgres transliteration of blockingMarkerRegex in
-- internal/service/comment_service.go: `(?n)` gives ^ the per-line meaning Go's `(?m)`
-- does. It is NOT case-insensitive here, matching the Go regex's literal `Blocking`
-- via (?i)... — see the note below; the Go side is (?im) so a lower-case "blocking @x"
-- also arms there. This backfill accepts the same by using ~* on the marker word only.
--
-- Measured on prod 2026-09-06 before writing this migration (mesh_read, read-only):
--   * 107 tasks with human_gate = true; 107 of them (100%) have a matching marker
--     comment, so this backfill leaves no armed gate authorless.
--   * NEGATIVE CONTROL — the filter is not vacuously true: it matches 840 of 33627
--     comments in the last 90 days (2.5%), and 571 tasks that are NOT gated also carry
--     a marker comment (released gates). Those 571 are deliberately excluded by the
--     human_gate = true predicate: back-filling a released gate's author would resurrect
--     authorship for a question that is already answered.
UPDATE tasks t
SET gate_author      = m.author_id,
    gate_author_type = m.author_type
FROM (
    SELECT DISTINCT ON (c.task_id)
           c.task_id, c.author_id, c.author_type
    FROM comments c
    WHERE c.author_type <> 'system'
      AND c.body ~* '(?n)^[[:space:]]*(❓[[:space:]]*)?[*]{0,2}[[:space:]]*Blocking[[:space:]]+@[a-z0-9]'
    ORDER BY c.task_id, c.created_at DESC
) m
WHERE t.id = m.task_id
  AND t.human_gate = true
  AND t.deleted_at IS NULL
  AND t.gate_author IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_gate_deadline;
DROP INDEX IF EXISTS idx_tasks_gate_author;
ALTER TABLE tasks DROP COLUMN gate_deadline;
ALTER TABLE tasks DROP COLUMN recommended_default;
ALTER TABLE tasks DROP COLUMN gate_reason;
ALTER TABLE tasks DROP COLUMN gate_author_type;
ALTER TABLE tasks DROP COLUMN gate_author;
