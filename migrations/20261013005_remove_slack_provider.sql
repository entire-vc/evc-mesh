-- +goose Up
-- Slack integration removed (#c9f9834f); drop any leftover rows.
DELETE FROM integration_configs WHERE provider = 'slack';

-- +goose Down
-- Deleted rows are not recoverable; the Slack provider no longer exists in code.
SELECT 1;
