-- +goose Up

-- @-mentions extracted from a document comment's body.
--
-- Its own table rather than a second FK on comment_mentions. That table's
-- comment_id is `NOT NULL REFERENCES comments(id) ON DELETE CASCADE`, and
-- comments holds task comments only — document comments live in
-- document_comments (see migrations/20260819100_create_document_comments.sql
-- for why they are separate). Widening comment_mentions would mean either a
-- nullable polymorphic pair of FKs, which no constraint can keep exclusive
-- without a CHECK that the primary key cannot express, or dropping referential
-- integrity on the side that already has it. One table per parent keeps the
-- cascade honest: deleting a document comment takes its mentions with it,
-- which is what stops a mention inbox from listing a row nobody can open.
--
-- Column-for-column the same shape as comment_mentions, deliberately: the read
-- side (list, mark-seen, unseen count) is the same three queries, and a
-- divergent shape would be a second set of them for no reason.
CREATE TABLE document_comment_mentions (
    comment_id     UUID        NOT NULL REFERENCES document_comments(id) ON DELETE CASCADE,
    mentioned_id   UUID        NOT NULL,
    -- No FK: the id is a users.id or an agents.id depending on the kind, and a
    -- column cannot reference two tables. Same trade-off comment_mentions makes.
    mentioned_kind TEXT        NOT NULL CHECK (mentioned_kind IN ('agent', 'user')),
    -- The slug as it was written. Kept alongside the resolved id because it is
    -- what the body says: a renamed principal keeps its id, and the stored slug
    -- is the only way to explain later why that text matched this recipient.
    mentioned_slug TEXT        NOT NULL,
    extracted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    seen_at        TIMESTAMPTZ NULL,
    -- One row per recipient per comment. Editing a comment to name the same
    -- person twice, or re-running extraction over an unchanged body, is an
    -- ON CONFLICT DO NOTHING rather than a duplicate notification.
    PRIMARY KEY (comment_id, mentioned_id)
);

-- Serves the inbox read: this recipient's mentions, unseen first-class.
CREATE INDEX ix_document_comment_mentions_mentioned
    ON document_comment_mentions (mentioned_id, seen_at);

-- Serves the ORDER BY extracted_at DESC the listing always applies.
CREATE INDEX ix_document_comment_mentions_extracted
    ON document_comment_mentions (extracted_at DESC);

-- Tenant isolation at the schema layer, one join hop further than
-- document_comments: a mention names its tenant through its comment, which
-- names it through its document, which names it through its project. Both
-- USING and WITH CHECK, so the policy governs writes as well as reads — a
-- WITH CHECK-less policy would let a session insert a row it could not see.
ALTER TABLE document_comment_mentions ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_document_comment_mentions ON document_comment_mentions
    USING (
        EXISTS (
            SELECT 1 FROM document_comments dc
            JOIN documents d ON dc.document_id = d.id
            JOIN projects p ON d.project_id = p.id
            WHERE dc.id = document_comment_mentions.comment_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM document_comments dc
            JOIN documents d ON dc.document_id = d.id
            JOIN projects p ON d.project_id = p.id
            WHERE dc.id = document_comment_mentions.comment_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );

-- Subscribe existing mention-subscribers to the new event.
--
-- notificationService.dispatch delivers a row only if the event type is listed
-- in that row's `events` array, and every preference row that exists today was
-- written before "document.mentioned" was a thing any producer emitted. Adding
-- the event type to the code alone would ship a notification that is dispatched
-- correctly, matches nobody's stored subscription, and is dropped by the fan-out
-- loop without a log line — indistinguishable, from the outside, from a mention
-- that was never written. That is the exact failure this feature exists to avoid,
-- so the backfill is part of shipping it rather than a follow-up.
--
-- Scoped to rows that already carry task.mentioned: somebody who asked to be told
-- when they are @mentioned meant being @mentioned, not being @mentioned
-- specifically on a task. Rows that deliberately excluded mentions keep excluding
-- them.
UPDATE notification_preferences
   SET events = array_append(events, 'document.mentioned')
 WHERE 'task.mentioned' = ANY(events)
   AND NOT ('document.mentioned' = ANY(events));

-- +goose Down
DROP TABLE IF EXISTS document_comment_mentions;

UPDATE notification_preferences
   SET events = array_remove(events, 'document.mentioned')
 WHERE 'document.mentioned' = ANY(events);
