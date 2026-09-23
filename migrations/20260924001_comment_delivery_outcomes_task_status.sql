-- +goose Up

-- #ed60c795. The single "no_queue_path" verdict is split into not_assignee /
-- status_not_fed / task_gated / task_scheduled, and the status_not_fed hint
-- has to name WHERE the card is parked ("sits in backlog"). The task may move
-- after the comment is written, so the category is recorded with the verdict
-- rather than looked up at read time. Nullable and additive: rows written
-- before this column read back with NULL, and old code ignores the column.
ALTER TABLE comment_delivery_outcomes ADD COLUMN IF NOT EXISTS task_status_category TEXT NULL;

-- +goose Down
ALTER TABLE comment_delivery_outcomes DROP COLUMN IF EXISTS task_status_category;
