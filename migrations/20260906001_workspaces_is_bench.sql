-- +goose Up

-- lme-bench fixture isolation is held today by comparing a caller's agent-key
-- slug against BENCH_WORKSPACE_SLUG at the CLIENT (scripts/memory-bench/) — a
-- VALUE, not code. A wrong secret, or a hand-run script holding a prod key,
-- silently writes ~49 fixtures per question straight into a live workspace
-- (the July leak, #4045f449: 32 fixtures landed in prod this way). This column
-- backs a SERVER-side, fail-closed guard on the reserved `lme-bench` memory
-- tag (memoryService.Remember, task #0104878c): a workspace not flagged here
-- can never accept that tag, no matter what env var any client process is
-- holding. DEFAULT FALSE is deliberate — every existing and future workspace
-- is refused the tag until explicitly flagged, never the other way round.
ALTER TABLE workspaces ADD COLUMN is_bench BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE workspaces SET is_bench = TRUE WHERE id = '8ad7e9da-ae6d-4cde-9a08-02e88ac76015';

COMMENT ON COLUMN workspaces.is_bench IS
    'True for exactly the dedicated LME-bench workspace. Gates the reserved '
    '`lme-bench` memory tag in memoryService.Remember — see task #0104878c. '
    'No API path sets this; it is changed only by migration.';

-- +goose Down

ALTER TABLE workspaces DROP COLUMN is_bench;
