-- +goose Up
-- A deploy recorded as "production" is a prod deploy. Since an internal ticket
-- every reader (domain.DependencyReleasedSQL, VCSLink.IsShipped, DeployedTasks,
-- resolve_server_card_waits) compares metadata.deployed.environment with 'prod'
-- only, and the write path now stores "production" as "prod"
-- (domain.NormalizeVCSEnvironment). This rewrites the rows written before that.
--
-- Bound evidence first: the drift trigger on vcs_links revokes a trusted receipt
-- whose link environment no longer equals evidence_environment, so the receipt
-- must already say 'prod' when its link is rewritten.
UPDATE task_dependencies
   SET evidence_environment = 'prod'
 WHERE lower(btrim(evidence_environment)) = 'production';

-- The AFTER UPDATE trigger on vcs_links resolves the server card waits these
-- deploys now satisfy, as a fresh deploy record would.
UPDATE vcs_links
   SET metadata = jsonb_set(metadata, '{deployed,environment}', '"prod"'::jsonb)
 WHERE jsonb_typeof(metadata->'deployed') = 'object'
   AND lower(btrim(metadata->'deployed'->>'environment')) = 'production';

-- +goose Down
-- Irreversible by design: "production" and "prod" name the same environment and
-- the original spelling is not kept. Nothing to undo.
SELECT 1;
