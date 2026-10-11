package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Edges retained from before the workspace write guard may still join tasks of two
// workspaces. wsEdgesCTE is the only edge source for graph reads: an edge with an endpoint
// in another workspace is dropped, so its foreign task id never reaches the response
// (not even as a restricted node) and never counts as a blocker. Deleted tasks and
// projects stay in: they are still this workspace's "missing" boundary.
// $%d is the workspace id.
const wsEdgesCTE = `ws_edges AS (SELECT d.id,d.task_id,d.depends_on_task_id,d.dependency_type,d.created_at FROM task_dependencies d WHERE NOT EXISTS (SELECT 1 FROM tasks xt JOIN projects xp ON xp.id=xt.project_id WHERE xt.id IN (d.task_id,d.depends_on_task_id) AND xp.workspace_id<>$%d))`

// ReadGraph uses one read-only repeatable-read transaction and five bulk reads (six for focus).
// Only compact fields of project tasks and direct boundary nodes are read;
// descriptions, custom fields, comments, checkouts and secret values are absent.
func (r *TaskDependencyRepo) ReadGraph(ctx context.Context, projectID, focus uuid.UUID, scope domain.GraphScope) (*domain.GraphSnapshot, error) {
	tx, err := r.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var wsID uuid.UUID
	if err := tx.GetContext(ctx, &wsID, `/* graph_read */ SELECT workspace_id FROM projects WHERE id=$1 AND deleted_at IS NULL`, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Project")
		}
		return nil, err
	}
	if scope.WorkspaceID != wsID {
		return nil, apierror.Forbidden("workspace context mismatch")
	}
	// Independently enforce membership, including the centre of a cross-project focus.
	var permitted bool
	if err := tx.GetContext(ctx, &permitted, `/* graph_read */ SELECT EXISTS(SELECT 1 FROM projects p WHERE p.id=$1 AND p.workspace_id=$2 AND p.deleted_at IS NULL AND ($3 OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=p.id AND (($4 AND m.agent_id=$5) OR (NOT $4 AND m.user_id=$5)))))`, projectID, wsID, scope.Admin && !scope.Agent, scope.Agent, scope.ActorID); err != nil {
		return nil, err
	}
	if !permitted {
		return nil, apierror.Forbidden("not a member of this project")
	}
	if focus != uuid.Nil {
		if err := tx.GetContext(ctx, &permitted, `/* graph_read */ SELECT EXISTS(SELECT 1 FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1 AND t.deleted_at IS NULL AND p.workspace_id=$2 AND p.deleted_at IS NULL AND ($3 OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=p.id AND (($4 AND m.agent_id=$5) OR (NOT $4 AND m.user_id=$5)))))`, focus, wsID, scope.Admin && !scope.Agent, scope.Agent, scope.ActorID); err != nil {
			return nil, err
		}
		if !permitted {
			return nil, apierror.NotFound("Task")
		}
	}
	snapshot := &domain.GraphSnapshot{Nodes: []domain.GraphNode{}, Dependencies: []domain.TaskDependency{}, Topology: []domain.TaskDependency{}}
	// Diagnose legacy cross-project cycles only within the actor's authorized
	// topology. Restricted paths must not expose cycle existence, size or changes
	// through diagnostics or revision; direct boundaries still appear as unknown.
	if err := tx.SelectContext(ctx, &snapshot.Topology, `/* graph_read */ SELECT d.id,d.task_id,d.depends_on_task_id,d.dependency_type,d.created_at FROM task_dependencies d JOIN tasks a ON a.id=d.task_id JOIN projects ap ON ap.id=a.project_id JOIN tasks b ON b.id=d.depends_on_task_id JOIN projects bp ON bp.id=b.project_id WHERE ap.workspace_id=$1 AND bp.workspace_id=$1 AND a.deleted_at IS NULL AND b.deleted_at IS NULL AND ap.deleted_at IS NULL AND bp.deleted_at IS NULL AND ($2 OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=ap.id AND (($3 AND m.agent_id=$4) OR (NOT $3 AND m.user_id=$4)))) AND ($2 OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=bp.id AND (($3 AND m.agent_id=$4) OR (NOT $3 AND m.user_id=$4)))) AND d.dependency_type='blocks' ORDER BY d.id`, wsID, scope.Admin && !scope.Agent, scope.Agent, scope.ActorID); err != nil {
		return nil, err
	}
	if err := tx.SelectContext(ctx, &snapshot.Dependencies, fmt.Sprintf(`/* graph_read */ WITH local AS (SELECT id FROM tasks WHERE deleted_at IS NULL AND (($2='00000000-0000-0000-0000-000000000000'::uuid AND project_id=$1) OR id=$2)), `+wsEdgesCTE+` SELECT d.id,d.task_id,d.depends_on_task_id,d.dependency_type,d.created_at FROM ws_edges d WHERE d.task_id IN (SELECT id FROM local) OR d.depends_on_task_id IN (SELECT id FROM local) ORDER BY d.id`, 3), projectID, focus, wsID); err != nil {
		return nil, err
	}
	// Use CASE at the SQL boundary: inaccessible metadata never enters Go memory.
	// Aggregate once, instead of one repository/network query per node.
	nodeQuery := `/* graph_read */ WITH local AS (SELECT id FROM tasks WHERE deleted_at IS NULL AND (($2='00000000-0000-0000-0000-000000000000'::uuid AND project_id=$1) OR id=$2)),
 ` + fmt.Sprintf(wsEdgesCTE, 3) + `,
 edges AS (SELECT d.task_id,d.depends_on_task_id FROM ws_edges d WHERE d.task_id IN (SELECT id FROM local) OR d.depends_on_task_id IN (SELECT id FROM local)),
 ids AS (SELECT id FROM local UNION SELECT task_id FROM edges UNION SELECT depends_on_task_id FROM edges UNION SELECT parent_task_id FROM tasks WHERE id IN (SELECT id FROM local) AND parent_task_id IS NOT NULL UNION SELECT id FROM tasks WHERE parent_task_id=$2 AND deleted_at IS NULL),
 allowed_projects AS (SELECT p.id FROM projects p WHERE p.workspace_id=$3 AND p.deleted_at IS NULL AND ($4 OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=p.id AND (($5 AND m.agent_id=$6) OR (NOT $5 AND m.user_id=$6))))),
 access AS (SELECT i.id, (t.id IS NOT NULL AND t.deleted_at IS NULL AND ap.id IS NOT NULL) AS allowed FROM ids i LEFT JOIN tasks t ON t.id=i.id LEFT JOIN allowed_projects ap ON ap.id=t.project_id),
 counts AS (SELECT d.task_id, COUNT(*) AS blockers,
 COUNT(*) FILTER (WHERE ap.id IS NULL OR t.deleted_at IS NOT NULL OR s.category IS NULL OR s.category NOT IN ('done','cancelled')) AS unfinished_blockers,
 COUNT(*) FILTER (WHERE ap.id IS NULL OR t.deleted_at IS NOT NULL) AS unknown_blockers
 FROM ws_edges d LEFT JOIN tasks t ON t.id=d.depends_on_task_id LEFT JOIN task_statuses s ON s.id=t.status_id LEFT JOIN allowed_projects ap ON ap.id=t.project_id WHERE d.dependency_type='blocks' AND d.task_id IN (SELECT id FROM ids) GROUP BY d.task_id),
 dependents AS (SELECT depends_on_task_id,COUNT(*) AS n FROM ws_edges WHERE dependency_type='blocks' AND depends_on_task_id IN (SELECT id FROM ids) GROUP BY depends_on_task_id)
 SELECT a.id, CASE WHEN a.allowed THEN t.project_id END AS project_id, CASE WHEN a.allowed THEN p.name ELSE '' END AS project_name, CASE WHEN a.allowed THEN p.slug ELSE '' END AS project_slug,
 CASE WHEN a.allowed THEN t.title ELSE '' END AS title, CASE WHEN a.allowed THEN t.status_id END AS status_id, CASE WHEN a.allowed THEN s.name ELSE '' END AS status_name, CASE WHEN a.allowed THEN s.category::text ELSE '' END AS status_category,
 CASE WHEN a.allowed THEN t.priority::text ELSE '' END AS priority, CASE WHEN a.allowed THEN COALESCE(CASE WHEN t.assignee_type='agent' THEN ag.name ELSE u.display_name END,'') ELSE '' END AS assignee_name,
 CASE WHEN a.allowed THEN t.parent_task_id END AS parent_task_id, CASE WHEN a.allowed THEN t.human_gate ELSE false END AS human_gate, CASE WHEN a.allowed THEN t.start_after END AS start_after,
 CASE WHEN a.allowed THEN 'available' WHEN t.id IS NULL OR t.deleted_at IS NOT NULL OR p.id IS NULL OR p.deleted_at IS NOT NULL THEN 'missing' ELSE 'restricted' END AS availability,
 CASE WHEN a.allowed THEN COALESCE(c.blockers,0) ELSE 0 END AS blockers, CASE WHEN a.allowed THEN COALESCE(c.unfinished_blockers,0) ELSE 0 END AS unfinished_blockers, CASE WHEN a.allowed THEN COALESCE(c.unknown_blockers,0) ELSE 0 END AS unknown_blockers,
 CASE WHEN a.allowed THEN COALESCE(d.n,0) ELSE 0 END AS dependents, CASE WHEN a.allowed THEN t.updated_at ELSE 'epoch'::timestamptz END AS updated_at
 FROM access a LEFT JOIN tasks t ON t.id=a.id LEFT JOIN projects p ON p.id=t.project_id LEFT JOIN task_statuses s ON s.id=t.status_id LEFT JOIN agents ag ON ag.id=t.assignee_id AND t.assignee_type='agent' LEFT JOIN users u ON u.id=t.assignee_id AND t.assignee_type='user' LEFT JOIN counts c ON c.task_id=a.id LEFT JOIN dependents d ON d.depends_on_task_id=a.id ORDER BY a.id`
	if err := tx.SelectContext(ctx, &snapshot.Nodes, nodeQuery, projectID, focus, wsID, scope.Admin && !scope.Agent, scope.Agent, scope.ActorID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Hash only observable data. Private task metadata and unrelated workspace
	// topology must not turn the continuation token into an activity side channel.
	hash := sha256.New()
	components := domain.GraphCycleComponents(snapshot.Topology)
	visible := map[uuid.UUID]bool{}
	for _, n := range snapshot.Nodes {
		visible[n.ID] = n.Availability == "available"
	}
	for _, n := range snapshot.Nodes {
		n.Cycle = visible[n.ID] && len(components[n.ID]) > 0
		data, marshalErr := json.Marshal(n)
		if marshalErr != nil {
			return nil, marshalErr
		}
		_, _ = hash.Write(data)
		if visible[n.ID] {
			_, _ = fmt.Fprintf(hash, "/%d/", len(components[n.ID]))
			for _, id := range components[n.ID] {
				if visible[id] {
					_, _ = fmt.Fprintf(hash, "%s;", id)
				}
			}
		}
	}
	for _, d := range snapshot.Dependencies {
		_, _ = fmt.Fprintf(hash, "%s/%s/%s/%s;", d.ID, d.TaskID, d.DependsOnTaskID, d.DependencyType)
	}
	snapshot.Revision = fmt.Sprintf("%x", hash.Sum(nil))
	return snapshot, nil
}
