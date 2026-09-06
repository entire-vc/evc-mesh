-- +goose Up
-- Task #5d3dc714 (audit §3.2b): 40-45% of asks to Pavel were cases the agent could have
-- decided from a rule already written down — an access that was already in keys.env, its
-- own 403 mistaken for a human decision, an approval Pavel had already declined, waiting
-- on someone else's card instead of adding a dependency.
--
-- So arming a gate now requires stating four things, and the server refuses the arm when
-- the stated answers say the agent should just act. This table records EVERY predicate
-- evaluation — allowed and refused alike.
--
-- Why a log table and not a counter: the card's own acceptance asks for the ratio of
-- refusals to successful arms two weeks after rollout. A counter of refusals cannot
-- answer that — it measures how often the guard FIRED, which reads identically whether
-- the guard is doing nothing or quietly preventing everything. Only the pair, with the
-- reasons attached, says whether refusals are landing on asks that deserved refusing.
CREATE TABLE gate_predicate_log (
    id                     UUID PRIMARY KEY,
    task_id                UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    -- Who tried to arm. Taken from the authenticated identity, never from the request
    -- body — same rule as tasks.gate_author (task #4545660b).
    actor_id               UUID NOT NULL,
    actor_type             TEXT NOT NULL CHECK (actor_type IN ('user', 'agent', 'system')),
    -- 'allowed' | 'refused_self_serve' | 'refused_use_dependency'.
    outcome                TEXT NOT NULL
        CHECK (outcome IN ('allowed', 'refused_self_serve', 'refused_use_dependency')),
    -- The four answers, verbatim as stated. Stored even on 'allowed' — a refusal rate
    -- computed only over refusals has no denominator.
    credential_exists      BOOLEAN NOT NULL,
    reversible             BOOLEAN NOT NULL,
    blocked_by_other_task  BOOLEAN NOT NULL,
    customer_visible_now   BOOLEAN NOT NULL,
    -- One line of justification per answer. Free text on purpose: the point is that a
    -- human reviewing the log can see WHY the agent answered as it did, and an enum
    -- would push the real reason back into prose somewhere else.
    credential_reason      TEXT NOT NULL,
    reversible_reason      TEXT NOT NULL,
    blocked_reason         TEXT NOT NULL,
    customer_reason        TEXT NOT NULL,
    -- Which arming path this came from: 'api' (explicit set_human_gate) or 'marker'
    -- (a "❓ Blocking @pavel" comment). Recorded because the two are held to different
    -- standards and a rate that mixes them is uninterpretable — see the service layer.
    source                 TEXT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The acceptance query is "refusals vs allowed over the last N days", so the index is
-- on time, with outcome carried along.
CREATE INDEX idx_gate_predicate_log_created ON gate_predicate_log (created_at DESC, outcome);
CREATE INDEX idx_gate_predicate_log_task ON gate_predicate_log (task_id);

-- +goose Down
DROP TABLE gate_predicate_log;
