-- psql -v task_id='<uuid>' -f scripts/audit/task-transition.sql
-- A task-scoped 24-hour / 500-entry window. Activity is durable independently
-- of the event feed TTL. No checkout credentials or arbitrary payloads selected.
BEGIN READ ONLY;
SET LOCAL statement_timeout = '5s';
SELECT id AS event_id, created_at, action, actor_id, actor_type,
       source, reason, session_id, correlation_id, old_version, new_version,
       lease_generation, previous_holder, trigger_task_id
FROM activity_log
WHERE entity_type = 'task' AND entity_id = :'task_id'::uuid
  AND event_id IS NOT NULL
  AND created_at >= now() - interval '24 hours' AND created_at <= now()
  AND action IN ('task.moved','task.assigned','task.checkout_lease_expired',
                 'task.checkout_unleased_returned','task.checkout_released_auto')
ORDER BY created_at DESC, id DESC
LIMIT 500;
SELECT id AS event_id, task_version, action, attempts, available_at, delivered_at
FROM task_event_outbox
WHERE task_id = :'task_id'::uuid
  AND created_at >= now() - interval '24 hours' AND created_at <= now()
ORDER BY created_at DESC, id DESC
LIMIT 500;
COMMIT;
