-- +goose Up

-- Write the anchor's coordinate system into the database itself, now that the
-- API enforces it.
--
-- 20260819100_create_document_comments.sql already said, in a comment above the
-- columns, that anchor_start/anchor_end are UTF-8 byte offsets into the markdown.
-- Nothing held a writer to it: validateAnchor checked the sign, the order and the
-- length of the fields and never opened the document, so the coordinate system
-- was in practice whatever the writing client used. Two independently written
-- frontends promptly used two different ones — byte offsets into the markdown in
-- one, character indices into the rendered text in the other — and on ASCII those
-- are the same number, so both looked correct. They differ on any non-ASCII
-- document, which is most of ours.
--
-- The service now refuses an anchor whose offsets do not contain its own quote
-- (mdoc.SpanMatchesQuote, internal/service/document_comment_service.go). This
-- migration carries no schema change and no data change: it puts the rule where
-- the next person to open the table sees it, so that the file comment, the code
-- and the database agree instead of only two of the three.
--
-- COMMENT ON is metadata-only — it takes no lock on the table's rows and rewrites
-- nothing.

COMMENT ON COLUMN document_comments.anchor_start IS
    'UTF-8 BYTE offset into the document markdown, inclusive; half-open with anchor_end. '
    'NOT a character index, NOT a UTF-16 index, and NOT an offset into the rendered text — '
    'those coincide only on ASCII. Enforced on write: the API refuses a span that does not '
    'contain anchor_exact. NULL together with anchor_end means orphaned (the quote survives, '
    'the position was lost to an edit).';

COMMENT ON COLUMN document_comments.anchor_end IS
    'UTF-8 BYTE offset into the document markdown, exclusive. See anchor_start.';

COMMENT ON COLUMN document_comments.anchor_exact IS
    'The quoted text the comment is about, taken from the RENDERED document, so it may lack '
    'markup the byte range spans (`**bold**` reads as bold). It is what re-finds the range '
    'after an edit, and what anchor_start/anchor_end are checked against on write.';

-- +goose Down

COMMENT ON COLUMN document_comments.anchor_start IS NULL;
COMMENT ON COLUMN document_comments.anchor_end IS NULL;
COMMENT ON COLUMN document_comments.anchor_exact IS NULL;
