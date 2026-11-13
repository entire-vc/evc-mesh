-- +goose Up
-- Regression: archiving a memory by hand is a recorded change, not a silent flag
-- flip. POST /memories/:id/archive and /unarchive bump the memory's version and write a
-- revision row carrying the reason, so the history answers "who took this out of recall
-- and why". The action CHECK from 20260821002 only knew created/updated/forgotten, and
-- that inline CHECK got PostgreSQL's default name memory_revisions_action_check.
ALTER TABLE memory_revisions DROP CONSTRAINT IF EXISTS memory_revisions_action_check;
ALTER TABLE memory_revisions ADD CONSTRAINT memory_revisions_action_check
    CHECK (action IN ('created', 'updated', 'forgotten', 'archived', 'unarchived'));

-- A person can archive too (a workspace owner/admin, a project member user), and
-- actor_agent_id cannot name them. actor_user_id records the user behind a hand
-- archive; a user deleted later leaves the revision with the column cleared.
ALTER TABLE memory_revisions ADD COLUMN IF NOT EXISTS actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE memory_revisions DROP COLUMN IF EXISTS actor_user_id;
-- Archive revisions are history; Down keeps them rather than deleting audit rows, so
-- the narrower CHECK is restored NOT VALID: it binds new writes only.
ALTER TABLE memory_revisions DROP CONSTRAINT IF EXISTS memory_revisions_action_check;
ALTER TABLE memory_revisions ADD CONSTRAINT memory_revisions_action_check
    CHECK (action IN ('created', 'updated', 'forgotten')) NOT VALID;
