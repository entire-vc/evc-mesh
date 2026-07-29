-- +goose Up
-- The application now always writes an explicit status for PR links
-- (vcs_link_service.Create defaults empty to 'open'), but the column itself
-- carried no default — any future direct insert (a script, a migration, a
-- manual fixup) can still leave status NULL/'' and reproduce the exact
-- ambiguity the done-evidence gate (service.MoveTask, #2697392d) can't
-- resolve: an empty status blocks the same as 'open' with no way to tell
-- "known open" from "never recorded".
--
-- Deliberately NOT backfilling existing empty-status rows to 'open' here:
-- some of them (e.g. the two rows behind #df734dd9 itself) are links to
-- PRs that were ALREADY MERGED before the link was created, so their real
-- status is 'merged', not 'open' — asserting 'open' would replace an
-- honest "unknown" with a confident wrong answer. Those rows need a
-- case-by-case correction (check the PR, then re-link with an explicit
-- status), not a blanket default.
ALTER TABLE vcs_links ALTER COLUMN status SET DEFAULT 'open';

-- +goose Down
ALTER TABLE vcs_links ALTER COLUMN status DROP DEFAULT;
