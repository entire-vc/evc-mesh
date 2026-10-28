-- +goose Up
-- Typed turn outcomes (spec an internal ticket §4): what an agent's turn ended with is
-- recorded as state, not parsed out of a comment. Three tables and one column:
--   decision_requests — a question to another agent (replaces [decision] cards);
--   task_waits        — the one active wait of an agent on a card;
--   turn_outcomes     — append-only journal of turn results, read by the runner;
--   tasks.result_kind — what kind of result a closed card delivered.
-- Commands that write these rows are a later unit; this one fixes the schema the
-- server, the MCP and the runner build against.

CREATE TABLE decision_requests (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id           UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    from_agent_id     UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    to_agent_id       UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    question          TEXT NOT NULL CHECK (char_length(btrim(question)) BETWEEN 1 AND 4000),
    state             TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'answered', 'withdrawn')),
    answer_comment_id UUID REFERENCES comments(id) ON DELETE SET NULL,
    answered_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Asking yourself is not a question: decide and log it on the card.
    CONSTRAINT decision_requests_not_self CHECK (from_agent_id <> to_agent_id),
    CONSTRAINT decision_requests_answered_at CHECK ((state = 'answered') = (answered_at IS NOT NULL))
);

-- The same open question from the same agent to the same addressee on the same
-- card is one request: a repeat reuses it instead of opening a second one.
CREATE UNIQUE INDEX decision_requests_one_open
    ON decision_requests (task_id, from_agent_id, to_agent_id, md5(lower(btrim(question))))
    WHERE state = 'open';
CREATE INDEX idx_decision_requests_to_open ON decision_requests (to_agent_id) WHERE state = 'open';

CREATE TABLE task_waits (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id             UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id            UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    session_id          TEXT CHECK (char_length(session_id) <= 200),
    kind                TEXT NOT NULL CHECK (kind IN ('card', 'mr', 'pipeline', 'time', 'decision', 'human')),
    -- card: the dependency edge carries the condition; the wait names the target.
    target_task_id      UUID REFERENCES tasks(id) ON DELETE SET NULL,
    target_condition    TEXT,
    -- mr: the link is optional, the coordinates are copied so the wait still
    -- reads after the link is removed.
    vcs_link_id         UUID REFERENCES vcs_links(id) ON DELETE SET NULL,
    provider            TEXT,
    project_path        TEXT,
    mr_iid              INTEGER,
    -- pipeline
    pipeline_id         BIGINT,
    required_jobs       TEXT[] NOT NULL DEFAULT '{}',
    -- mr: merged|green|changed; pipeline: finished|green.
    until_state         TEXT,
    -- time
    until_at            TIMESTAMPTZ,
    decision_request_id UUID REFERENCES decision_requests(id) ON DELETE SET NULL,
    human_gate_ref      TEXT CHECK (char_length(human_gate_ref) <= 200),
    note                TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at         TIMESTAMPTZ,
    resolution          TEXT CHECK (resolution IN ('met', 'superseded', 'cancelled', 'expired', 'invalid')),
    CONSTRAINT task_waits_resolution CHECK ((resolved_at IS NULL) = (resolution IS NULL)),
    CONSTRAINT task_waits_not_self CHECK (target_task_id IS DISTINCT FROM task_id),
    -- Targets that cannot disappear under the row are required here; card and
    -- decision targets are foreign keys that may be nulled, so the service checks
    -- them when the wait is declared.
    CONSTRAINT task_waits_mr_target CHECK (kind <> 'mr' OR (
        provider IS NOT NULL AND project_path IS NOT NULL AND mr_iid IS NOT NULL
        AND until_state IN ('merged', 'green', 'changed'))),
    CONSTRAINT task_waits_pipeline_target CHECK (kind <> 'pipeline' OR (
        project_path IS NOT NULL AND pipeline_id IS NOT NULL
        AND until_state IN ('finished', 'green'))),
    CONSTRAINT task_waits_time_target CHECK (kind <> 'time' OR until_at IS NOT NULL)
);

-- One active wait per (task, agent). A new wait first resolves the old one as
-- superseded; two concurrent declarations cannot both stay active.
CREATE UNIQUE INDEX task_waits_one_active ON task_waits (task_id, agent_id) WHERE resolved_at IS NULL;
CREATE INDEX idx_task_waits_target_task ON task_waits (target_task_id) WHERE resolved_at IS NULL;
CREATE INDEX idx_task_waits_until_at ON task_waits (until_at) WHERE resolved_at IS NULL AND kind = 'time';

CREATE TABLE turn_outcomes (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    session_id  TEXT CHECK (char_length(session_id) <= 200),
    turn_id     TEXT CHECK (char_length(turn_id) <= 200),
    kind        TEXT NOT NULL CHECK (kind IN ('next', 'wait', 'done', 'review', 'handoff', 'cancelled')),
    wait_id     UUID REFERENCES task_waits(id),
    result_kind TEXT CHECK (result_kind IN ('change', 'defect', 'research', 'content', 'ops')),
    note        TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 4096),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT turn_outcomes_wait_ref CHECK ((kind = 'wait') = (wait_id IS NOT NULL))
);

CREATE INDEX idx_turn_outcomes_session ON turn_outcomes (task_id, session_id, created_at DESC);

-- Append-only: a recorded outcome is history the runner already acted on.
-- Rows still go away with their task (ON DELETE CASCADE); only UPDATE is refused.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION turn_outcomes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'turn_outcomes is append-only';
END $$;
-- +goose StatementEnd
CREATE TRIGGER turn_outcomes_append_only BEFORE UPDATE ON turn_outcomes
    FOR EACH ROW EXECUTE FUNCTION turn_outcomes_append_only();

ALTER TABLE tasks ADD COLUMN result_kind TEXT
    CHECK (result_kind IN ('change', 'defect', 'research', 'content', 'ops'));

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS result_kind;
DROP TRIGGER IF EXISTS turn_outcomes_append_only ON turn_outcomes;
DROP FUNCTION IF EXISTS turn_outcomes_append_only();
DROP TABLE IF EXISTS turn_outcomes;
DROP TABLE IF EXISTS task_waits;
DROP TABLE IF EXISTS decision_requests;
