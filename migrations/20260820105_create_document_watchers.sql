-- +goose Up

-- Who asked to be told when a document changes.
--
-- One row per (document, principal). The principal is a users.id or an
-- agents.id depending on the kind, so there is no FK on watcher_id — the same
-- trade-off document_comment_mentions makes for mentioned_id, and for the same
-- reason: a column cannot reference two tables.
CREATE TABLE document_watchers (
    document_id  UUID        NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    watcher_id   UUID        NOT NULL,
    watcher_kind TEXT        NOT NULL CHECK (watcher_kind IN ('agent', 'user')),

    -- How this subscription came about. Kept because it is the only way to
    -- explain a subscription nobody remembers making: 'author' and 'commenter'
    -- are created by the system, 'explicit' is the Watch button.
    source       TEXT        NOT NULL CHECK (source IN ('explicit', 'author', 'commenter')),

    -- Unsubscribing sets this instead of deleting the row, and that is the
    -- whole reason the row survives an unsubscribe at all.
    --
    -- Auto-subscription is what makes the feature useful — you should not learn
    -- about your own page last. It is also what makes unsubscribing impossible
    -- if a cancelled subscription leaves no trace: the next comment you write
    -- re-creates the row the system just deleted on your behalf, and the button
    -- appears to do nothing. A tombstone is the difference between "not
    -- subscribed yet" and "asked not to be", and only the first of those may be
    -- overwritten by an automatic subscribe.
    muted        BOOLEAN     NOT NULL DEFAULT FALSE,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (document_id, watcher_id)
);

-- Serves "what am I watching" and the unwatch lookup.
CREATE INDEX ix_document_watchers_watcher
    ON document_watchers (watcher_id, watcher_kind);

-- Serves the dispatch read: the live watchers of one document.
CREATE INDEX ix_document_watchers_live
    ON document_watchers (document_id)
    WHERE muted = FALSE;

ALTER TABLE document_watchers ENABLE ROW LEVEL SECURITY;

CREATE POLICY rls_document_watchers ON document_watchers
    USING (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_watchers.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_watchers.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );


-- A change that has happened but has not been announced yet.
--
-- This table IS the coalescing. The editor autosaves on a 2-second debounce, so
-- ten minutes of typing is on the order of a hundred writes to the document; a
-- subscription that notified per write would send a hundred messages for one
-- sitting and be switched off the same day. Each of those writes instead UPDATEs
-- the one open notice for (document, actor) — bumping edit_count, moving
-- last_edit_at, extending to_version — and a sweeper turns the notice into a
-- single notification once the actor has stopped typing for the quiet window.
--
-- In a table rather than a timer in the API process, because the state has to
-- survive the thing that owns it: an in-process timer loses every pending
-- notification on deploy or restart, and two API replicas would each hold their
-- own half of the truth. A dropped notification here looks exactly like a
-- document nobody edited, which is the failure this feature is supposed to make
-- impossible.
--
-- One notice per ACTOR, not per document: two people editing the same page are
-- two separate pieces of news, and merging them would leave the message unable
-- to say who did what without re-reading history that is not kept.
CREATE TABLE document_change_notices (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id   UUID        NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    workspace_id  UUID        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,

    actor_id      UUID        NOT NULL,
    actor_kind    TEXT        NOT NULL CHECK (actor_kind IN ('agent', 'user')),
    -- Resolved once, when the notice opens. The message is built by the sweeper
    -- minutes later and a name lookup there would be a second round-trip per
    -- notice for a value that cannot have changed meaningfully in the window.
    actor_name    TEXT        NOT NULL DEFAULT '',

    edit_count    INTEGER     NOT NULL DEFAULT 1,
    title_changed BOOLEAN     NOT NULL DEFAULT FALSE,
    body_changed  BOOLEAN     NOT NULL DEFAULT FALSE,

    -- The version span the notice covers, so the message can say what it is
    -- summarising and a reader can diff it later if that is ever built.
    from_version  INTEGER     NOT NULL,
    to_version    INTEGER     NOT NULL,

    first_edit_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_edit_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- NULL means still pending. Set when the sweeper has finished with the
    -- notice — including when it finished by failing, see dispatch_error.
    dispatched_at TIMESTAMPTZ NULL,

    -- Why a dispatch produced nothing, when it produced nothing.
    --
    -- The point of storing it rather than only logging it: "the watchers could
    -- not be read" and "there were no watchers" are the same silence from
    -- outside, and the second is normal. A row that records which one happened
    -- is the difference between a bug that is noticed and a bug that runs for
    -- twenty-five days.
    dispatch_error TEXT       NULL,

    -- How many principals the sweeper actually handed this notice to. Zero with
    -- no error is a real answer (nobody is watching) and is kept as one.
    recipients     INTEGER    NOT NULL DEFAULT 0
);

-- At most one OPEN notice per (document, actor) — this is the constraint the
-- coalescing upsert targets. Dispatched notices are left out of the index so
-- history accumulates without ever blocking the next notice.
CREATE UNIQUE INDEX uq_document_change_notices_open
    ON document_change_notices (document_id, actor_id, actor_kind)
    WHERE dispatched_at IS NULL;

-- Serves the sweeper: the pending notices whose author has gone quiet.
CREATE INDEX ix_document_change_notices_pending
    ON document_change_notices (last_edit_at)
    WHERE dispatched_at IS NULL;

ALTER TABLE document_change_notices ENABLE ROW LEVEL SECURITY;

-- Same tenant rule as the watchers, expressed through the document. The sweeper
-- runs outside any one request and reads this table with the workspace setting
-- applied per notice batch (see DocumentWatchRepo.ClaimPendingNotices).
CREATE POLICY rls_document_change_notices ON document_change_notices
    USING (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_change_notices.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    )
    WITH CHECK (
        EXISTS (
            SELECT 1 FROM documents d
            JOIN projects p ON d.project_id = p.id
            WHERE d.id = document_change_notices.document_id
              AND p.workspace_id = current_setting('app.current_workspace_id', true)::uuid
        )
    );


-- Subscribe existing subscribers to the new event types.
--
-- Same reason as migrations/20260820103: notificationService.dispatch delivers a
-- row only if the event type appears in that row's `events` array, and every
-- preference row that exists today was written before these event types existed.
-- Without this, watching a document would be dispatched perfectly and delivered
-- to nobody, with no log line to say so — which is precisely the silence this
-- feature is built to remove.
--
-- Scoped to rows that are subscribed to something. A row with an empty array has
-- opted out of everything and stays opted out; adding events to it would turn a
-- deliberate silence back on. Rows with a non-empty array are opted in on the
-- reasoning that delivery is gated a second time, by an explicit per-document
-- Watch that nobody performs by accident: a subscriber who never presses it is
-- not reached by any of these three regardless of what their array says.
UPDATE notification_preferences
   SET events = events
                || CASE WHEN 'document.changed'   = ANY(events) THEN '{}'::text[] ELSE '{document.changed}'::text[]   END
                || CASE WHEN 'document.commented' = ANY(events) THEN '{}'::text[] ELSE '{document.commented}'::text[] END
                || CASE WHEN 'document.deleted'   = ANY(events) THEN '{}'::text[] ELSE '{document.deleted}'::text[]   END
 WHERE COALESCE(array_length(events, 1), 0) > 0;

-- +goose Down
DROP TABLE IF EXISTS document_change_notices;
DROP TABLE IF EXISTS document_watchers;

UPDATE notification_preferences
   SET events = array_remove(array_remove(array_remove(events,
           'document.changed'), 'document.commented'), 'document.deleted')
 WHERE 'document.changed'   = ANY(events)
    OR 'document.commented' = ANY(events)
    OR 'document.deleted'   = ANY(events);
