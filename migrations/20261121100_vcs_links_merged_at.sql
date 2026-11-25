-- +goose Up

-- merged_at is the moment the VCS provider merged the PR/MR a link names, as
-- the provider reports it (GitHub pull_request.merged_at, GitLab
-- merge_requests.merged_at). NULL means the time is not known yet. It is never
-- derived from status_synced_at, created_at, the card's completion or a deploy:
-- those move whenever Mesh re-reads a status, and a window counted by them
-- would shift between runs without any new merge. Existing merged links are
-- filled from the provider by the merged_at backfill sweeper, not here.
ALTER TABLE vcs_links ADD COLUMN IF NOT EXISTS merged_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE vcs_links DROP COLUMN IF EXISTS merged_at;
