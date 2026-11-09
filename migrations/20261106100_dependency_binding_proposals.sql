-- +goose Up

-- A proposal to move a blocks edge from one condition to another. It is only a
-- suggestion with its sources; the edge changes when the dependent card's owner
-- confirms it through the API (CAS on the next-action generation, one-shot).
CREATE TABLE dependency_binding_proposals (
    id              uuid PRIMARY KEY,
    edge_id         uuid NOT NULL REFERENCES task_dependencies(id) ON DELETE CASCADE,
    old_condition   text NOT NULL,
    new_condition   text NOT NULL,
    sources         jsonb NOT NULL,
    scope           text NOT NULL,
    base_generation text NOT NULL,
    proposed_by     uuid NOT NULL,
    status          text NOT NULL DEFAULT 'pending',
    confirmed_by    uuid NULL,
    confirmed_at    timestamptz NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_binding_proposal_status CHECK (status IN ('pending', 'applied')),
    CONSTRAINT chk_binding_proposal_change CHECK (old_condition <> new_condition)
);
CREATE INDEX idx_binding_proposals_edge ON dependency_binding_proposals (edge_id, status);

-- +goose Down
DROP TABLE IF EXISTS dependency_binding_proposals;
