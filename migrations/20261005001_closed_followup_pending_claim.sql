-- +goose Up

-- #a2368528, follow-up to the codex-review R7 finding on !1083: ListDue
-- selects without a lock, so once the api runs more than one replica two
-- reconcile passes can work the same pending row at once — a duplicate
-- system notice, and for a closed root a double-counted reopen slot for one
-- finding. The lease column makes the claim atomic: a pass takes a row only
-- when
--   UPDATE ... WHERE claimed_until IS NULL OR claimed_until <= now()
-- affects exactly that row, and the lease outlives one pass (TTL 10 min >
-- the 5-min pass context) so a crashed pass's rows come back on their own.
-- Additive and nullable: the running single replica ignores it until the
-- claim call ships. The lease covers a whole row unit — no progress write
-- releases it mid-row (a MarkNoticed release once reopened the overlap
-- window right before delivery); the reconcile loop hands it back at the
-- unit's end, so a surviving row keeps its retry cadence instead of
-- waiting out the TTL.
ALTER TABLE closed_followup_pending
    ADD COLUMN IF NOT EXISTS claimed_until timestamptz NULL;

-- +goose Down
ALTER TABLE closed_followup_pending
    DROP COLUMN IF EXISTS claimed_until;
