-- +goose Up

-- Task #060ccaae (1.4 default-on-timeout): a soft-classified gate that names a
-- recommended_default and a gate_deadline gets that default APPLIED — not just
-- unfrozen (that is the older, narrower human_gate_soft_timeout.go mechanism) — once
-- the deadline passes with no answer. The application is recorded as a real row in
-- human_gate_decisions (task #c56339b1's ledger), same as any other answer, so the
-- question shows up as resolved everywhere that table is read (digest, task history,
-- ListByTask) rather than needing a fourth place to special-case it.
--
-- 'default_applied' is deliberately its own provenance, not reused from direct/
-- bridged/attested: those three all mean "a human actually answered, through some
-- channel" (contract §2, P2 — "never averaged or collapsed to a single trusted
-- bucket"). This one means the OPPOSITE — nobody answered, and the gate's own
-- pre-stated fallback fired instead. Collapsing it into 'attested' (the closest
-- existing value) would misrepresent a non-answer as an agent-transcribed answer.
-- +goose StatementBegin
ALTER TYPE human_gate_decision_provenance ADD VALUE IF NOT EXISTS 'default_applied';
-- +goose StatementEnd

-- +goose StatementBegin
COMMENT ON COLUMN human_gate_decisions.provenance IS
    'direct (Pavel commented in Mesh, unforgeable) | bridged (Telegram bridge, '
    'tied to a message_id) | attested (agent transcribed -- forgeable by '
    'construction, contract §2 P3; visibility+revocation is the control, not '
    'prevention) | default_applied (nobody answered by gate_deadline; the stated '
    'recommended_default was applied mechanically -- task #060ccaae).';
-- +goose StatementEnd

-- +goose Down

-- PostgreSQL does not support removing a single enum value. Down intentionally
-- leaves 'default_applied' in the type -- the same accepted limitation as every
-- other ADD VALUE migration in this repo (see 20260227028, 20260613071).
