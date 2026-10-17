-- +goose Up

-- vcs_links.pipeline_*: the state of the latest GitLab pipeline of an MR link,
-- written by the "Pipeline Hook" webhook so the task card badge can show
-- success / failed / running without a GitLab round-trip per render.
-- pipeline_id orders deliveries (GitLab pipeline ids only grow, webhooks can
-- arrive out of order): an event for an older pipeline never overwrites a
-- newer one. All three columns are nullable and additive; NULL means no
-- pipeline has been reported for the link (every pre-existing row, every
-- GitHub link). An older binary never names them (vcsLinkSelectCols is an
-- explicit list), so rolling the code back alone is safe.
ALTER TABLE vcs_links ADD COLUMN IF NOT EXISTS pipeline_id BIGINT;
ALTER TABLE vcs_links ADD COLUMN IF NOT EXISTS pipeline_status TEXT;
ALTER TABLE vcs_links ADD COLUMN IF NOT EXISTS pipeline_updated_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE vcs_links DROP COLUMN IF EXISTS pipeline_updated_at;
ALTER TABLE vcs_links DROP COLUMN IF EXISTS pipeline_status;
ALTER TABLE vcs_links DROP COLUMN IF EXISTS pipeline_id;
