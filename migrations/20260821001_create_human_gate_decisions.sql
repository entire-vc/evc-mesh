-- +goose Up

-- The third exit from human_gate: "the question was answered, the answer
-- arrived through a different channel" (task #c56339b1, contract
-- docs/human-gate-decision-recorded.md, epic #4036ae02).
--
-- Today human_gate has exactly two exits, and both mean "the ask stopped
-- existing": releaseHumanGate (Pavel commented on the task himself) and
-- releaseHumanGateOnWithdrawal (the marker's own author retracted it). An
-- agent that receives Pavel's answer through Telegram, chat, or in person has
-- no honest way to record that — the only lever it can reach is withdrawal,
-- which asserts "there was never a question" where the truth is "there was a
-- question and it has an answer". That lie is why the same question gets
-- asked again weeks later (#b392f590, contract §1).
--
-- This table is the ledger for that third exit: one immutable row per
-- decision, one immutable row per revocation of a decision. Nothing is ever
-- UPDATEd (trg_human_gate_decisions_append_only below) -- a revocation is a
-- NEW row (revokes_id set) referencing the decision it cancels, never an
-- edit of the decision's own fields. The "revoked_at/revoked_reason" the
-- contract's §3 table shows as if they lived on the decision row are
-- computed at read time from a later revocation row, not stored there.
--
-- provenance is graduated and never averaged (contract §2, P2):
--   direct   -- Pavel acted inside Mesh (his own comment); comment_handler.go
--              derives author_type from the authenticated identity, so this
--              is unforgeable by an agent key by construction.
--   bridged  -- delivered by the Telegram bridge, tied to a real message_id.
--   attested -- an agent transcribed an answer from an external channel.
--              Forgeable by construction (contract §2, P3 -- every agent
--              process on this host shares one UID, so no secret makes this
--              unforgeable here). The control is visibility + revocability,
--              not prevention: attested rows are always named as such
--              wherever they render, and Pavel can revoke one.

-- +goose StatementBegin
DO $$ BEGIN
    CREATE TYPE human_gate_decision_provenance AS ENUM ('direct', 'bridged', 'attested');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$ BEGIN
    CREATE TYPE human_gate_decision_channel AS ENUM ('mesh', 'telegram', 'chat', 'voice', 'in-person');
EXCEPTION WHEN duplicate_object THEN NULL; END $$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS human_gate_decisions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id        UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    -- question_ref: the comment_id of the live "❓ Blocking @pavel" marker
    -- this decision answers (contract §3: "маркер и есть аск"). No FK to
    -- comments -- a comment can be hard-deleted and the decision must
    -- outlive it as a historical record.
    question_ref   UUID,
    canonical_key  TEXT,
    decided_by     UUID NOT NULL,
    provenance     human_gate_decision_provenance,
    channel        human_gate_decision_channel,
    quote          TEXT,
    channel_ref    JSONB,
    recorded_by    UUID,
    revokes_id     UUID REFERENCES human_gate_decisions(id),
    revoked_reason TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A decision row (revokes_id IS NULL) must answer SOMETHING identifiable
    -- and carry the fields the contract requires for it to be a real record,
    -- not a placeholder. A revocation row only needs its revoked_reason.
    CONSTRAINT chk_hgd_decision_shape CHECK (
        revokes_id IS NOT NULL
        OR (
            (question_ref IS NOT NULL OR canonical_key IS NOT NULL)
            AND provenance IS NOT NULL
            AND channel IS NOT NULL
        )
    ),
    -- quote is mandatory for bridged/attested (contract §3: "обязателен для
    -- bridged и attested") -- these are the two provenances an operator or
    -- Pavel might need to independently check against what was actually
    -- said, unlike direct, where the comment itself already carries it.
    CONSTRAINT chk_hgd_quote_required CHECK (
        revokes_id IS NOT NULL
        OR provenance = 'direct'
        OR quote IS NOT NULL
    ),
    CONSTRAINT chk_hgd_revocation_has_reason CHECK (
        revokes_id IS NULL OR revoked_reason IS NOT NULL
    )
);

CREATE INDEX IF NOT EXISTS idx_hgd_task ON human_gate_decisions(task_id);
-- Partial: only decision rows (not revocations) are ever looked up by these
-- keys -- this is exactly the query enforceBlockingTriage's repeat-check
-- (contract §6) runs before arming the flag.
CREATE INDEX IF NOT EXISTS idx_hgd_question_ref
    ON human_gate_decisions(task_id, question_ref) WHERE revokes_id IS NULL;
CREATE INDEX IF NOT EXISTS idx_hgd_canonical_key
    ON human_gate_decisions(task_id, canonical_key) WHERE revokes_id IS NULL;
-- UNIQUE, not just indexed: a decision may be revoked at most once. Without
-- this, two concurrent revocations would both insert and the LEFT JOIN every
-- read query uses to compute "is this decision revoked" would fan out into
-- two rows instead of raising the conflict where it belongs.
CREATE UNIQUE INDEX IF NOT EXISTS idx_hgd_revokes ON human_gate_decisions(revokes_id) WHERE revokes_id IS NOT NULL;

COMMENT ON TABLE human_gate_decisions IS
    'Append-only ledger for the third human_gate exit -- "the question was '
    'answered, the answer arrived through another channel" (task #c56339b1). '
    'A row with revokes_id NULL is a decision; a row with revokes_id set is a '
    'revocation of that decision. Never UPDATEd -- see trg_human_gate_decisions_append_only.';
COMMENT ON COLUMN human_gate_decisions.provenance IS
    'direct (Pavel commented in Mesh, unforgeable) | bridged (Telegram bridge, '
    'tied to a message_id) | attested (agent transcribed -- forgeable by '
    'construction, contract §2 P3; visibility+revocation is the control, not prevention).';

-- Pure INSERT-only ledger: nobody may UPDATE an existing row, including to
-- "add" revoked_at -- a revocation is always a new row (revokes_id set), per
-- contract §3 ("Отзыв — новая строка, никогда не правка существующей").
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION human_gate_decisions_append_only()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION
        'human_gate_decisions is append-only -- insert a revocation row (revokes_id set), never UPDATE'
        USING ERRCODE = 'check_violation';
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_human_gate_decisions_append_only
    BEFORE UPDATE ON human_gate_decisions
    FOR EACH ROW
    EXECUTE FUNCTION human_gate_decisions_append_only();

-- +goose Down

DROP TRIGGER IF EXISTS trg_human_gate_decisions_append_only ON human_gate_decisions;
DROP FUNCTION IF EXISTS human_gate_decisions_append_only();
DROP TABLE IF EXISTS human_gate_decisions;
DROP TYPE IF EXISTS human_gate_decision_channel;
DROP TYPE IF EXISTS human_gate_decision_provenance;
