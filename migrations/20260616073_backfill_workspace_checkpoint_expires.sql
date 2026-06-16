-- +goose Up
-- +goose StatementBegin
-- Backfill expires_at for workspace-scoped session-checkpoints that never got a TTL
-- because defaultExpiresAt checked for "session-checkpoint" but agents write "kind:session-checkpoint".
-- Entries older than 7 days get expires_at = NOW() + 1d so CleanExpired removes them on next 6h tick.
-- Entries newer than 7 days get expires_at = created_at + 7d (their correct TTL).
-- Only affects importance_score < 0.5 (excludes promoted checkpoints).
UPDATE memories
SET expires_at = GREATEST(created_at + INTERVAL '7 days', NOW() + INTERVAL '1 day')
WHERE scope = 'workspace'
  AND (
    tags @> ARRAY['kind:session-checkpoint']::text[]
    OR tags @> ARRAY['session-checkpoint']::text[]
  )
  AND expires_at IS NULL
  AND importance_score < 0.5;
-- +goose StatementEnd

-- +goose Down
-- no-op: intentionally irreversible — expired entries will be cleaned up by CleanExpired
