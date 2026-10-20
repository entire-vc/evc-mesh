-- +goose Up
-- A GitLab MR iid (and a GitHub PR number) is unique only inside its project,
-- but external_id stores just that number, so two projects' !88 linked to one
-- task collided on (task_id, provider, link_type, external_id) and the webhook
-- got a 409 forever. The url names the project; fold it into the identity.
-- Same index name: the repo maps a violation of it to a clear 409.
-- md5(url) keeps entries small whatever the url length. Every row that
-- satisfied the old, narrower index satisfies this one, so no data moves.
DROP INDEX IF EXISTS idx_vcs_links_unique;
CREATE UNIQUE INDEX idx_vcs_links_unique
    ON vcs_links(task_id, provider, link_type, external_id, md5(url));

-- +goose Down
DROP INDEX IF EXISTS idx_vcs_links_unique;
CREATE UNIQUE INDEX idx_vcs_links_unique
    ON vcs_links(task_id, provider, link_type, external_id);
