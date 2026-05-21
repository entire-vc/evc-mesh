-- +goose Up
-- Non-unique index for webhook task resolution when MESH-<uuid> ref is not
-- present in PR title/body. Lets the orchestrator look up a previously-linked
-- task by (provider, link_type, external_id) without scanning vcs_links.
CREATE INDEX IF NOT EXISTS idx_vcs_links_external_lookup
    ON vcs_links(provider, link_type, external_id);

-- +goose Down
DROP INDEX IF EXISTS idx_vcs_links_external_lookup;
