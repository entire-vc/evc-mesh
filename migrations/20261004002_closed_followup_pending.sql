-- +goose Up

-- #db1c6c7a (P2 of #9b712414, contract approved by Garfield). Visible queue
-- for a follow-up finding whose delivery could not be completed inline:
-- until P2, a read error while searching for the finding's root was treated
-- as "dedup clean" — the finding was silently dropped or, worse, a burst of
-- duplicate roots could appear once the database came back. Now the delivery
-- path retries inline, and when that fails it parks the finding HERE:
-- a pending row plus a visible system comment on the source card, re-delivered
-- by the 5-minute reconcile job until it lands or a human is asked for.
--
-- comment_id is the PK: one comment carries exactly one finding delivery, so
-- one comment can queue at most one pending row (Enqueue is an upsert that
-- never resets attempts — re-enqueueing must not buy a finding a fresh
-- 10-attempt budget).
--
-- No FK to comments/tasks by design, same standalone-store discipline as
-- closed_followup_roots (P1): the queue must stay writable and readable when
-- the tables around it are failing — that is its whole reason to exist.
-- attempts counts RECONCILE attempts (the inline retries are not reconciles);
-- ListDue selects attempts < 10, the 10th failure escalates to a human and
-- the row stays as the paper trail.
--
-- escalated_at is the escalation's completion stamp: NULL until the
-- «нужен человек» notice has actually been WRITTEN on the source card. A row
-- at the budget with NULL escalated_at stays in the due set, listed for the
-- notice alone — the counter must never freeze an untold human out of the
-- queue (codex-review P1, round 3: the count used to commit before the
-- notice, and a failed notice write lost the escalation permanently).
--
-- noticed_at is the park's visibility stamp: NULL until the «не подтверждена»
-- notice has landed. Every reconcile pass that works the row retries that
-- notice while it is NULL — a queued finding its commenter cannot see is a
-- queue, not a delivery (codex-review P1, round 4). A row past the budget
-- leaves the due set only when BOTH stamps are set: retiring on escalated_at
-- alone stranded a park notice that never landed (round 5).
CREATE TABLE IF NOT EXISTS closed_followup_pending (
    comment_id     uuid        NOT NULL,
    source_task_id uuid        NOT NULL,
    finding_key    text        NOT NULL,
    attempts       int         NOT NULL DEFAULT 0,
    last_error     text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    escalated_at   timestamptz,
    noticed_at     timestamptz,
    PRIMARY KEY (comment_id)
);

-- The reconcile job's selection predicate, oldest first: partial index on
-- exactly the rows ListDue reads (below the budget, or past it with either
-- notice still undelivered), so the every-5-minutes scan stays O(due) even
-- though the table keeps completed rows forever.
CREATE INDEX IF NOT EXISTS idx_closed_followup_pending_due
    ON closed_followup_pending (created_at)
    WHERE attempts < 10 OR escalated_at IS NULL OR noticed_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS closed_followup_pending;
