-- +goose Up

-- Evidence for a typed `blocks` edge used to be any non-empty URL plus a
-- timestamp. It is now a verified record bound to the edge's criterion and
-- generation and, for delivery conditions, to the blocker's own pr link, HEAD
-- and environment. Only evidence_status = 'trusted' releases the edge.
ALTER TABLE task_dependencies
    ADD COLUMN criterion text NOT NULL DEFAULT '',
    ADD COLUMN generation bigint NOT NULL DEFAULT 1,
    ADD COLUMN evidence_status text NOT NULL DEFAULT 'none',
    ADD COLUMN evidence_reason text NULL,
    ADD COLUMN evidence_head_sha text NULL,
    ADD COLUMN evidence_environment text NULL,
    ADD COLUMN evidence_link_id uuid NULL,
    ADD COLUMN evidence_actor_id uuid NULL;

ALTER TABLE task_dependencies
    ADD CONSTRAINT chk_task_deps_evidence_status CHECK (evidence_status IN
        ('none', 'unverified', 'pending', 'invalid', 'trusted', 'revoked'));

-- Legacy evidence stays as a link but is not promoted to trusted.
UPDATE task_dependencies SET evidence_status = 'unverified'
 WHERE evidence_url IS NOT NULL AND btrim(evidence_url) <> '';

-- Drift: when the pr link a trusted receipt was bound to gets another HEAD (or
-- another deploy of the same environment), the receipt stops applying.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION revoke_dependency_evidence_on_drift() RETURNS trigger AS $$
BEGIN
    UPDATE task_dependencies d
       SET evidence_status = 'revoked',
           evidence_reason = 'bound pr link moved to another HEAD/deploy or its SHIP/acceptance changed'
     WHERE d.evidence_link_id = NEW.id
       AND d.evidence_status = 'trusted'
       AND d.evidence_head_sha IS NOT NULL
       AND (
            COALESCE(NEW.metadata->>'head_sha', '') <> d.evidence_head_sha
         OR (d.condition = 'shipped'
             AND COALESCE(NEW.metadata->>'ship_sha', '') <> d.evidence_head_sha)
         OR (d.condition = 'deployed'
             AND (COALESCE(NEW.metadata->'deployed'->>'sha', '') <> d.evidence_head_sha
               OR COALESCE(NEW.metadata->'deployed'->>'environment', '') <> COALESCE(d.evidence_environment, '')))
         OR (d.condition = 'accepted'
             AND (COALESCE(NEW.metadata->'acceptance'->>'sha', '') <> d.evidence_head_sha
               OR COALESCE(NEW.metadata->'acceptance'->>'result', '') <> 'passed'))
           );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_vcs_links_revoke_dependency_evidence
    AFTER UPDATE OF metadata ON vcs_links
    FOR EACH ROW EXECUTE FUNCTION revoke_dependency_evidence_on_drift();

-- Deleting the bound pr link removes the receipt itself: the evidence cannot
-- stay trusted without the record it was verified against.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION revoke_dependency_evidence_on_link_delete() RETURNS trigger AS $$
BEGIN
    UPDATE task_dependencies
       SET evidence_status = 'revoked',
           evidence_reason = 'bound pr link was deleted'
     WHERE evidence_link_id = OLD.id
       AND evidence_status = 'trusted';
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_vcs_links_revoke_dependency_evidence_on_delete
    BEFORE DELETE ON vcs_links
    FOR EACH ROW EXECUTE FUNCTION revoke_dependency_evidence_on_link_delete();

-- +goose Down
DROP TRIGGER IF EXISTS trg_vcs_links_revoke_dependency_evidence_on_delete ON vcs_links;
DROP FUNCTION IF EXISTS revoke_dependency_evidence_on_link_delete();
DROP TRIGGER IF EXISTS trg_vcs_links_revoke_dependency_evidence ON vcs_links;
DROP FUNCTION IF EXISTS revoke_dependency_evidence_on_drift();
ALTER TABLE task_dependencies DROP CONSTRAINT IF EXISTS chk_task_deps_evidence_status;
ALTER TABLE task_dependencies
    DROP COLUMN IF EXISTS evidence_actor_id, DROP COLUMN IF EXISTS evidence_link_id,
    DROP COLUMN IF EXISTS evidence_environment, DROP COLUMN IF EXISTS evidence_head_sha,
    DROP COLUMN IF EXISTS evidence_reason, DROP COLUMN IF EXISTS evidence_status,
    DROP COLUMN IF EXISTS generation, DROP COLUMN IF EXISTS criterion;
