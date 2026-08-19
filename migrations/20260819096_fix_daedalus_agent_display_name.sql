-- +goose Up
-- Data fix for agent 684bd684-10e6-4329-9875-04846a1845c0, two columns of one row.
--
-- 1. name: "Deadalus" -> "Daedalus". A typo. The canonical spelling is used everywhere
--    else — his lane config, his tmux session, his GitHub account (daedalus-mb), and the
--    fleet's shared docs. Any code that resolves a member by display name has to special
--    case the typo or silently miss him; four independent alias maps grew for exactly that.
--
-- 2. slug: "evc-mesh-dev" -> "daedalus". This is the half that actually breaks routing.
--    @-mentions resolve through agents.slug (comment_service.go:990, agentSvc.GetBySlug),
--    so "@daedalus" — the spelling documented in the team directory and used by everyone —
--    resolves to nobody. Measured on prod over 25 days: mentioned_slug 'daedalus' = 0 rows,
--    'deadalus' = 0, 'evc-mesh-dev' = 1 (a single hand-written workaround). Every mention
--    addressed to him in that window was dropped on the floor, silently: notifyMentions
--    only writes a comment_mentions row once the slug resolves, so a miss is indistinguish-
--    able from never having been mentioned. It has already stalled a P0.
--
-- Safe because nothing keys on either string:
--   * The agent is addressed by UUID, and the UUID does not change — API keys, active
--     checkouts and his lane (which keys on mesh_agent_key) are untouched.
--   * name carries no constraint, index or FK. slug is unique per workspace and 'daedalus'
--     is free on prod (verified); it satisfies chk_agents_slug_format.
--   * author_name / assignee_name are resolved by correlated subquery at read time
--     (comment_repo.go:21), not stored, so this heals history rather than splitting it
--     into two series.
--   * 'evc-mesh-dev' appears in no code, config, lane roster, MCP registry or launchd job
--     — only in two historical log artifacts, which are records of the past and should not
--     be rewritten.
--
-- Both statements are guarded on the old value, so re-running is a no-op and Down cannot
-- clobber a later deliberate rename. A no-op on a fresh database (migration-check, CI) by
-- design — which is also why "goose up passed" proves nothing here on its own.
UPDATE agents
   SET name = 'Daedalus',
       updated_at = NOW()
 WHERE id = '684bd684-10e6-4329-9875-04846a1845c0'
   AND name = 'Deadalus';

UPDATE agents
   SET slug = 'daedalus',
       updated_at = NOW()
 WHERE id = '684bd684-10e6-4329-9875-04846a1845c0'
   AND slug = 'evc-mesh-dev';

-- +goose Down
UPDATE agents
   SET slug = 'evc-mesh-dev',
       updated_at = NOW()
 WHERE id = '684bd684-10e6-4329-9875-04846a1845c0'
   AND slug = 'daedalus';

UPDATE agents
   SET name = 'Deadalus',
       updated_at = NOW()
 WHERE id = '684bd684-10e6-4329-9875-04846a1845c0'
   AND name = 'Daedalus';
