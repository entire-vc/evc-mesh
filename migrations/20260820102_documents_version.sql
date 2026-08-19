-- +goose Up

-- A version counter per document: a monotonically increasing integer bumped on
-- every write to the document, so a writer can say which revision it is editing
-- and the server can refuse a write built on a stale one.
--
-- This is what conditional writes are checked against. Two agents editing the
-- same page on 2026-08-19 overwrote each other and the second write won
-- silently; the field one of them had added simply stopped existing. A document
-- is a shared mutable object, and an unconditional PATCH on a shared mutable
-- object loses whichever edit lands first, with no error anywhere to notice.
--
-- Deliberately NOT updated_at. A timestamp is the tempting reuse and it is the
-- wrong instrument three ways: two writes inside the same clock tick are
-- indistinguishable, comparing them means agreeing on precision and timezone
-- across every client, and it moves backwards across an NTP correction or a
-- host restart — at which point a stale write starts looking fresh. A counter
-- has one job and cannot go backwards.
--
-- BIGINT rather than INT: this increments per save, and the editor autosaves
-- every two seconds while somebody is typing. INT's 2.1 billion is not a
-- ceiling anybody reaches, but neither is it one worth reasoning about, and
-- widening a NOT NULL column later costs a table rewrite.
--
-- Backward compatible in both directions, which is what lets it deploy ahead of
-- the code that reads it: DEFAULT 1 gives every existing row a value without a
-- back-fill pass, and code that predates this column neither selects nor writes
-- it. There is no DROP, no NOT NULL added to an existing column, and no rename
-- in this release.
--
-- Starts at 1, not 0: the version a caller sends back is the one it was handed,
-- so the only value that matters is that it is defined and moves. 1 reads as
-- "the first revision" rather than as an unset field, which is what a
-- zero-valued int64 in Go looks like when a client forgot to send one.
ALTER TABLE documents ADD COLUMN version BIGINT NOT NULL DEFAULT 1;

-- No index. version is read from a row already located by its id and compared
-- there; nothing filters, joins or orders on it. The conditional UPDATE narrows
-- on the primary key first and evaluates version as a row predicate, so an index
-- on it would be write cost with no reader — the same reasoning as updated_by in
-- migrations/20260819099_document_updated_by.sql.

-- +goose Down
ALTER TABLE documents DROP COLUMN IF EXISTS version;
