-- +goose Up
-- +goose StatementBegin

-- Add task.blocking_triage to the default events array and to all existing preference rows
-- that don't already include it. Blocking events are high-signal; opt-in ON is the right default.

ALTER TABLE notification_preferences
    ALTER COLUMN events SET DEFAULT '{task.assigned,task.status_changed,comment.created,task.blocking_triage}';

UPDATE notification_preferences
SET events = array_append(events, 'task.blocking_triage')
WHERE NOT (events @> ARRAY['task.blocking_triage']);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE notification_preferences
    ALTER COLUMN events SET DEFAULT '{task.assigned,task.status_changed,comment.created}';

UPDATE notification_preferences
SET events = array_remove(events, 'task.blocking_triage');

-- +goose StatementEnd
