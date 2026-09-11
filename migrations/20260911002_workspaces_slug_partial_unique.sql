-- +goose Up

-- Task #c164a5df (parent #93644be7): workspaces.slug was UNIQUE across ALL
-- rows, live and soft-deleted alike. A soft-deleted workspace (deleted_at
-- SET, row never removed — see WorkspaceRepo.Delete) kept holding its slug
-- forever, so recreating a workspace with the same slug always failed on
-- the uniqueness constraint, and the failure looked identical to "slug
-- genuinely taken" — nothing distinguished "someone else has this" from
-- "you deleted this yourself yesterday".
--
-- Fix: uniqueness among LIVE rows only. The soft-delete path (same PR)
-- renames a row's slug at the moment it is deleted, so this partial index
-- is the enforcement side of that contract — even if a future write path
-- forgets to rename on delete, two live-looking rows can never collide.

DROP INDEX IF EXISTS idx_workspaces_slug;
ALTER TABLE workspaces DROP CONSTRAINT IF EXISTS workspaces_slug_key;

CREATE UNIQUE INDEX idx_workspaces_slug_unique_live
    ON workspaces(slug) WHERE deleted_at IS NULL;

-- Soft-deleted rows are still looked up sometimes (e.g. by their renamed
-- slug) — keep a plain (non-unique) index over ALL rows so those reads
-- don't fall back to a sequential scan now that the old always-unique
-- index is gone.
CREATE INDEX idx_workspaces_slug_all ON workspaces(slug);

-- +goose Down

DROP INDEX IF EXISTS idx_workspaces_slug_all;
DROP INDEX IF EXISTS idx_workspaces_slug_unique_live;

ALTER TABLE workspaces ADD CONSTRAINT workspaces_slug_key UNIQUE (slug);
CREATE INDEX idx_workspaces_slug ON workspaces(slug);
