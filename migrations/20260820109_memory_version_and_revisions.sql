-- +goose Up

-- Optimistic concurrency + an audit trail for memory writes.
--
-- Today `remember` is an UPSERT keyed on (workspace, scope, key) with no notion
-- of a prior state. Two agents holding the same key overwrite each other and
-- neither call fails: the loser's content is gone and the only evidence is that
-- the text changed. Memory is injected into other agents' context at session
-- start, so a silent overwrite does not stay a storage problem — it becomes a
-- fact everyone reads.
--
-- `version` gives a write something to be conditional on, and memory_revisions
-- keeps what the entry said before, together with the reason it changed.

ALTER TABLE memories
    ADD COLUMN version INTEGER NOT NULL DEFAULT 1;

-- One row per version of a memory: what it SAID at that version, and why it
-- came to say it. The current row in `memories` is always version = MAX(version)
-- here, so the newest revision is a duplicate of the live row by design — that
-- redundancy is what lets "what did this claim before?" be answered by reading
-- one table instead of reconstructing state from diffs.
CREATE TABLE memory_revisions (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Deliberately NOT a foreign key with ON DELETE CASCADE.
    --
    -- The 'forgotten' action below exists so that "what did this memory say
    -- before someone deleted it" stays answerable. A cascading FK would delete
    -- precisely those rows at precisely the moment they became the only record
    -- — the audit trail would be complete for every memory except the ones that
    -- were removed. Keeping this a plain UUID lets revisions outlive their
    -- memory, which is what an append-only audit log has to do.
    --
    -- The cost is orphaned rows after a delete. That is the intended state, not
    -- leakage: they are the record of the deletion.
    memory_id   UUID        NOT NULL,
    -- The version this snapshot represents. Monotonic per memory, starting at 1.
    version     INTEGER     NOT NULL CHECK (version >= 1),

    -- The content as of this version. Stored in full rather than as a diff:
    -- these rows are read to answer "what did this memory assert on date X",
    -- and a diff chain makes the common read expensive and a corrupted link
    -- unrecoverable.
    content     TEXT        NOT NULL,
    tags        TEXT[]      NOT NULL DEFAULT '{}',

    -- What produced this version.
    action      TEXT        NOT NULL CHECK (action IN ('created', 'updated', 'forgotten')),

    -- Why the author says this version exists.
    --
    -- NULL is permitted ON PURPOSE and only for now. Requiring a reason is the
    -- point of this column, but the fleet's callers cannot send one yet: the
    -- `remember` MCP tool has no reason parameter, and the binary every agent
    -- loads is built and installed by hand (see Mesh #3d448464). A NOT NULL
    -- column here would reject every memory write in the fleet the moment this
    -- migration landed, and the repair would need a manual rebuild on the very
    -- host whose agents had just lost the ability to record anything.
    --
    -- So: expand now (nullable, accepted), enforce at the application layer
    -- behind MESH_MEMORY_REQUIRE_REASON, and contract to NOT NULL in a later
    -- migration once adoption is measured rather than assumed.
    --
    -- The CHECK still holds the line that matters: a reason may be absent, but
    -- it may not be blank. Whitespace-named and unnamed read identically to a
    -- human, so btrim's default (space only) is widened to the real set.
    reason      TEXT        NULL CHECK (reason IS NULL OR length(btrim(reason, E' \t\r\n')) > 0),

    -- Who wrote this version. NULL for writes with no agent identity (system
    -- reconcilers, imports) — a real state, hence no FK requirement of presence.
    actor_agent_id UUID     NULL,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- One snapshot per version per memory. Also the guard that makes a
    -- concurrent double-bump fail loudly instead of forking the history.
    UNIQUE (memory_id, version)
);

-- The history read: newest first for one memory.
CREATE INDEX ix_memory_revisions_memory_version ON memory_revisions (memory_id, version DESC);

-- Supports "which writes never said why", the query that tells us whether the
-- reason requirement can be switched on without breaking callers.
CREATE INDEX ix_memory_revisions_missing_reason ON memory_revisions (created_at DESC)
    WHERE reason IS NULL;

-- Deliberately NOT backfilling a version-1 revision for the ~1.4k existing
-- memories. We do not know what they said when they were written or why, and
-- inventing a snapshot from today's content would put a row in the audit trail
-- that claims to be history and is not. History starts here; rows written
-- before this migration simply have none, which is the truthful state.

-- +goose Down
DROP TABLE IF EXISTS memory_revisions;
ALTER TABLE memories DROP COLUMN IF EXISTS version;
