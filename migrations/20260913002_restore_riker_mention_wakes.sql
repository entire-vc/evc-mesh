-- +goose Up
-- Found live during #ba959606's red/green control of MENTION_HANDOFF_ENFORCE=1:
-- @riker no longer passes the mention-handoff gate's capability exemption.
--
-- Confirmed on prod (mesh_read, 2026-09-13):
--   SELECT slug, jsonb_typeof(capabilities), capabilities::text FROM agents
--    WHERE slug='riker' AND deleted_at IS NULL;
--   → riker | array | ["fleet-coordination"]
--
-- 20260906004_mention_wakes_riker_capability.sql set
-- capabilities = {"mention_wakes": true} on this row on 2026-09-06 16:07 UTC
-- (confirmed applied in goose_db_version). Seven days later the same column
-- holds a plain array with a different, unrelated value and no
-- "mention_wakes" key anywhere — the object was replaced wholesale, not
-- merged into.
--
-- Root cause (structural, tracked separately as #33b7d4b7, not fixed here):
-- rules_service.go's UpdateAgentProfile does `agent.Capabilities =
-- profile.Capabilities` — a blind replace — and a SEPARATE consumer
-- (GetMeshConfig's TeamAgentConfig.Capabilities []string, used by workspace
-- config export/import) expects this same jsonb column to hold a plain
-- array of skill-tag strings. The mention gate expects an object. One jsonb
-- value cannot be both shapes at once; whichever write lands last destroys
-- the other's data completely, silently (activity_log has no entity_type='agent'
-- rows at all for the window — there is no audit trail for this).
--
-- This migration restores the known-good state so the gate's documented
-- escape hatch works again. It does NOT fix the underlying write path — a
-- future update_agent_profile/config-import call on Riker's row can destroy
-- this flag again exactly the same way. See #33b7d4b7 for the actual fix.
--
-- Trade-off accepted knowingly: this drops the "fleet-coordination" tag that
-- was in the array. Verified zero code consumers reference that literal
-- (`grep -rn "fleet-coordination"` across the repo is empty) — it was
-- display-only. mention_wakes gates a live, functioning delivery path
-- (Riker's own event-stream listener); the tag does not gate anything.
UPDATE agents
SET capabilities = '{"mention_wakes": true}'::jsonb
WHERE slug = 'riker' AND deleted_at IS NULL
  AND jsonb_typeof(coalesce(capabilities, '{}'::jsonb)) != 'object';

-- Idempotent companion for the case where it's already an object (re-run,
-- or someone else already restored it a different way): merge rather than
-- clobber, same as the original 20260906004 migration did.
UPDATE agents
SET capabilities = capabilities || '{"mention_wakes": true}'::jsonb
WHERE slug = 'riker' AND deleted_at IS NULL
  AND jsonb_typeof(coalesce(capabilities, '{}'::jsonb)) = 'object';

-- +goose Down
UPDATE agents
SET capabilities = capabilities - 'mention_wakes'
WHERE slug = 'riker' AND deleted_at IS NULL
  AND jsonb_typeof(coalesce(capabilities, '{}'::jsonb)) = 'object';
