-- +goose Up

-- The collision this table exists to make visible turned out to still have a
-- blind spot one layer down. A slug that resolves to BOTH an agent and a user
-- (task f4f47938 — @hugh names the QA-Mesh agent and hugh@entire.vc) now gets
-- a verdict row written for each side (comment_service.go no longer
-- `continue`s past the first match). But both rows share one comment_id +
-- recipient_slug, and the old primary key was exactly that pair — so the
-- second INSERT ... ON CONFLICT DO UPDATE upserts over the first, and the row
-- that lands is whichever branch ran last. The agent verdict silently
-- disappears the moment the human verdict is written (live measurement on
-- prod, probe card #d3dbcf37: three rows in, two rows out).
--
-- This is the same failure the table was built to end, one layer down: an
-- outcome for one of two addressed parties goes missing with no trace that it
-- ever existed. recipient_kind already distinguishes the two rows in every
-- read path (outcomesBySlugAndKind, ListByCommentIDs) — it just was not part
-- of what made a row unique to write.
ALTER TABLE comment_delivery_outcomes DROP CONSTRAINT comment_delivery_outcomes_pkey;
ALTER TABLE comment_delivery_outcomes ADD PRIMARY KEY (comment_id, recipient_slug, recipient_kind);

-- +goose Down
ALTER TABLE comment_delivery_outcomes DROP CONSTRAINT comment_delivery_outcomes_pkey;
ALTER TABLE comment_delivery_outcomes ADD PRIMARY KEY (comment_id, recipient_slug);
