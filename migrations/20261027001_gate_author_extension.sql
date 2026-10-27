-- +goose Up

-- An extension actor (Keep) may arm a human gate on a card it opened, so the gate
-- author and the predicate log must accept the third actor type next to
-- user/agent/system (actor_type got 'extension' in 20261014004). Clearing and
-- deciding a gate stay closed to extensions at the route allowlist.
ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_gate_author_type_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_gate_author_type_check
    CHECK (gate_author_type IS NULL OR gate_author_type IN ('user', 'agent', 'system', 'extension'));

ALTER TABLE gate_predicate_log DROP CONSTRAINT IF EXISTS gate_predicate_log_actor_type_check;
ALTER TABLE gate_predicate_log ADD CONSTRAINT gate_predicate_log_actor_type_check
    CHECK (actor_type IN ('user', 'agent', 'system', 'extension'));

-- +goose Down

-- NOT VALID: rows an extension already wrote keep their author; only new writes
-- are held to the old set again.
ALTER TABLE gate_predicate_log DROP CONSTRAINT IF EXISTS gate_predicate_log_actor_type_check;
ALTER TABLE gate_predicate_log ADD CONSTRAINT gate_predicate_log_actor_type_check
    CHECK (actor_type IN ('user', 'agent', 'system')) NOT VALID;

ALTER TABLE tasks DROP CONSTRAINT IF EXISTS tasks_gate_author_type_check;
ALTER TABLE tasks ADD CONSTRAINT tasks_gate_author_type_check
    CHECK (gate_author_type IS NULL OR gate_author_type IN ('user', 'agent', 'system')) NOT VALID;
