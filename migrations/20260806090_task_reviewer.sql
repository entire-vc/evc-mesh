-- +goose Up
-- Adds a Reviewer role to tasks, separate from Assignee. A custom user_ref field
-- could already store "who reviews this" but custom fields never trigger
-- notifications — the reviewer had no way to learn they were assigned or that
-- their task reached review. reviewer_id/reviewer_type follow the same
-- untyped-FK pattern as assignee_id/assignee_type (can point to users or agents).
--
-- Written idempotently on purpose. This migration was already applied by hand on a
-- self-hosted instance from an unmerged branch, before the branch was lost and the
-- host rolled back to a main that did not contain it. goose keys on version_id and
-- keeps no checksum, so on that instance version 20260806090 is already recorded and
-- this body will simply be skipped — but any environment where the columns exist while
-- the version row does NOT (a hand-run ALTER, a restored dump, a partially applied
-- deploy) would otherwise fail the whole migration and take the API down on boot,
-- since cmd/api runs goose.Up at startup and log.Fatalf's on error.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS reviewer_id UUID;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS reviewer_type assignee_type;

CREATE INDEX IF NOT EXISTS idx_tasks_reviewer ON tasks(reviewer_id, reviewer_type)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_tasks_reviewer;
ALTER TABLE tasks DROP COLUMN IF EXISTS reviewer_type;
ALTER TABLE tasks DROP COLUMN IF EXISTS reviewer_id;
