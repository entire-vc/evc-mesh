-- +goose Up
-- Task 1.17a (§1r.A machine gate): GateArmPredicate gains a fifth, optional field —
-- copy_tier ("A" or "B"). copy_tier="B" is a new predicate outcome, refused_copy_tier_b,
-- kept separate from refused_self_serve so the two populations don't get averaged
-- together when the ratio in gate_predicate_log is read (#7084b912). This widens the
-- outcome CHECK constraint to accept it.
--
-- The original constraint (20260906003) was an inline column CHECK with no explicit
-- name, so Postgres assigned it one automatically. Rather than guess that name, this
-- looks it up from pg_constraint — a CHECK constraint on gate_predicate_log whose
-- definition mentions "outcome" — and drops whatever it actually finds. A guessed name
-- that turned out wrong would leave the old (narrower) constraint in place and this
-- migration's own ADD CONSTRAINT would then fail outright with "already exists" under a
-- different name, so this is not defensive polish — it is what makes the migration
-- correct regardless of what goose/Postgres named the original.
-- +goose StatementBegin
DO $$
DECLARE
    old_name text;
BEGIN
    SELECT con.conname INTO old_name
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.conrelid
    WHERE rel.relname = 'gate_predicate_log'
      AND con.contype = 'c'
      AND pg_get_constraintdef(con.oid) LIKE '%outcome%'
    LIMIT 1;

    IF old_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE gate_predicate_log DROP CONSTRAINT %I', old_name);
    END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE gate_predicate_log
    ADD CONSTRAINT gate_predicate_log_outcome_check
    CHECK (outcome IN ('allowed', 'refused_self_serve', 'refused_use_dependency', 'refused_copy_tier_b'));

-- +goose Down
-- Down assumes no 'refused_copy_tier_b' rows exist yet to violate the narrower
-- constraint being restored — true at the moment this migration is written, and this
-- mirrors how every other migration in this tree writes its Down (no guard against
-- rows created after Up ran).
ALTER TABLE gate_predicate_log DROP CONSTRAINT IF EXISTS gate_predicate_log_outcome_check;

ALTER TABLE gate_predicate_log
    ADD CONSTRAINT gate_predicate_log_outcome_check
    CHECK (outcome IN ('allowed', 'refused_self_serve', 'refused_use_dependency'));
