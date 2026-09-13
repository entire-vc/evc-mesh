-- +goose Up

-- Task #c5b5fb48: the memory review-triage nightly job used a bare
-- time.NewTicker(24*time.Hour) keyed off process START, not wall-clock time.
-- On a service that restarts often (measured: 70 restarts / 6 days on
-- mesh-vm), the ticker almost never survives to fire — 2 real runs in 6 days
-- against a nightly target. This table gives any such job a durable
-- last-run watermark so a restart can compute "how long is really left"
-- instead of resetting to a fresh full interval.
--
-- Deliberately generic (job_name, not memory-review-triage-specific): any
-- future long-interval background job on a frequently-restarted service has
-- the same failure shape and can reuse this table rather than re-inventing
-- its own watermark storage.
CREATE TABLE scheduler_job_runs (
    job_name    TEXT PRIMARY KEY,
    last_run_at TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down

DROP TABLE IF EXISTS scheduler_job_runs;
