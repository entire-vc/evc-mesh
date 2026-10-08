-- +goose Up
ALTER TABLE activity_log
 ADD COLUMN event_id uuid,
 ADD COLUMN source text NOT NULL DEFAULT 'api',
 ADD COLUMN reason text NOT NULL DEFAULT '',
 ADD COLUMN session_id uuid,
 ADD COLUMN correlation_id uuid,
 ADD COLUMN old_version bigint,
 ADD COLUMN new_version bigint,
 ADD COLUMN lease_generation bigint,
 ADD COLUMN previous_holder uuid,
 ADD COLUMN trigger_task_id uuid;
CREATE UNIQUE INDEX activity_log_event_id_unique ON activity_log(event_id) WHERE event_id IS NOT NULL;
CREATE INDEX activity_log_durable_task_audit ON activity_log(entity_id, created_at) WHERE event_id IS NOT NULL;

CREATE TABLE task_event_outbox (
 id uuid PRIMARY KEY,
 task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 task_version bigint NOT NULL,
 action text NOT NULL,
 message jsonb NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 available_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 delivered_at timestamptz,
 UNIQUE(task_id, task_version, action)
);
CREATE INDEX task_event_outbox_pending ON task_event_outbox(available_at, created_at) WHERE delivered_at IS NULL;

-- +goose Down
DROP TABLE task_event_outbox;
DROP INDEX activity_log_durable_task_audit;
DROP INDEX activity_log_event_id_unique;
ALTER TABLE activity_log DROP COLUMN event_id, DROP COLUMN source, DROP COLUMN reason,
 DROP COLUMN session_id, DROP COLUMN correlation_id, DROP COLUMN old_version,
 DROP COLUMN new_version, DROP COLUMN lease_generation, DROP COLUMN previous_holder,
 DROP COLUMN trigger_task_id;
