-- +goose Up
-- #33b7d4b7: agents.capabilities is written by two incompatible consumers —
-- the mention-handoff gate expects an object ({"mention_wakes": true}),
-- UpdateAgentProfile's config export/import expects a plain string array.
-- UpdateAgentProfile blind-replaces rather than merges, so whichever write
-- lands last destroys the other shape entirely, silently (no activity_log
-- trail — see 20260913002_restore_riker_mention_wakes.sql, the stopgap data
-- repair this migration follows up on).
--
-- Fix: mention_wakes moves to its own column. It has exactly one reader
-- (comment_mention_handoff_gate.go's agentMentionAlreadyWakes) and no longer
-- shares a write path with the array-shaped capabilities consumers.
--
-- DEFAULT false, no backfill: as of 2026-09-13 no lane in the fleet has a
-- live mention-wake path (Riker moved from mesh-dispatcher, the only runtime
-- that ever listened, to fiddler — see canon
-- canon-riker-mention-wake-closed-fiddler-migration). Restoring true for
-- Riker here would re-introduce the exact "documented guarantee that
-- guarantees nothing" defect this fix exists to prevent. Whoever re-enables
-- a live fleet-side listener for some agent in the future sets this column
-- true on that agent's row then.
ALTER TABLE agents ADD COLUMN mention_wakes boolean NOT NULL DEFAULT false;

-- Drop the now-dead key from any row that still carries it as a jsonb
-- object (in practice: riker, restored by 20260913002). Scoped to
-- jsonb_typeof=='object' so an array-shaped capabilities value (the other,
-- incompatible consumer) is left untouched.
UPDATE agents
SET capabilities = capabilities - 'mention_wakes'
WHERE jsonb_typeof(coalesce(capabilities, '{}'::jsonb)) = 'object'
  AND capabilities ? 'mention_wakes';

-- +goose Down
ALTER TABLE agents DROP COLUMN mention_wakes;
