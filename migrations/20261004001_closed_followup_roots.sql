-- +goose Up

-- #5194afd4 (P1 of #9b712414, contract approved by Garfield). Persistent
-- identity of a finding raised on a closed card: one (source card, finding)
-- pair maps to exactly one follow-up root for the pair's whole life, so a
-- repeat after the root was closed reopens THAT root instead of opening a
-- second card (the measured defect: #f0d4539b closed → same text → #06b275a8).
--
-- PK (source_task_id, finding_key) is the atomicity: claimers race with
-- INSERT ... ON CONFLICT DO NOTHING, the loser reads the winner's root_task_id
-- and takes the repeat branch. Additive only — no backfill: follow-up roots
-- created before this table exist without a row, and a repeat of their text
-- claims a fresh key and opens one extra card, once, by design.
--
-- reopen_count is NOT a lifetime total: it counts reopens inside the current
-- 24h window anchored at last_reopened_at (TryReopen resets it to 1 when the
-- last reopen is older than the window). Two columns are enough for the
-- storm limit "≥3 reopens in 24h → stop reopening, comment only" and keep the
-- contract's table shape exactly as approved.
CREATE TABLE IF NOT EXISTS closed_followup_roots (
    source_task_id    uuid        NOT NULL,
    finding_key       text        NOT NULL,
    root_task_id      uuid        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    reopen_count      int         NOT NULL DEFAULT 0,
    last_reopened_at  timestamptz NULL,
    PRIMARY KEY (source_task_id, finding_key)
);

-- The reopen-claim ledger (codex-review P2, MR !1078, rounds 9→10): one row
-- per storm-limit slot TryReopen TAKES. Compensation deletes the claim's OWN
-- row and recomputes reopen_count / last_reopened_at from the surviving rows
-- of the window — so every claim is independently compensable. A single pin
-- column on the root row (round 9) held only the LATEST claim's token: a
-- claim displaced by a later one could not give its slot back, and
-- overlapping failed reopens burned the storm budget on attempts rather than
-- delivered reopens — the exact defect compensation exists to prevent (round
-- 4). The ledger removes displacement as a class while keeping the other
-- half of the round-9 guarantee: deleting OUR row can never free a slot that
-- was not ours. No FK to closed_followup_roots by design (same additive,
-- standalone identity store); Delete on the root removes its claims with it.
CREATE TABLE IF NOT EXISTS closed_followup_reopen_claims (
    claim_id        uuid        NOT NULL,
    source_task_id  uuid        NOT NULL,
    finding_key     text        NOT NULL,
    claimed_at      timestamptz NOT NULL,
    PRIMARY KEY (claim_id)
);

CREATE INDEX IF NOT EXISTS idx_closed_followup_reopen_claims_root
    ON closed_followup_reopen_claims (source_task_id, finding_key, claimed_at);

-- +goose Down
DROP TABLE IF EXISTS closed_followup_reopen_claims;
DROP TABLE IF EXISTS closed_followup_roots;
