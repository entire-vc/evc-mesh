-- +goose Up

-- A monotonic write counter on a document, so that a write can be made
-- conditional on the state the writer read.
--
-- Why this exists: a document is a shared mutable object whose body is a single
-- object in storage with no history. On 2026-08-19 two agents editing the same
-- page overwrote each other and the loss was silent — prose does not look wrong
-- the way a missing form field does. Unconditional write over shared prose
-- reproduces that incident in a place nobody notices.
--
-- A counter, not updated_at. Timestamps are awkward to compare across clients,
-- can go backwards when a clock is corrected, and cannot distinguish two writes
-- landing inside the same microsecond — which is exactly the case a lost-update
-- check exists for. An integer that only ever goes up has none of those problems
-- and compares with =.
--
-- Starts at 1, and back-filling every existing row with 1 is honest here in a way
-- back-filling updated_by was not: 1 does not claim "this row was written once",
-- it claims "this is the version you are reading", which is true of every row the
-- moment the column exists. Nothing about the past is being invented, because
-- the value is only ever compared against a value a caller read from this same
-- column.
--
-- It is bumped on a write to the BODY or the TITLE — the document's content. A
-- pure move (parent_id, position) leaves it alone: re-filing a page in the tree
-- does not change a word of it, and bumping there would make a concurrent
-- reorganisation 409 every editor in the project for no reason.
--
-- This is not the versioning the create_documents migration deferred. There is
-- still no history table and no way to read an old revision; this is one integer
-- that answers "is the document still as I last saw it". History, when it comes,
-- gets its own table keyed on document_id and can adopt this counter as its
-- revision number.
ALTER TABLE documents ADD COLUMN version INT NOT NULL DEFAULT 1;

-- Monotonic is a property worth stating to the database rather than trusting to
-- the one service that writes it: every write path goes through a conditional
-- UPDATE that sets version = version + 1, and a future path that set it to a
-- literal would silently break every reader holding a base_version.
ALTER TABLE documents ADD CONSTRAINT chk_documents_version_positive
    CHECK (version >= 1);

-- No index. version is read from a row already located by its id and compared in
-- the WHERE of the same statement that updates that row; nothing filters, joins
-- or orders on it across rows.

-- +goose Down
ALTER TABLE documents DROP CONSTRAINT IF EXISTS chk_documents_version_positive;
ALTER TABLE documents DROP COLUMN IF EXISTS version;
