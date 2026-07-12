-- +goose Up
-- Stashes the assignee that held a task before a SetReviewer rule (applyReviewAssignee)
-- reassigned it to the reviewer/lead on entering review. Restored by MoveTask when the
-- task bounces back out of review without an explicit assignee_id in the request —
-- otherwise it silently strands on the reviewer instead of returning to the executor.
ALTER TABLE tasks ADD COLUMN pre_review_assignee_id UUID;
ALTER TABLE tasks ADD COLUMN pre_review_assignee_type assignee_type;

-- +goose Down
ALTER TABLE tasks DROP COLUMN IF EXISTS pre_review_assignee_type;
ALTER TABLE tasks DROP COLUMN IF EXISTS pre_review_assignee_id;
