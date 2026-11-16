//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func addTestStatus(t *testing.T, db *sqlx.DB, projectID uuid.UUID, slug string, cat domain.StatusCategory) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$2,$3,$4,'#000000',5,$5,false,$6)`,
		id, projectID, slug, slug, cat, json.RawMessage(`{}`))
	require.NoError(t, err)
	return id
}

func addTestTask(t *testing.T, repo *TaskRepo, projID, statusID uuid.UUID, title string) uuid.UUID {
	t.Helper()
	task := &domain.Task{
		ID: uuid.New(), ProjectID: projID, StatusID: statusID, Title: title,
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CustomFields: json.RawMessage(`{}`), CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond), UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	require.NoError(t, repo.Create(context.Background(), task, nil))
	return task.ID
}

func TestTaskRepo_QueryTasks_TriageQueue(t *testing.T) {
	db := testDB(t)
	ws, proj, _ := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()

	inProgress := addTestStatus(t, db, proj.ID, "wip", domain.StatusCategoryInProgress)
	triage := addTestStatus(t, db, proj.ID, "triage", domain.StatusCategoryTriage)
	done := addTestStatus(t, db, proj.ID, "done", domain.StatusCategoryDone)

	autoHard := addTestTask(t, repo, proj.ID, inProgress, "auto in_progress hard gate")
	plainTriage := addTestTask(t, repo, proj.ID, triage, "triage no gate")
	released := addTestTask(t, repo, proj.ID, inProgress, "released gate")
	doneGated := addTestTask(t, repo, proj.ID, done, "done with gate")
	dup := addTestTask(t, repo, proj.ID, triage, "triage + gate")
	softGate := addTestTask(t, repo, proj.ID, inProgress, "soft gate")
	_ = addTestTask(t, repo, proj.ID, inProgress, "no gate, in progress")

	require.NoError(t, repo.SetHumanGate(ctx, autoHard, true))
	require.NoError(t, repo.SetHumanGate(ctx, released, true))
	require.NoError(t, repo.SetHumanGate(ctx, released, false))
	require.NoError(t, repo.SetHumanGate(ctx, doneGated, true))
	require.NoError(t, repo.SetHumanGate(ctx, dup, true))
	require.NoError(t, repo.SetHumanGate(ctx, softGate, true))
	require.NoError(t, repo.SetHumanGateClass(ctx, softGate, domain.HumanGateClassSoft))

	triageQ := repository.TaskQuery{WorkspaceID: ws.ID, Viewer: repository.TaskViewer{AllProjects: true}, Gated: true, Sort: repository.TaskQuerySortTriage}
	page, err := repo.QueryTasks(ctx, triageQ, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)

	var ids []uuid.UUID
	for _, tk := range page.Items {
		ids = append(ids, tk.ID)
	}
	assert.Contains(t, ids, autoHard, "auto in_progress with hard gate must be listed")
	assert.Contains(t, ids, plainTriage, "triage status without gate must be listed")
	assert.Contains(t, ids, softGate)
	assert.NotContains(t, ids, released, "released gate must not be listed")
	assert.NotContains(t, ids, doneGated, "done card with gate must not be listed")
	n := 0
	for _, id := range ids {
		if id == dup {
			n++
		}
	}
	assert.Equal(t, 1, n, "triage+gate card must appear exactly once")
	assert.Equal(t, 4, page.TotalCount)
	assert.Len(t, page.Items, 4)

	// gated cards must carry their gate state back to the caller
	for _, tk := range page.Items {
		if tk.ID == autoHard {
			assert.True(t, tk.HumanGate, "returned gated card must have human_gate=true")
			assert.Equal(t, domain.HumanGateClassHard, tk.HumanGateClass)
			assert.NotNil(t, tk.HumanGateArmedAt)
		}
	}

	// hard gates sort before soft and before ungated triage cards
	soft := -1
	for i, id := range ids {
		if id == softGate {
			soft = i
		}
	}
	for i, id := range ids {
		if id == autoHard || id == dup {
			assert.Less(t, i, soft, "hard gate must precede soft gate")
		}
	}

	// pagination keeps total
	p2, err := repo.QueryTasks(ctx, triageQ, pagination.Params{Page: 2, PageSize: 3})
	require.NoError(t, err)
	assert.Equal(t, 4, p2.TotalCount)
	assert.Len(t, p2.Items, 1)

	// hard gates only: the soft gate and the ungated triage card drop out, total follows
	hard, err := repo.QueryTasks(ctx, repository.TaskQuery{WorkspaceID: ws.ID, Viewer: repository.TaskViewer{AllProjects: true}, HardGatesOnly: true, Sort: repository.TaskQuerySortTriage}, pagination.Params{Page: 1, PageSize: 1})
	require.NoError(t, err)
	assert.Equal(t, 2, hard.TotalCount, "autoHard and dup carry a live hard gate")
	assert.Len(t, hard.Items, 1, "total_count is the match count, not the page length")
}

func queryIDs(page *pagination.Page[domain.Task]) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(page.Items))
	for _, tk := range page.Items {
		ids = append(ids, tk.ID)
	}
	return ids
}

// TestTaskRepo_QueryTasks_OnlyProjectsTheViewerMayRead pins the rule of
// middleware.UserMayAccessProject on a workspace-wide read: a member of the
// workspace who is not in a project sees none of its tasks, in the page and
// in total_count, while an owner/admin viewer sees them all.
func TestTaskRepo_QueryTasks_OnlyProjectsTheViewerMayRead(t *testing.T) {
	db := testDB(t)
	ws, mine, _ := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()
	viewer := createTestUser(t, db, "tq-viewer")

	other := uuid.New()
	_, err := db.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings) VALUES ($1,$2,'Closed project',$3,'none','{}')`,
		other, ws.ID, "closed-"+other.String()[:8])
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM project_members WHERE project_id = ANY($1)", pq.Array([]uuid.UUID{mine.ID, other}))
		_, _ = db.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2", ws.ID, viewer)
		_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", other)
		_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", other)
		_, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", other)
	})
	// a project member must be a workspace member first (fk_pm_user_workspace_member)
	addWorkspaceMember(t, db, ws.ID, viewer, "member")
	_, err = db.ExecContext(ctx, `INSERT INTO project_members (id, workspace_id, project_id, user_id) VALUES ($1,$2,$3,$4)`, uuid.New(), ws.ID, mine.ID, viewer)
	require.NoError(t, err)

	triageMine := addTestStatus(t, db, mine.ID, "triage", domain.StatusCategoryTriage)
	triageOther := addTestStatus(t, db, other, "triage", domain.StatusCategoryTriage)
	visible := addTestTask(t, repo, mine.ID, triageMine, "in my project")
	hidden := addTestTask(t, repo, other, triageOther, "in a project I am not in")

	for _, gated := range []bool{true, false} {
		member, err := repo.QueryTasks(ctx, repository.TaskQuery{WorkspaceID: ws.ID, Viewer: repository.TaskViewer{ActorID: viewer}, Gated: gated}, pagination.Params{})
		require.NoError(t, err)
		assert.Equal(t, []uuid.UUID{visible}, queryIDs(member), "gated=%v: a non-member must not see the other project's task", gated)
		assert.Equal(t, 1, member.TotalCount, "gated=%v: the hidden task is not counted either", gated)

		admin, err := repo.QueryTasks(ctx, repository.TaskQuery{WorkspaceID: ws.ID, Viewer: repository.TaskViewer{ActorID: viewer, AllProjects: true}, Gated: gated}, pagination.Params{})
		require.NoError(t, err)
		assert.ElementsMatch(t, []uuid.UUID{visible, hidden}, queryIDs(admin), "gated=%v: owner/admin sees every project", gated)
	}

	// asking for the closed project by id does not open it
	byID, err := repo.QueryTasks(ctx, repository.TaskQuery{WorkspaceID: ws.ID, Viewer: repository.TaskViewer{ActorID: viewer}, ProjectID: &other}, pagination.Params{})
	require.NoError(t, err)
	assert.Empty(t, byID.Items)
	assert.Equal(t, 0, byID.TotalCount)

	// an agent viewer is checked against project_members.agent_id, not user_id
	agent, err := repo.QueryTasks(ctx, repository.TaskQuery{WorkspaceID: ws.ID, Viewer: repository.TaskViewer{ActorID: viewer, Agent: true}}, pagination.Params{})
	require.NoError(t, err)
	assert.Empty(t, agent.Items, "the user's membership row does not admit an agent with the same id")
}

func TestTaskRepo_QueryTasks_Filters(t *testing.T) {
	db := testDB(t)
	ws, proj, todo := createTestProject(t, db)
	repo := NewTaskRepo(db)
	ctx := context.Background()
	me := createTestUser(t, db, "tq-me")
	all := repository.TaskViewer{AllProjects: true}

	done := addTestStatus(t, db, proj.ID, "done", domain.StatusCategoryDone)
	review := addTestStatus(t, db, proj.ID, "review", domain.StatusCategoryReview)

	mk := func(title string, status uuid.UUID, prio domain.Priority, due *time.Time, assignee *uuid.UUID) uuid.UUID {
		t.Helper()
		id := addTestTask(t, repo, proj.ID, status, title)
		_, err := db.ExecContext(ctx, `UPDATE tasks SET priority = $2, due_date = $3, assignee_id = $4, assignee_type = CASE WHEN $4::uuid IS NULL THEN 'unassigned' ELSE 'user' END::assignee_type WHERE id = $1`,
			id, prio, due, assignee)
		require.NoError(t, err)
		return id
	}
	day := func(d int) *time.Time { v := time.Date(2026, 12, d, 12, 0, 0, 0, time.UTC); return &v }

	urgentLate := mk("urgent due 20th", todo.ID, domain.PriorityUrgent, day(20), &me)
	lowSoon := mk("low due 5th", todo.ID, domain.PriorityLow, day(5), &me)
	highUndated := mk("high no due", review, domain.PriorityHigh, nil, nil)
	closed := mk("done urgent", done, domain.PriorityUrgent, day(1), &me)

	run := func(q repository.TaskQuery) *pagination.Page[domain.Task] {
		t.Helper()
		q.WorkspaceID = ws.ID
		if q.Viewer == (repository.TaskViewer{}) {
			q.Viewer = all
		}
		page, err := repo.QueryTasks(ctx, q, pagination.Params{Page: 1, PageSize: 50})
		require.NoError(t, err)
		return page
	}

	// default: closed categories are left out
	assert.ElementsMatch(t, []uuid.UUID{urgentLate, lowSoon, highUndated}, queryIDs(run(repository.TaskQuery{})))
	// explicit categories replace the default
	assert.ElementsMatch(t, []uuid.UUID{closed}, queryIDs(run(repository.TaskQuery{StatusCategories: []domain.StatusCategory{domain.StatusCategoryDone}})))
	assert.ElementsMatch(t, []uuid.UUID{highUndated}, queryIDs(run(repository.TaskQuery{StatusCategories: []domain.StatusCategory{domain.StatusCategoryReview}})))
	// priority, several values
	assert.ElementsMatch(t, []uuid.UUID{urgentLate, highUndated}, queryIDs(run(repository.TaskQuery{Priorities: []domain.Priority{domain.PriorityUrgent, domain.PriorityHigh}})))
	// due before: strictly earlier, undated never matches
	assert.ElementsMatch(t, []uuid.UUID{lowSoon}, queryIDs(run(repository.TaskQuery{DueBefore: day(10)})))
	// mine
	assert.ElementsMatch(t, []uuid.UUID{urgentLate, lowSoon}, queryIDs(run(repository.TaskQuery{AssigneeUserID: &me})))
	// project
	assert.Len(t, run(repository.TaskQuery{ProjectID: &proj.ID}).Items, 3)

	// sorts
	assert.Equal(t, []uuid.UUID{urgentLate, highUndated, lowSoon}, queryIDs(run(repository.TaskQuery{Sort: repository.TaskQuerySortPriority})))
	assert.Equal(t, []uuid.UUID{lowSoon, urgentLate, highUndated}, queryIDs(run(repository.TaskQuery{Sort: repository.TaskQuerySortDueDate})), "undated last")

	// total_count is the match count, not the page length
	page, err := repo.QueryTasks(ctx, repository.TaskQuery{WorkspaceID: ws.ID, Viewer: all}, pagination.Params{Page: 1, PageSize: 1})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	assert.Equal(t, 3, page.TotalCount)
}
