-- +goose Up
-- pre_review_assignee_id now records the author on EVERY entry into review (rules
-- author/not_author need it). Only a reviewer rotation may be undone on bounce-out,
-- so that fact lives in its own flag; the restore path keys on it, not on the stash.
ALTER TABLE tasks ADD COLUMN pre_review_restore BOOLEAN NOT NULL DEFAULT FALSE;
UPDATE tasks SET pre_review_restore = TRUE WHERE pre_review_assignee_type IS NOT NULL;

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS pre_review_restore;
