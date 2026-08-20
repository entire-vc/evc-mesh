-- +goose Up

-- Say in the database that the offsets are now MAINTAINED, not merely written.
--
-- 20260819100_create_document_comments.sql already described orphaning — "the
-- quote is kept either way, it is what a re-anchoring pass searches with" — and
-- described a pass that did not exist. Nothing ever nulled anchor_start, so the
-- orphaned state was unreachable and every row claimed a position forever.
-- Measured on prod (#90dd31f9): three consecutive edits to one document, and
-- three times the stored anchor addressed a paragraph the comment was not about
-- while the API reported orphaned:false. 20260820104 fixed what the numbers
-- MEAN; this is about how long they stay true.
--
-- PATCH /documents/:id now re-resolves every anchored comment against the
-- markdown it just stored (mdoc.Reanchor, documentService.reanchorComments),
-- moves the offsets to wherever the quote now sits, and nulls them when it is
-- gone. So the three states below are all reachable, and `orphaned` — computed
-- from whether the position is present — became an answer about the text rather
-- than about the write that stored it.
--
-- One residue worth writing down where the next reader will find it: the pass
-- is best-effort. The document row and its body object are written first and
-- must be, or a failed comment pass would report a saved edit as lost. If the
-- pass then fails, it logs and leaves the offsets describing the previous body
-- until this document is written again. Bounded and self-healing, but it means
-- "orphaned is false" is a claim about the last SUCCESSFUL pass, not a
-- guarantee at every instant.
--
-- COMMENT ON is metadata-only: no lock on the rows, nothing rewritten.

COMMENT ON COLUMN document_comments.anchor_start IS
    'UTF-8 BYTE offset into the document markdown, inclusive; half-open with anchor_end. '
    'NOT a character index, NOT a UTF-16 index, and NOT an offset into the rendered text — '
    'those coincide only on ASCII. Enforced on write: the API refuses a span that does not '
    'contain anchor_exact. Maintained on every body write: PATCH /documents/:id re-resolves '
    'anchor_exact against the new markdown and moves this offset to it. NULL together with '
    'anchor_end means orphaned — that write could not find the quote in the document any more.';

COMMENT ON COLUMN document_comments.anchor_end IS
    'UTF-8 BYTE offset into the document markdown, exclusive. See anchor_start.';

COMMENT ON COLUMN document_comments.anchor_prefix IS
    'Up to 48 code points of the document immediately BEFORE the quote, retaken from the '
    'document on every re-anchor rather than carried over: it exists to tell repeats of one '
    'quote apart in THIS revision, and a neighbourhood describing text deleted two edits ago '
    'tells nothing apart. Kept unchanged while an anchor is orphaned — it is the last place '
    'the quote was known to sit.';

COMMENT ON COLUMN document_comments.anchor_suffix IS
    'Up to 48 code points of the document immediately AFTER the quote. See anchor_prefix.';

COMMENT ON COLUMN document_comments.anchor_exact IS
    'The quoted text the comment is about, taken from the RENDERED document, so it may lack '
    'markup the byte range spans (`**bold**` reads as bold). It is never rewritten — it records '
    'what the comment was written about — and it is what re-finds the range after an edit, and '
    'what anchor_start/anchor_end are checked against on write.';

-- +goose Down

-- Back to the wording of 20260820104, which is what this file amends.
COMMENT ON COLUMN document_comments.anchor_start IS
    'UTF-8 BYTE offset into the document markdown, inclusive; half-open with anchor_end. '
    'NOT a character index, NOT a UTF-16 index, and NOT an offset into the rendered text — '
    'those coincide only on ASCII. Enforced on write: the API refuses a span that does not '
    'contain anchor_exact. NULL together with anchor_end means orphaned (the quote survives, '
    'the position was lost to an edit).';

COMMENT ON COLUMN document_comments.anchor_end IS
    'UTF-8 BYTE offset into the document markdown, exclusive. See anchor_start.';

COMMENT ON COLUMN document_comments.anchor_prefix IS NULL;
COMMENT ON COLUMN document_comments.anchor_suffix IS NULL;

COMMENT ON COLUMN document_comments.anchor_exact IS
    'The quoted text the comment is about, taken from the RENDERED document, so it may lack '
    'markup the byte range spans (`**bold**` reads as bold). It is what re-finds the range '
    'after an edit, and what anchor_start/anchor_end are checked against on write.';
