//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Scope "project" widgets: only projects bound to the extension that the
// viewer may open; the GitLab widget's data: the viewer's tasks only.
func TestDashboardWidgetData_ProjectsAndFailedPipelines(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	ws, bound, status := createTestProject(t, db)
	member, admin, outsider := createTestUser(t, db, "wm"), createTestUser(t, db, "wa"), createTestUser(t, db, "wo")
	addWorkspaceMember(t, db, ws.ID, member, "member")
	addWorkspaceMember(t, db, ws.ID, admin, "admin")
	addWorkspaceMember(t, db, ws.ID, outsider, "member")

	// A second project in the workspace, the member is in it but GitLab is not bound to it.
	unbound := uuid.New()
	_, err := db.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings, created_at, updated_at)
		VALUES ($1, $2, 'Unbound', $3, 'none', '{}', NOW(), NOW())`, unbound, ws.ID, "ub-"+unbound.String()[:8])
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO project_integrations (id, project_id, type, extension_id, enabled, settings, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'gitlab', 'gitlab', true, '{"repo_path":"g/p"}', NOW(), NOW())`, bound.ID)
	require.NoError(t, err)
	for _, p := range []uuid.UUID{bound.ID, unbound} {
		_, err = db.ExecContext(ctx, `INSERT INTO project_members (id, project_id, user_id, workspace_id, role) VALUES (gen_random_uuid(), $1, $2, $3, 'member')`, p, member, ws.ID)
		require.NoError(t, err)
	}

	scope := NewExtensionProjectScopeRepo(db)
	got, err := scope.WidgetProjects(ctx, ws.ID, "gitlab", member)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{bound.ID}, got, "bound and a member: offered; unbound: not")
	got, err = scope.WidgetProjects(ctx, ws.ID, "gitlab", admin)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{bound.ID}, got, "a workspace admin opens every project")
	got, err = scope.WidgetProjects(ctx, ws.ID, "gitlab", outsider)
	require.NoError(t, err)
	require.Empty(t, got, "not a member of the bound project: nothing")
	_, err = db.ExecContext(ctx, `UPDATE project_integrations SET enabled = false WHERE project_id = $1`, bound.ID)
	require.NoError(t, err)
	got, err = scope.WidgetProjects(ctx, ws.ID, "gitlab", member)
	require.NoError(t, err)
	require.Empty(t, got, "a disabled binding binds nothing")

	// Failed pipelines: the member's task with a failed open MR, plus controls.
	task := func(n int, assignee uuid.UUID) uuid.UUID {
		id := uuid.New()
		_, err := db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by, created_by_type, assignee_id, assignee_type)
			VALUES ($1, $2, $3, $4, $5, $6, 'user', $6, 'user')`, id, bound.ID, status.ID, "task", n, assignee)
		require.NoError(t, err)
		return id
	}
	link := func(taskID uuid.UUID, iid, status, pipeline string, at time.Time) {
		_, err := db.ExecContext(ctx, `INSERT INTO vcs_links (task_id, provider, link_type, external_id, url, title, status, pipeline_status, pipeline_updated_at)
			VALUES ($1, 'gitlab', 'pr', $2, $3, $4, $5, $6, $7)`,
			taskID, iid, "https://git.example/g/p/-/merge_requests/"+iid, "MR "+iid, status, pipeline, at)
		require.NoError(t, err)
	}
	now := time.Now().UTC()
	mine, theirs := task(1, member), task(2, outsider)
	link(mine, "1", "open", "failed", now.Add(-time.Hour))
	link(mine, "2", "open", "success", now.Add(-time.Hour))   // green
	link(mine, "3", "merged", "failed", now.Add(-time.Hour))  // no longer open
	link(mine, "4", "open", "failed", now.Add(-48*time.Hour)) // too old
	link(theirs, "5", "open", "failed", now.Add(-time.Hour))  // someone else's task

	links := NewVCSLinkRepo(db)
	items, err := links.FailedMergeRequestPipelines(ctx, ws.ID, member, []uuid.UUID{bound.ID}, now.Add(-24*time.Hour), 50)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "1", items[0].ExternalID)
	require.Equal(t, mine, items[0].TaskID)
	require.Equal(t, bound.ID, items[0].ProjectID)

	items, err = links.FailedMergeRequestPipelines(ctx, ws.ID, member, []uuid.UUID{unbound}, now.Add(-24*time.Hour), 50)
	require.NoError(t, err)
	require.Empty(t, items, "only the projects the caller passes")
}
