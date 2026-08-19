-- +goose Up

-- Comments anchored to a range of text inside a document — the Confluence-style
-- margin note, plus the thread that hangs off it.
--
-- Its own table rather than a polymorphic `comments`. That was measured, not
-- assumed: comments.task_id is NOT NULL REFERENCES tasks(id), every index on the
-- table leads with it, the create route is POST /tasks/:task_id/comments whose
-- handler opens with resolveTaskID, and comment_service.go layers ~2000 lines of
-- TASK machinery over every insert (enforceBlockingTriage, releaseHumanGate,
-- scanHumanGateOwnership, enforceTriageExit, buildTaskSnap, notifyMentions).
-- Making that polymorphic means reaching into the human-gate machinery that has
-- already stranded cards for weeks at a time, to gain a shared table nothing
-- queries across. A document comment shares the word, not the behaviour.
--
-- parent_comment_id is the whole tree: one self-FK, no materialized path. Every
-- read of this table is "all live comments of one document", which is a single
-- indexed query, so the client assembles the tree and no ancestor walk is ever
-- needed server-side. A thread_root_id column would be denormalisation bought
-- with nothing.
CREATE TABLE document_comments (
    id                  UUID PRIMARY KEY,
    document_id         UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    -- NULL for a thread root — the comment that owns the anchor. A reply names
    -- its parent and carries no anchor of its own.
    --
    -- ON DELETE CASCADE, unlike comments.parent_comment_id which is SET NULL:
    -- there, orphaning a reply leaves it in a flat task thread where it still
    -- reads sensibly. Here the root is what ties the thread to a place in the
    -- document, so a reply that outlived its root would be a comment on nothing,
    -- displayable nowhere. This covers the hard-delete path only; the API soft
    -- deletes, and the repository walks the subtree itself.
    parent_comment_id   UUID REFERENCES document_comments(id) ON DELETE CASCADE,
    body                TEXT NOT NULL,

    -- ---- The anchor. Thread roots only. -----------------------------------
    --
    -- The shape is ADR G1's {start, end, exact, prefix, suffix}, and it is the
    -- SAME one D6 put on a paragraph link (web/src/lib/docs/anchor.ts) — a
    -- comment range is the general case and a linked paragraph is the range that
    -- happens to cover one whole block. One scheme, so a document has one answer
    -- to "where was this pointing", not two that disagree.
    --
    -- start/end are half-open character offsets into the document's text
    -- projection (every top-level block's text, whitespace-collapsed, joined with
    -- newlines) — the same half-open shape memory_chunks uses for chunk_start /
    -- chunk_end, and for the same reason: an offset pair survives the body moving
    -- between stores, where a pointer into a parse tree would not.
    --
    -- They are a HINT. Identity is carried by exact, with prefix/suffix as the
    -- surrounding context, because offsets go stale on the first edit above the
    -- range while the quoted text does not. Resolution is quote-first and refuses
    -- to guess: see resolveAnchor. A resolver that trusted the offsets would
    -- silently move a comment onto whatever text now sits at that position, which
    -- is the one outcome worse than losing the anchor.
    anchor_start        INT,
    anchor_end          INT,
    anchor_exact        TEXT,
    anchor_prefix       TEXT,
    anchor_suffix       TEXT,

    -- ---- Resolution. Thread roots only. -----------------------------------
    resolved_at         TIMESTAMPTZ,
    resolved_by         UUID,
    resolved_by_type    actor_type,

    author_id           UUID NOT NULL,
    author_type         actor_type NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ,

    -- A root has an anchor and a reply does not. Stated as an equivalence rather
    -- than two one-way checks so neither half can drift: an anchored reply and an
    -- unanchored root are both refused.
    CONSTRAINT ck_document_comments_root_has_anchor
        CHECK ((parent_comment_id IS NULL) = (anchor_start IS NOT NULL)),

    -- All five anchor fields travel together. Four-of-five is not a partial
    -- anchor, it is an unresolvable one, and the resolver would read the missing
    -- context as "no context" — which is exactly how a match gets accepted that
    -- should have been refused.
    CONSTRAINT ck_document_comments_anchor_complete
        CHECK (num_nonnulls(anchor_start, anchor_end, anchor_exact, anchor_prefix, anchor_suffix) IN (0, 5)),

    -- Half-open and non-empty. An empty range is not a comment on anything, and
    -- it would match at every position in the document.
    CONSTRAINT ck_document_comments_anchor_range
        CHECK (anchor_start IS NULL OR (anchor_start >= 0 AND anchor_end > anchor_start)),

    -- Resolving is a property of the thread, so only a root carries it.
    CONSTRAINT ck_document_comments_resolved_root_only
        CHECK (resolved_at IS NULL OR parent_comment_id IS NULL),

    -- Who resolved it is recorded whenever it is resolved. Without this, an
    -- unresolve that cleared only the timestamp would leave a stale actor behind
    -- and the next reader would attribute a resolution nobody made.
    CONSTRAINT ck_document_comments_resolved_actor
        CHECK (num_nonnulls(resolved_at, resolved_by, resolved_by_type) IN (0, 3)),

    CONSTRAINT ck_document_comments_body_not_blank
        CHECK (length(btrim(body)) > 0)
);

-- Serves the only read there is: every live comment of one document, oldest
-- first. The client builds the tree and the anchoring from this one page.
CREATE INDEX idx_document_comments_document ON document_comments (document_id, created_at, id)
    WHERE deleted_at IS NULL;

-- Serves the subtree walk a delete does.
CREATE INDEX idx_document_comments_parent ON document_comments (parent_comment_id)
    WHERE parent_comment_id IS NOT NULL AND deleted_at IS NULL;

-- Tenant isolation at the schema layer. Same shape and same join hop as
-- document_attachments: a comment names its tenant through its document, which
-- names it through its project. Both USING and WITH CHECK, so the policy governs
-- writes too — a WITH CHECK-less policy lets a session insert a row it cannot
-- then see.
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
