-- +goose Up
-- resolve_server_card_waits is the plpgsql twin of domain.DependencyReleasedSQL.
-- 20261113600 recreated it with the pre-6e99b97e4 `deployed` branch: any pr link
-- on prod released the wait, while the Go rule (DeploymentReleasesSQL) keeps a
-- `deployed` edge holding until every own pr link that is not closed is on
-- prod, and ignores review-subject and relates_to links. Server-observed card
-- waits on a target delivered by several MRs woke after the first deploy.
-- Only the `deployed` branch changes; the rest is the 20261113600 body.
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
              -- domain.DeploymentReleasesSQL('bt.id'): an own pr link is on prod
              -- and no other own link (not review-subject, not relates_to) is
              -- still open or merged without a prod deploy.
              WHEN 'deployed' THEN bt.is_shipped OR (EXISTS (
                  SELECT 1 FROM vcs_links dv WHERE dv.task_id = bt.id AND dv.link_type = 'pr'
                    AND COALESCE(dv.metadata->>'role', '') <> 'review'
                    AND COALESCE(dv.metadata->>'relation', '') <> 'relates_to'
                    AND (COALESCE(dv.metadata->'deployed'->>'sha', '') <> ''
                         AND dv.metadata->'deployed'->>'at' IS NOT NULL
                         AND dv.metadata->'deployed'->>'environment' = 'prod'))
                AND NOT EXISTS (
                  SELECT 1 FROM vcs_links dh WHERE dh.task_id = bt.id AND dh.link_type = 'pr'
                    AND COALESCE(dh.metadata->>'role', '') <> 'review'
                    AND COALESCE(dh.metadata->>'relation', '') <> 'relates_to'
                    AND COALESCE(dh.status, '') <> 'closed'
                    AND NOT (COALESCE(dh.metadata->'deployed'->>'sha', '') <> ''
                         AND dh.metadata->'deployed'->>'at' IS NOT NULL
                         AND dh.metadata->'deployed'->>'environment' = 'prod')))
              WHEN 'accepted' THEN bts.category='done'
              ELSE bts.category IN ('done','cancelled') END, false);
END $$;
-- +goose StatementEnd

-- +goose Down
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
