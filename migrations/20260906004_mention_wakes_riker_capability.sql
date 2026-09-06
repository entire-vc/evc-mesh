-- RENUMBERED 20260906003 -> 20260906004 before merge (Garfield, 2026-09-06).
-- It collided with 20260906003_gate_predicate_log.sql on the sibling branch
-- garfield/gate-predicate (MR !868), open at the same time.
--
-- MEASURED, not assumed — I first wrote here that goose would silently skip the second
-- migration, then tested it and was wrong. goose fails LOUD and fail-closed:
--
--   panic: goose: duplicate version 20260906003 detected:
--       .../20260906003_mention_wakes_riker_capability.sql
--       .../20260906003_gate_predicate_log.sql
--
-- and NOTHING applies — gate_predicate_log was not created either (count=0). So the real
-- consequence is a dead migration step, which the CI migrate-gate turns into a blocked
-- deploy rather than a half-migrated prod. Bad, but honest about itself.
--
-- Worth keeping the reason anyway: neither branch's own testing could catch this. Each
-- verified its chain against main plus its own file, and the colliding migration was
-- never on main. A green run that cannot see the sibling branch cannot see the
-- collision — so on a day when two migrations are authored in parallel, the check has to
-- be run over the UNION of the open branches, not per-branch.

-- +goose Up
-- Task #9d8f7606 (audit §3.1) added a mention-handoff gate
-- (internal/service/comment_mention_handoff_gate.go) that refuses an
-- add_comment @-mentioning an agent lane with no queue path and no recent
-- accompanying assign_task/create_subtask. The gate reads one escape hatch
-- from data: an agent whose own `capabilities` JSON carries
-- `{"mention_wakes": true}` is exempt, because a fleet-side channel already
-- wakes it on a bare mention without any Mesh-side queue state.
--
-- Confirmed live on prod, read-only, 2026-09-06 (mesh_read):
--   SELECT name, slug, coalesce(capabilities::text,'(null)') FROM agents
--    WHERE deleted_at IS NULL AND (capabilities::text ILIKE '%mention%' OR slug='riker');
--   → Riker|riker|{}
-- The flag is set on NOBODY. Riker is the one agent whose mentions actually
-- get delivered today (its own event-stream listener spawns a session on
-- task.mentioned; every other lane is polling-driven and genuinely is not
-- woken by a bare mention). Shipping the gate without this backfill makes
-- its first act break the one mention-delivery path it exists to preserve.
--
-- WHY RIKER AND ONLY RIKER, AND WHY THIS IS A SNAPSHOT NOT A LAW: this is the
-- fleet roster as measured 2026-09-06, not a permanent architectural fact.
-- Which lane listens for mentions is controlled by fleet-ops config (a
-- coordinator agent is switched between the mention-listening mode and the
-- ordinary polling mode via `/switch <agent> feeder|dispatcher`), and the
-- Mesh API has no access to that config and must not hardcode a roster in
-- Go source (see the long comment on mentionWakesCapabilityKey). This
-- migration is the one-time bridge: it records today's roster as a data
-- fact on the agent's own row. If the roster ever changes — another lane
-- takes over the mention-listening role, or Riker stops — WHOEVER MAKES
-- THAT CHANGE must update capabilities.mention_wakes accordingly; this
-- migration does not, and cannot, keep itself in sync with a config file it
-- has no way to observe.
--
-- Scoped to slug='riker' only, and only merges the one key: an agent's
-- capabilities may already carry other fields (no_lane, etc. — see
-- registry_stamp_is_documentation_not_a_gate) that this must not disturb.
UPDATE agents
SET capabilities = coalesce(capabilities, '{}'::jsonb) || '{"mention_wakes": true}'::jsonb
WHERE slug = 'riker' AND deleted_at IS NULL;

-- +goose Down
UPDATE agents
SET capabilities = capabilities - 'mention_wakes'
WHERE slug = 'riker' AND deleted_at IS NULL;
