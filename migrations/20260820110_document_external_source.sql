-- +goose Up

-- Where a document came from, and — for a copy — what it was copied from.
--
-- Every document schema decision so far assumed the document was born here:
-- created_by names an actor in this workspace, version tracks writes we made,
-- updated_by tracks who among us edited it. A document synced in from a Team
-- Relay share breaks all three assumptions at once. It has an author who is not
-- in agents or users. It has a body whose truth lives in Team Relay, not here —
-- so "the current version" is really "the version we last copied", and staleness
-- is checked against the source, not against our own edit history. And writing
-- into it as though it were ours would silently fork a copy from the page it is
-- supposed to mirror.
--
-- source_kind is the discriminator, and it is NOT inferred from whether the
-- other columns are NULL. A document that simply predates a feature and one
-- that is deliberately empty must stay distinguishable from "nobody filled this
-- in yet" — see 20260819099_document_updated_by's reasoning for why NULL as a
-- silent stand-in for "doesn't apply" is the wrong shape once two different
-- kinds of emptiness exist. Here a plain document's source columns are NULL
-- because there is nothing to say, and a copy's are NOT NULL because a copy is
-- required to say it — the CHECK below enforces both directions, so "empty
-- because unfilled" cannot occur on a copy and "filled because guessed" cannot
-- occur on an own document.
CREATE TYPE document_source AS ENUM ('own', 'team_relay');

ALTER TABLE documents ADD COLUMN source_kind document_source NOT NULL DEFAULT 'own';

-- source_share is the Team Relay share's id, not its slug. A slug is a name the
-- share's owner can change; the id is not, and TeamRelaySettings.ShareID
-- (internal/domain/project_integration.go) already standardises on it for the
-- same reason. VARCHAR rather than UUID: this column names an identifier issued
-- by whatever the source is, and the next source this table grows to describe
-- may not hand out UUIDs.
ALTER TABLE documents ADD COLUMN source_share VARCHAR(255);

-- source_path is the file's path inside that share — Team Relay's own unit of
-- addressing a page, independent of whatever slug we assign the copy on our
-- side.
ALTER TABLE documents ADD COLUMN source_path VARCHAR(1000);

-- source_sha256 is the hash of the original's body at the moment it was copied.
-- It is load-bearing for two things neither of which exists yet in this PR:
-- R3's freshness check (does the source still match what we hold?) and R8's
-- write-back guard (has the source moved since we last read it, so writing
-- would silently clobber someone else's edit?). Team Relay hashes the same way
-- we would — hashlib.sha256 over the raw body bytes — so the two sides' hashes
-- are directly comparable without renormalizing either one.
ALTER TABLE documents ADD COLUMN source_sha256 VARCHAR(64);

-- synced_at is when this copy was last checked against its source — not when
-- the row was created and not updated_at, which tracks OUR edits and a copy is
-- not supposed to receive any.
ALTER TABLE documents ADD COLUMN synced_at TIMESTAMPTZ;

-- external_author names whoever Team Relay's source says wrote the page — as
-- free text, not a foreign key. There is nothing in this workspace to point a
-- key at: Team Relay does not record a per-file author anywhere in its sync
-- protocol or its web-publish API (checked against apps/control-plane/app/api/
-- routers/shares.py and web.py on entire-vc/evc-team-relay@main, 2026-08-20) —
-- only a share owner, who is routinely not the person who wrote a given page
-- inside it. Attributing a copy to the share owner would print a name on
-- documents that person never touched, which is the exact failure this column
-- exists to avoid. So it stays NULL on every copy today, honestly, the same way
-- updated_by stays NULL on every row that predates it: NULL here means "the
-- source didn't say", not "we forgot to ask". The day Team Relay starts naming
-- an author, this column starts getting one, with no second migration.
ALTER TABLE documents ADD COLUMN external_author VARCHAR(255);

-- The shape check: own and copy are exhaustive and mutually exclusive, and each
-- has exactly one legal shape for these five columns. synced_at is required on
-- a copy (not merely allowed) because a copy with no sync timestamp has never
-- actually been synced — it would be a row claiming to mirror a source it has
-- never read. external_author is deliberately absent from this constraint: it
-- is allowed to be NULL on a copy (see above — that is the honest value today),
-- so requiring it here would make every existing and near-future copy
-- unwritable.
ALTER TABLE documents ADD CONSTRAINT chk_documents_source_shape CHECK (
    (source_kind = 'own'
        AND source_share  IS NULL AND source_path   IS NULL
        AND source_sha256 IS NULL AND synced_at     IS NULL)
 OR (source_kind <> 'own'
        AND source_share  IS NOT NULL AND source_path IS NOT NULL
        AND source_sha256 IS NOT NULL AND synced_at   IS NOT NULL)
);

-- One copy per (project, source share, source path). Two rows mirroring the
-- same source page in the same project is not a legitimate state to reach —
-- it is what a re-run sync that failed to find its own earlier copy would
-- produce, and it is cheaper for the database to refuse that outright than for
-- every reader of the tree to wonder which of two rows is the real one.
--
-- Partial (WHERE source_kind <> 'own'): an own document's source_share is
-- always NULL, and NULL is never equal to NULL for uniqueness purposes anyway
-- — but naming the condition explicitly is what keeps this index small and
-- documents, rather than relying on that NULL behaviour as an accident that
-- happens to shrink it. It doubles as the index that answers "every copy of
-- this share" (matched on its project_id, source_share prefix) and "the
-- document that mirrors this exact source path" (matched in full) — both reads
-- R3 needs on every open, once R3 exists to ask them.
CREATE UNIQUE INDEX uq_documents_source ON documents (project_id, source_share, source_path)
    WHERE source_kind <> 'own' AND deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS uq_documents_source;
ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_source_shape;
ALTER TABLE documents DROP COLUMN IF EXISTS external_author;
ALTER TABLE documents DROP COLUMN IF EXISTS synced_at;
ALTER TABLE documents DROP COLUMN IF EXISTS source_sha256;
ALTER TABLE documents DROP COLUMN IF EXISTS source_path;
ALTER TABLE documents DROP COLUMN IF EXISTS source_share;
ALTER TABLE documents DROP COLUMN IF EXISTS source_kind;
DROP TYPE IF EXISTS document_source;
