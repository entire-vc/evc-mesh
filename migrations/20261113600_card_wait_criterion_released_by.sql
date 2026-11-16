-- +goose Up
-- A card wait may name the result it waits for (criterion, with
-- target_condition artifact_published), and a met card wait records what
-- released its edge: trusted evidence, the target's closure without that
-- evidence, or the target's own state (domain.ReleasedBy*).
ALTER TABLE task_waits ADD COLUMN criterion text, ADD COLUMN released_by text,
    ADD COLUMN target_status text, ADD COLUMN target_closed_by text, ADD COLUMN target_closed_at timestamptz;
ALTER TABLE task_waits ADD CONSTRAINT task_waits_criterion
    CHECK (criterion IS NULL OR (kind = 'card' AND char_length(criterion) BETWEEN 1 AND 500));
ALTER TABLE task_waits ADD CONSTRAINT task_waits_released_by
    CHECK (released_by IS NULL OR (kind = 'card'
        AND released_by IN ('evidence', 'closure_without_evidence', 'target_state')));

-- The target's status name and its latest status change (who, when): the
-- closure snapshot a met card wait keeps and an already_met refusal names.
-- +goose StatementBegin
CREATE FUNCTION card_wait_target_closure(target uuid,
    OUT status text, OUT category text, OUT closed_by text, OUT closed_at timestamptz)
LANGUAGE sql STABLE AS $$
    SELECT s.name, s.category::text, mv.closer,
           COALESCE(mv.created_at, t.status_changed_at, t.completed_at)
      FROM tasks t JOIN task_statuses s ON s.id = t.status_id
      LEFT JOIN LATERAL (
           SELECT al.created_at, CASE
               WHEN al.actor_type = 'agent' THEN (SELECT name FROM agents WHERE id = al.actor_id)
               WHEN al.actor_type = 'user' THEN (SELECT display_name FROM users WHERE id = al.actor_id)
               ELSE al.actor_type::text END AS closer
             FROM activity_log al
            WHERE al.entity_type = 'task' AND al.entity_id = t.id
              AND al.action = 'task.moved' AND al.changes ? 'status'
            ORDER BY al.created_at DESC LIMIT 1) mv ON true
     WHERE t.id = target;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION resolve_server_card_waits(target uuid) RETURNS void
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    -- SQL twin of domain.DependencyReleasedSQL / DependencyReleasedBySQL.
    -- Updating ONLY waits avoids target->source row locks opposite the
    -- declaration's source->target order.
    UPDATE task_waits w SET resolved_at=now(), resolution='met', resolve_cause='target_condition_met',
        released_by = CASE
            WHEN d.condition<>'task_done' AND d.evidence_status='trusted'
                 AND d.condition_met_at IS NOT NULL AND COALESCE(btrim(d.evidence_url),'')<>'' THEN 'evidence'
            WHEN d.condition IN ('access_ready','contract_ready','artifact_published') THEN 'closure_without_evidence'
            ELSE 'target_state' END,
        target_status=c.status, target_closed_by=c.closed_by, target_closed_at=c.closed_at
    FROM tasks bt JOIN task_statuses bts ON bts.id=bt.status_id,
         task_dependencies d, card_wait_target_closure(target) c
    WHERE w.server_observed AND w.resolved_at IS NULL AND w.target_task_id=target
      AND bt.id=target AND bt.deleted_at IS NULL
      AND d.task_id=w.task_id AND d.depends_on_task_id=target
      AND d.dependency_type='blocks' AND d.condition=COALESCE(w.target_condition,'task_done')
      AND COALESCE(
          (d.condition<>'task_done' AND d.evidence_status='trusted'
             AND d.condition_met_at IS NOT NULL AND COALESCE(btrim(d.evidence_url),'')<>'')
          OR CASE d.condition
              WHEN 'shipped' THEN bt.is_shipped OR bts.category='done'
              WHEN 'deployed' THEN bt.is_shipped OR EXISTS (
                  SELECT 1 FROM vcs_links dv WHERE dv.task_id=bt.id AND dv.link_type='pr'
                    AND COALESCE(dv.metadata->'deployed'->>'sha','')<>''
                    AND dv.metadata->'deployed'->>'at' IS NOT NULL
                    AND dv.metadata->'deployed'->>'environment'='prod')
              WHEN 'accepted' THEN bts.category='done'
              ELSE bts.category IN ('done','cancelled') END, false);
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION resolve_server_card_waits(target uuid) RETURNS void
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    -- SQL twin of domain.DependencyReleasedSQL. Updating ONLY waits avoids
    -- target->source row locks opposite the declaration's source->target order.
    UPDATE task_waits w SET resolved_at=now(), resolution='met', resolve_cause='target_condition_met'
    FROM tasks bt JOIN task_statuses bts ON bts.id=bt.status_id,
         task_dependencies d
    WHERE w.server_observed AND w.resolved_at IS NULL AND w.target_task_id=target
      AND bt.id=target AND bt.deleted_at IS NULL
      AND d.task_id=w.task_id AND d.depends_on_task_id=target
      AND d.dependency_type='blocks' AND d.condition=COALESCE(w.target_condition,'task_done')
      AND COALESCE(
          (d.condition<>'task_done' AND d.evidence_status='trusted'
             AND d.condition_met_at IS NOT NULL AND COALESCE(btrim(d.evidence_url),'')<>'')
          OR CASE d.condition
              WHEN 'shipped' THEN bt.is_shipped OR bts.category='done'
              WHEN 'deployed' THEN bt.is_shipped OR EXISTS (
                  SELECT 1 FROM vcs_links dv WHERE dv.task_id=bt.id AND dv.link_type='pr'
                    AND COALESCE(dv.metadata->'deployed'->>'sha','')<>''
                    AND dv.metadata->'deployed'->>'at' IS NOT NULL
                    AND dv.metadata->'deployed'->>'environment'='prod')
              WHEN 'accepted' THEN bts.category='done'
              ELSE bts.category IN ('done','cancelled') END, false);
END $$;
-- +goose StatementEnd
DROP FUNCTION IF EXISTS card_wait_target_closure(uuid);
ALTER TABLE task_waits DROP CONSTRAINT IF EXISTS task_waits_released_by,
    DROP CONSTRAINT IF EXISTS task_waits_criterion;
ALTER TABLE task_waits DROP COLUMN IF EXISTS target_closed_at, DROP COLUMN IF EXISTS target_closed_by,
    DROP COLUMN IF EXISTS target_status, DROP COLUMN IF EXISTS released_by, DROP COLUMN IF EXISTS criterion;
