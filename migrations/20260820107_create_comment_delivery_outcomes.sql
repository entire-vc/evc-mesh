-- +goose Up

-- One row per @-addressed slug on a comment, recording what happened to it.
--
-- This is deliberately NOT a set of columns on comment_mentions. That table is
-- the Mention feed: a row exists there only once a slug has RESOLVED to an
-- agent or a user, and its primary key is (comment_id, mentioned_id). The
-- failure this table exists to make visible is precisely the one that produces
-- no resolved id at all — an @-mention of a slug nobody owns writes nothing
-- anywhere today, so "the name was never mentioned" and "every attempt to
-- reach that name failed" are the same empty query result. Keyed on the SLUG
-- as written, this table can record the miss.
--
-- reason is NOT NULL and non-empty for EVERY row, delivered ones included.
-- An outcome whose reason is blank is the thing this table was built to stop
-- being possible, so it is a table constraint and not a convention.
CREATE TABLE comment_delivery_outcomes (
    comment_id     UUID        NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    -- The handle exactly as written in the comment body, lowercased by the
    -- extractor. Present even when it resolves to nothing.
    recipient_slug TEXT        NOT NULL,
    -- NULL when the slug resolved to nobody. That is a real state, not missing
    -- data, which is why there is no FK here: the row's whole purpose is to
    -- survive the absence of a referent.
    recipient_id   UUID        NULL,
    recipient_kind TEXT        NOT NULL CHECK (recipient_kind IN ('agent', 'user', 'unknown')),
    outcome        TEXT        NOT NULL CHECK (outcome IN ('delivered', 'skipped', 'failed')),
    -- btrim's default character set is the space alone, so btrim(reason) would
    -- happily accept a reason of one tab: whitespace-named is the same as
    -- unnamed to a reader, and the constraint has to say so explicitly.
    reason         TEXT        NOT NULL CHECK (length(btrim(reason, E' \t\r\n')) > 0),
    -- Which path the verdict is about: event_stream, task_queue, notification,
    -- or none. Without it "delivered" would not say delivered to WHAT, and the
    -- two agent paths have genuinely different reach.
    channel        TEXT        NOT NULL CHECK (length(btrim(channel, E' \t\r\n')) > 0),
    -- The recipient's presence at decision time (online/idle/offline/unknown).
    -- Recorded separately from reason because presence explains a verdict
    -- without being one: a mention can be delivered to an offline agent (it
    -- sits in their queue) and skipped for an online one (the card is not
    -- theirs), and collapsing the two loses exactly the distinction a sender
    -- needs to know which of those two things to fix.
    recipient_presence TEXT    NOT NULL DEFAULT 'unknown',
    decided_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (comment_id, recipient_slug)
);

CREATE INDEX ix_comment_delivery_outcomes_comment ON comment_delivery_outcomes (comment_id);
-- Supports "show me everything that failed to reach anyone" across the corpus,
-- which is the query that turns this from a per-comment detail into a signal.
CREATE INDEX ix_comment_delivery_outcomes_outcome ON comment_delivery_outcomes (outcome, decided_at DESC)
    WHERE outcome <> 'delivered';

-- +goose Down
DROP TABLE IF EXISTS comment_delivery_outcomes;
