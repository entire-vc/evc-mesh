-- +goose Up

-- Full-text search over document CONTENT, not just titles.
--
-- The shape is the one memories already uses (migration 20260315041): a stored
-- tsvector column, a BEFORE INSERT OR UPDATE trigger that builds it, and a GIN
-- index over it. Deliberately not a third mechanism — everything else in this
-- schema searches with ILIKE, which cannot rank and cannot match across word
-- forms, and inventing something new here would leave three.
--
-- The one thing memories does not have to solve: a document's body is NOT in
-- this table. It is an object in S3 addressed by storage_key, so there is
-- nothing here to index. search_text is a copy of that body, written by the
-- service in the same call that uploads it.
--
-- That copy is the cost of this feature and it is worth stating plainly:
--
--   * S3 remains canonical. search_text exists to be indexed, and nothing reads
--     it as content.
--   * The upload happens first, so a failure between the two leaves the body
--     correct and the index stale — search can lag, but it can never return a
--     document whose stored text says something else.
--   * Rows written before this migration have search_text NULL and are found by
--     title only, until their next save. There is no way to backfill them in
--     SQL: the text is in another system.
ALTER TABLE documents ADD COLUMN search_text TEXT;
ALTER TABLE documents ADD COLUMN search_vector TSVECTOR;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION documents_search_vector_update() RETURNS trigger AS $$
BEGIN
	NEW.search_vector :=
		setweight(to_tsvector('simple', coalesce(NEW.title, '')), 'A') ||
		setweight(to_tsvector('simple', left(coalesce(NEW.search_text, ''), 262144)), 'B');
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- The `left(..., 262144)` is load-bearing and must not be removed.
--
-- A tsvector cannot exceed 1048575 bytes; past that to_tsvector raises, the
-- trigger fails, and the write fails with it — meaning a document that is under
-- the service's own 5 MiB body cap becomes IMPOSSIBLE TO SAVE. Not hypothetical:
-- measured on Postgres 16, 1.6 MB of distinct tokens gives
-- `ERROR: string is too long for tsvector (1597986 bytes, max 1048575 bytes)`.
--
-- The bound was chosen by measuring the worst case — every token distinct, so
-- nothing dedupes — rather than by estimating:
--
--   input chars   vector bytes (ASCII)   vector bytes (Cyrillic)
--   512K          1 125 062  OVER        1 048 582  OVER
--   256K            563 318  ok            592 608  ok
--   128K            282 462  ok            297 696  ok
--
-- 256K characters leaves ~44% headroom at the worst case this can produce, in
-- either alphabet. Real prose is nowhere near it: 1.25 MB of repeating words
-- collapses to a 918-byte vector. So the practical meaning is that search covers
-- the first ~256K characters of a document, and only a document that is already
-- pathological reaches that.
CREATE TRIGGER trg_documents_search_vector
	BEFORE INSERT OR UPDATE ON documents
	FOR EACH ROW EXECUTE FUNCTION documents_search_vector_update();

-- 'simple', matching the stored column on memories, and for a stronger reason
-- here: these documents are written in a mix of Russian and English, often in
-- one paragraph. 'english' would stem the English and mangle nothing else, but
-- it would also stem words that only look English, and there is no single
-- dictionary that is right for both. 'simple' matches what was written.
--
-- The cost is honest and known: no stemming, so "runbooks" does not find
-- "runbook". If that becomes the top complaint, the answer is memories' own
-- pattern — a second, expression-indexed arm with a real dictionary — not a
-- change to this column, which other queries would then silently disagree with.
CREATE INDEX idx_documents_search ON documents USING GIN(search_vector);

-- Backfill what can be backfilled without leaving this database: every existing
-- row gets its title-only vector, so a document written before today is at least
-- findable by name rather than invisible.
UPDATE documents SET search_text = search_text;

-- +goose Down
DROP INDEX IF EXISTS idx_documents_search;
DROP TRIGGER IF EXISTS trg_documents_search_vector ON documents;
DROP FUNCTION IF EXISTS documents_search_vector_update();
ALTER TABLE documents DROP COLUMN IF EXISTS search_vector;
ALTER TABLE documents DROP COLUMN IF EXISTS search_text;
