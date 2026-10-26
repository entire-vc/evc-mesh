package postgres

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

// ProjectRepoUsage implements repository.VCSLinkProjectRepos. The repository
// name is derived in SQL from the link URL (host dropped, the "/-/merge_requests/N"
// or "/pull/N" tail cut off), the same shape service.RepoFromVCSURL produces.
func (r *VCSLinkRepo) ProjectRepoUsage(ctx context.Context, projectID uuid.UUID, repo string) (uses, hasAny bool, err error) {
	const q = `
		SELECT COALESCE(bool_or(x.repo = $2), false) AS uses, COUNT(*) > 0 AS has_any
		FROM (
			SELECT lower(regexp_replace(
			           regexp_replace(l.url, '^https?://[^/]+/', ''),
			           '(/-)?/(merge_requests|pull)/[0-9]+.*$', '')) AS repo
			FROM vcs_links l
			JOIN tasks t ON t.id = l.task_id
			WHERE t.project_id = $1
			  AND t.deleted_at IS NULL
			  AND l.link_type = 'pr'
			  AND l.url ~ '/(merge_requests|pull)/[0-9]+'
			  AND COALESCE(l.metadata->>'role', '') <> 'review'
			  AND COALESCE(l.metadata->>'reference_only', '') <> 'true'
		) x
		WHERE x.repo <> ''`
	var row struct {
		Uses   bool `db:"uses"`
		HasAny bool `db:"has_any"`
	}
	if err := r.db.GetContext(ctx, &row, q, projectID, strings.ToLower(repo)); err != nil {
		return false, false, err
	}
	return row.Uses, row.HasAny, nil
}

var _ repository.VCSLinkProjectRepos = (*VCSLinkRepo)(nil)
