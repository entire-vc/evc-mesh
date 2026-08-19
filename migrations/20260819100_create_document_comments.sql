-- +goose Up

-- Confluence-style inline comments on a document: select some text, leave a
-- comment on it, reply to it, resolve the thread.
--
-- Its own table rather than a polymorphic `comments`. comments.task_id is
-- NOT NULL REFERENCES tasks(id) and every index on that table leads with it, so
-- a document comment has nothing to point at and no index that would serve it.
-- More decisively, comment_service.go wraps ~2000 lines of task-workflow around
-- every create — enforceBlockingTriage, releaseHumanGate, scanHumanGateOwnership,
-- enforceTriageExit, buildTaskSnap, notifyMentions. Making that path polymorphic
-- would run "typo in paragraph 3" through the human-gate machinery.
--
-- Threading is a single parent pointer, exactly like comments: no materialized
-- path, no depth column. The service keeps threads one level deep (a reply to a
-- reply is refused), which is what makes the resolved-thread filter below a
-- single COALESCE rather than a recursive walk.
CREATE TABLE document_comments (
    id                  UUID PRIMARY KEY,
    document_id         UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    -- Self-FK. ON DELETE CASCADE covers only the hard-delete path (a purged
    -- project takes its documents, which take their comments); the API's delete
    -- is deleted_at, and the repository stamps the reply subtree in the same
    -- statement so a reply cannot outlive the comment it answers.
    parent_comment_id   UUID REFERENCES document_comments(id) ON DELETE CASCADE,

    author_id           UUID NOT NULL,
    author_type         actor_type NOT NULL,
    body                TEXT NOT NULL,

    -- The anchor: a W3C Web Annotation selector pair, the same shape Hypothesis
    -- stores. anchor_exact/prefix/suffix are the TextQuoteSelector,
    -- anchor_start/end the TextPositionSelector, and both are kept because
    -- neither survives alone.
    --
    -- Offsets alone are provably insufficient. They are true only of the exact
    -- revision they were taken from, and a document body here is a single mutable
    -- object in storage with no revision to pin them to: inserting one character
    -- above a comment silently moves every anchor below it, and the row would go
    -- on pointing confidently at the wrong words. The quote is what survives an
    -- edit, because it identifies the text by what it says rather than where it
    -- sat; prefix and suffix are what disambiguate a quote occurring more than
    -- once ("the API" appears eleven times, this is the one after "authenticate").
    --
    -- The quote alone is not enough either: it makes every render a full re-scan,
    -- and a repeated quote needs a starting point to prefer the nearest match.
    --
    -- anchor_start/anchor_end are BYTE offsets, half-open [start, end), matching
    -- memory_chunks (migrations/20260727082_memory_chunks.sql) and the way
    -- Postgres substring() and most tooling index text.
    anchor_exact        TEXT,
    anchor_prefix       TEXT,
    anchor_suffix       TEXT,
    anchor_start        INT,
    anchor_end          INT,

    resolved_at         TIMESTAMPTZ,
    resolved_by         UUID,
    resolved_by_type    actor_type,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ,

    -- An orphan — a comment whose text was edited away so the range can no longer
    -- be located — is represented by NULL anchor_start/anchor_end with
    -- anchor_exact still set. Three states, each distinguishable:
    --
    --   anchor_exact IS NULL                        -> never anchored: a
    --                                                  document-level comment, or
    --                                                  a reply (it inherits its
    --                                                  parent's anchor)
    --   anchor_exact set, anchor_start set          -> anchored
    --   anchor_exact set, anchor_start NULL         -> ORPHANED: we still know
    --                                                  what it was said about,
    --                                                  we no longer know where
    --
    -- Nullable offsets rather than an is_orphaned flag, deliberately. A flag is a
    -- second source of truth beside the offsets it describes, and the two can
    -- disagree: a row saying orphaned=true while still carrying the stale offsets
    -- a client would happily highlight. Nulling the position IS the orphaning, in
    -- one write, and "orphaned but here are the offsets" becomes unrepresentable.
    -- The quote is kept either way — it is what a re-anchoring pass searches with,
    -- and what the UI shows as "this was written about: …".
    CONSTRAINT chk_document_comments_anchor_position CHECK (
        (anchor_start IS NULL) = (anchor_end IS NULL)
    ),
    CONSTRAINT chk_document_comments_anchor_range CHECK (
        anchor_start IS NULL OR (anchor_start >= 0 AND anchor_end > anchor_start)
    ),
    -- A position with no quote is the one shape that can never be re-found after
    -- an edit, and it is indistinguishable from an orphan that lost its quote too.
    -- Refuse it at the schema rather than discover it in a renderer.
    CONSTRAINT chk_document_comments_anchor_quote CHECK (
        anchor_start IS NULL OR anchor_exact IS NOT NULL
    ),
    -- prefix/suffix describe the neighbourhood of a quote; with no quote they
    -- describe nothing.
    CONSTRAINT chk_document_comments_anchor_neighbourhood CHECK (
        anchor_exact IS NOT NULL OR (anchor_prefix IS NULL AND anchor_suffix IS NULL)
    ),

    -- Resolution is one act by one actor at one time: all three columns or none.
    -- Two of three would leave "resolved, by nobody" or "resolved by Ann, never",
    -- and the read model has no way to render either.
    CONSTRAINT chk_document_comments_resolution CHECK (
        (resolved_at IS NULL     AND resolved_by IS NULL     AND resolved_by_type IS NULL)
     OR (resolved_at IS NOT NULL AND resolved_by IS NOT NULL AND resolved_by_type IS NOT NULL)
    )
);

-- Serves the only listing there is: a document's live comments in creation order,
-- with id as the tiebreak so a page boundary cannot repeat or skip a row when two
-- comments share a timestamp.
CREATE INDEX idx_document_comments_document ON document_comments (document_id, created_at, id)
    WHERE deleted_at IS NULL;

-- Serves the reply lookups: the resolved-thread filter's parent probe and the
-- recursive stamp that soft-deletes a thread with its root.
CREATE INDEX idx_document_comments_parent ON document_comments (parent_comment_id)
    WHERE parent_comment_id IS NOT NULL AND deleted_at IS NULL;

-- Tenant isolation at the schema layer. Same shape and same join hop as
-- document_attachments: a comment names its tenant through its document, which
-- names it through its project. Both USING and WITH CHECK, so the policy governs
-- writes as well as reads — a WITH CHECK-less policy would let a session insert a
-- row it could not then see.
ALTER TABLE document_comments ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_document_comments ON document_comments
    USING (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_comments.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_comments.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- +goose Down
DROP TABLE IF EXISTS document_comments;
