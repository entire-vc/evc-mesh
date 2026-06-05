-- +goose Up
-- Fix: bulk-inserted rows used type='team-relay' (hyphen); code uses 'team_relay' (underscore).
-- c6e35032 already has a correct 'team_relay' row from the PATCH API → delete the legacy duplicate.
DELETE FROM project_integrations
WHERE type = 'team-relay'
  AND project_id = 'c6e35032-36d5-4045-b30d-6cf9e35c3dee';

-- Rename remaining hyphen rows to underscore.
UPDATE project_integrations
SET type = 'team_relay'
WHERE type = 'team-relay';

-- +goose Down
UPDATE project_integrations
SET type = 'team-relay'
WHERE type = 'team_relay';
