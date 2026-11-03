-- +goose Up

-- Product-done enforcement (canary). Default off per project, separate from the
-- foundation `enabled` flag: enabling the typed endpoint must not turn the guard
-- on by itself, so a policy that was enabled before this migration stays
-- unguarded until someone sets enforce_done explicitly.
ALTER TABLE transition_receipt_policies
    ADD COLUMN enforce_done BOOLEAN NOT NULL DEFAULT false;

-- Immutable acceptance intent: written by the accepting identity under its own
-- credential (acceptor_actor_id comes from authentication, never from a request
-- body) and bound to one task, class, from-status/version, target status and
-- the evidence snapshot hash (MR head/ship, or exemption artifact hash). No
-- foreign key to tasks/vcs_links: it is a snapshot like the receipt.
CREATE TABLE task_acceptance_intents (
    id                  UUID        PRIMARY KEY,
    task_id             UUID        NOT NULL,
    project_id          UUID        NOT NULL,
    domain              TEXT        NOT NULL,
    task_class          TEXT        NOT NULL,
    operation           TEXT        NOT NULL,
    from_status_id      UUID        NOT NULL,
    from_version        BIGINT      NOT NULL,
    to_status_id        UUID        NOT NULL,
    evidence_kind       TEXT        NOT NULL CHECK (evidence_kind IN ('vcs', 'exemption')),
    evidence            JSONB       NOT NULL,
    evidence_hash       TEXT        NOT NULL,
    deploy_ref          TEXT        NOT NULL DEFAULT '',
    live_ref            TEXT        NOT NULL DEFAULT '',
    result_author_id    UUID,
    acceptor_actor_id   UUID        NOT NULL,
    acceptor_actor_type TEXT        NOT NULL,
    acceptor_role       TEXT        NOT NULL CHECK (acceptor_role IN ('reviewer', 'lead')),
    policy_version      INT         NOT NULL,
    reason              TEXT        NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_task_acceptance_intents_task ON task_acceptance_intents (task_id, created_at);

-- Revocation is a separate append-only row so the intent itself never changes.
CREATE TABLE task_acceptance_intent_revocations (
    intent_id   UUID        PRIMARY KEY REFERENCES task_acceptance_intents(id),
    revoked_by  UUID        NOT NULL,
    reason      TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE FUNCTION task_acceptance_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'acceptance intent records are append-only' USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER task_acceptance_intents_no_change
    BEFORE UPDATE OR DELETE ON task_acceptance_intents
    FOR EACH ROW EXECUTE FUNCTION task_acceptance_append_only();
CREATE TRIGGER task_acceptance_intent_revocations_no_change
    BEFORE UPDATE OR DELETE ON task_acceptance_intent_revocations
    FOR EACH ROW EXECUTE FUNCTION task_acceptance_append_only();

-- A receipt now also records the intent it consumed and the transaction that
-- wrote it. The guard below accepts only a receipt written by the transaction
-- that moves the task, so an old receipt can never be replayed into a new move.
ALTER TABLE task_transition_receipts
    ADD COLUMN acceptance_intent_id UUID REFERENCES task_acceptance_intents(id),
    ADD COLUMN commit_txid BIGINT NOT NULL DEFAULT txid_current(),
    ADD COLUMN acting_role TEXT NOT NULL DEFAULT 'producer';
-- One intent authorises one receipt.
CREATE UNIQUE INDEX uq_task_transition_receipts_intent
    ON task_transition_receipts (acceptance_intent_id) WHERE acceptance_intent_id IS NOT NULL;

-- Barrier on the shared write boundary. Every route that writes tasks.status_id
-- or tasks.project_id (MoveTask/UpdateTransition, generic Update, MoveToProject,
-- task_move_batch remap, parked_wait, typed outcome, VCS auto-done, auto
-- transition, recurring/template/create) reaches this trigger, as does raw SQL
-- under the application role. In a project with enforce_done, a task may enter a
-- done-category status only in a transaction that also wrote a receipt bound to
-- a non-revoked acceptance intent for exactly this task/status/version.
-- Deferred, so the typed path may update the task row first and insert the
-- receipt after it inside the same transaction.
-- +goose StatementBegin
CREATE FUNCTION enforce_product_done_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    enforced boolean;
    is_done  boolean;
    cur      tasks%ROWTYPE;
BEGIN
    SELECT * INTO cur FROM tasks WHERE id = NEW.id;
    IF NOT FOUND OR cur.deleted_at IS NOT NULL THEN
        RETURN NULL;
    END IF;
    -- A project with no policy row has nothing to FOR SHARE, so a first-time
    -- enable could commit between this read and the commit of the move. The
    -- policy writer takes the same advisory lock exclusively; the share lock
    -- is held to the end of this transaction, so either the writer committed
    -- before we read (we see the row) or it waits for us.
    PERFORM pg_advisory_xact_lock_shared(hashtextextended('transition_receipt_policy:' || cur.project_id::text, 0));
    -- FOR SHARE: a concurrent policy update (row lock) serialises with this
    -- commit check, so enabling cannot race past an in-flight move.
    SELECT p.enforce_done INTO enforced FROM transition_receipt_policies p
        WHERE p.project_id = cur.project_id FOR SHARE;
    IF NOT COALESCE(enforced, false) THEN
        RETURN NULL;
    END IF;
    -- FOR SHARE: the category cannot be flipped to done by a concurrent
    -- transaction between this read and the commit of the task move; the
    -- category guard below takes the conflicting row lock before it looks.
    SELECT s.category = 'done' INTO is_done FROM task_statuses s WHERE s.id = cur.status_id FOR SHARE;
    IF NOT COALESCE(is_done, false) THEN
        RETURN NULL;
    END IF;
    -- Only an entry into done is guarded: a task already done in the same
    -- project keeps its status through unrelated edits.
    IF TG_OP = 'UPDATE' THEN
        IF OLD.status_id = NEW.status_id AND OLD.project_id = NEW.project_id THEN
            RETURN NULL;
        END IF;
    END IF;
    IF EXISTS (
        SELECT 1 FROM task_transition_receipts r
        JOIN task_acceptance_intents i ON i.id = r.acceptance_intent_id
        WHERE r.task_id = cur.id AND r.commit_txid = txid_current()
          AND r.to_status_id = cur.status_id AND r.to_version = cur.version
          AND r.project_id = cur.project_id
          AND i.task_id = cur.id AND i.to_status_id = cur.status_id
          AND i.acceptor_actor_id = r.accepting_actor_id
          AND NOT EXISTS (SELECT 1 FROM task_acceptance_intent_revocations v WHERE v.intent_id = i.id)
    ) THEN
        RETURN NULL;
    END IF;
    RAISE EXCEPTION 'product_done_receipt_required: task % may enter done only through a receipt-bound transition', cur.id
        USING ERRCODE = 'check_violation';
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER tasks_product_done_guard
    AFTER INSERT OR UPDATE OF status_id, project_id ON tasks
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION enforce_product_done_receipt();

-- Re-categorising a status as done would move every task in it into done
-- without touching tasks. Refuse it in an enforced project.
-- +goose StatementBegin
CREATE FUNCTION enforce_product_done_status_category() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.category = 'done' AND OLD.category IS DISTINCT FROM 'done' THEN
        -- Lock the status row first, then look at its tasks with a fresh
        -- snapshot: a task move that already read the old category holds
        -- FOR SHARE on this row, so we wait for it and then see its task.
        PERFORM 1 FROM task_statuses WHERE id = NEW.id FOR NO KEY UPDATE;
        -- The policy check below reads state that a first-time or later enable
        -- can change before this transaction commits. UpsertPolicy takes the
        -- same advisory lock exclusively, so the enable waits for this change.
        PERFORM pg_advisory_xact_lock_shared(hashtextextended('transition_receipt_policy:' || NEW.project_id::text, 0));
    END IF;
    IF NEW.category = 'done' AND OLD.category IS DISTINCT FROM 'done'
       AND EXISTS (SELECT 1 FROM transition_receipt_policies p WHERE p.project_id = NEW.project_id AND p.enforce_done)
       AND EXISTS (SELECT 1 FROM tasks t WHERE t.status_id = NEW.id AND t.deleted_at IS NULL) THEN
        RAISE EXCEPTION 'product_done_receipt_required: status % cannot become done while it holds tasks', NEW.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER task_statuses_product_done_category_guard
    BEFORE UPDATE OF category ON task_statuses
    FOR EACH ROW EXECUTE FUNCTION enforce_product_done_status_category();

-- +goose Down
DROP TRIGGER IF EXISTS task_statuses_product_done_category_guard ON task_statuses;
DROP FUNCTION IF EXISTS enforce_product_done_status_category();
DROP TRIGGER IF EXISTS tasks_product_done_guard ON tasks;
DROP FUNCTION IF EXISTS enforce_product_done_receipt();
DROP INDEX IF EXISTS uq_task_transition_receipts_intent;
-- DROP COLUMN does not fire the append-only row trigger; receipts keep their data.
ALTER TABLE task_transition_receipts
    DROP COLUMN IF EXISTS acting_role,
    DROP COLUMN IF EXISTS commit_txid,
    DROP COLUMN IF EXISTS acceptance_intent_id;
DROP TRIGGER IF EXISTS task_acceptance_intent_revocations_no_change ON task_acceptance_intent_revocations;
DROP TRIGGER IF EXISTS task_acceptance_intents_no_change ON task_acceptance_intents;
DROP TABLE IF EXISTS task_acceptance_intent_revocations;
DROP TABLE IF EXISTS task_acceptance_intents;
DROP FUNCTION IF EXISTS task_acceptance_append_only();
ALTER TABLE transition_receipt_policies DROP COLUMN IF EXISTS enforce_done;
