package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// FailedPipeline is an open GitLab merge request link whose latest pipeline
// failed, with the task it is linked to.
type FailedPipeline struct {
	LinkID            uuid.UUID `json:"link_id" db:"link_id"`
	URL               string    `json:"url" db:"url"`
	Title             string    `json:"title" db:"title"`
	ExternalID        string    `json:"external_id" db:"external_id"`
	PipelineUpdatedAt time.Time `json:"pipeline_updated_at" db:"pipeline_updated_at"`
	TaskID            uuid.UUID `json:"task_id" db:"task_id"`
	TaskTitle         string    `json:"task_title" db:"task_title"`
	ProjectID         uuid.UUID `json:"project_id" db:"project_id"`
	ProjectName       string    `json:"project_name" db:"project_name"`
}

// FailedMergeRequestPipelines lists the open GitLab MR links of tasks assigned
// to userID in projectIDs (of workspaceID) whose latest pipeline failed at or
// after since, newest failure first. The caller decides which projects the
// user may see; an empty projectIDs returns nothing.
func (r *VCSLinkRepo) FailedMergeRequestPipelines(ctx context.Context, workspaceID, userID uuid.UUID, projectIDs []uuid.UUID, since time.Time, limit int) ([]FailedPipeline, error) {
	out := []FailedPipeline{}
	if len(projectIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(projectIDs))
	for i, id := range projectIDs {
		ids[i] = id.String()
	}
	err := r.db.SelectContext(ctx, &out, `
		SELECT l.id AS link_id, l.url, COALESCE(l.title, '') AS title, l.external_id, l.pipeline_updated_at,
		       t.id AS task_id, t.title AS task_title, p.id AS project_id, p.name AS project_name
		  FROM vcs_links l
		  JOIN tasks t ON t.id = l.task_id AND t.deleted_at IS NULL
		  JOIN projects p ON p.id = t.project_id AND p.deleted_at IS NULL
		 WHERE p.workspace_id = $1
		   AND p.id = ANY($2::uuid[])
		   AND t.assignee_type = 'user' AND t.assignee_id = $3
		   AND l.provider = 'gitlab' AND l.link_type = 'pr' AND l.status = 'open'
		   AND l.pipeline_status = 'failed' AND l.pipeline_updated_at >= $4
		 ORDER BY l.pipeline_updated_at DESC, l.id
		 LIMIT $5`, workspaceID, pq.Array(ids), userID, since, limit)
	return out, err
}
