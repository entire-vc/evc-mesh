-- +goose Up

-- Recall result usage (widget "Память Mesh", Stage 1 unit U3, plan §3
-- «Полезный recall»). One row per signal that an agent used a row i of a
-- recall answer r within W(r) = (t_r, t_r + 2h] of the same session S(r):
--   get_memory  — a get_memory read of i by id or key (signal_ref = that read event);
--   mention     — a comment (finish_task evidence included), a task description,
--                 a Mesh Doc or remember content of the same agent names key(i),
--                 id(i) or its 8-hex prefix (signal_ref = the comment, task,
--                 document or memory revision; ref_kind says which);
--   revision    — the same agent updated or archived i, or wrote the memory that
--                 superseded i (signal_ref = the memory revision).
-- Only rows with source memory or project_knowledge are attributed. Reads of
-- every caller_kind are attributed, and the row keeps the read's caller_kind:
-- a recall answer placed in a prompt by a runner or a hook is counted with its
-- own kind instead of dropping out silently.
-- Written by a background job (postgres.MemoryReadUsageRepo.ProcessOnce), never
-- on a request path. The job re-evaluates a read until its window has closed
-- and then stamps memory_read_events.usage_final_at: a read with the stamp has
-- its final usage rows. Off together with the journal (MEMORY_READ_LOG=false).
CREATE TABLE memory_read_usage (
    read_event_id UUID NOT NULL REFERENCES memory_read_events(id) ON DELETE CASCADE,
    memory_id     UUID NOT NULL,
    signal        TEXT NOT NULL CHECK (signal IN ('get_memory', 'mention', 'revision')),
    ref_kind      TEXT NOT NULL
        CHECK (ref_kind IN ('read_event', 'comment', 'task', 'document', 'memory_revision')),
    signal_ref    UUID NOT NULL,
    ts            TIMESTAMPTZ NOT NULL,
    agent_id      UUID NOT NULL,
    caller_kind   TEXT NOT NULL DEFAULT 'unknown'
        CHECK (caller_kind IN ('agent_tool', 'runner', 'script', 'ui', 'unknown')),
    PRIMARY KEY (read_event_id, memory_id, signal, signal_ref)
);

CREATE INDEX idx_memory_read_usage_ts ON memory_read_usage (ts);
CREATE INDEX idx_memory_read_usage_memory ON memory_read_usage (memory_id);

-- usage_final_at: when the usage job closed the read's window. NULL on a recall
-- read means "still being attributed" (or the job is off).
ALTER TABLE memory_read_events ADD COLUMN usage_final_at TIMESTAMPTZ NULL;

CREATE INDEX idx_memory_read_events_usage_pending ON memory_read_events (ts)
    WHERE usage_final_at IS NULL AND op IN ('recall', 'recall-final', 'recall_graph');

-- +goose Down
DROP INDEX IF EXISTS idx_memory_read_events_usage_pending;
ALTER TABLE memory_read_events DROP COLUMN IF EXISTS usage_final_at;
DROP TABLE IF EXISTS memory_read_usage;
