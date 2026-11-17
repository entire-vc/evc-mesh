package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// accessFixture is one workspace with two projects. Hidden (P) holds two tasks,
// two events and $5 of sessions; Shared (Q) holds one task, one event and $1.
// One more session ($0.50) has no task and so no project.
type accessFixture struct {
	ws, hidden, shared                  uuid.UUID
	sharedMember, hiddenMember, outside uuid.UUID // users
	agent                               uuid.UUID // member of Shared only
	hiddenTaskDone, hiddenTaskOpen      uuid.UUID
	sharedTask                          uuid.UUID
}

func seedAccessFixture(t *testing.T, db *sqlx.DB) accessFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	f := accessFixture{
		ws: uuid.New(), hidden: uuid.New(), shared: uuid.New(),
		sharedMember: uuid.New(), hiddenMember: uuid.New(), outside: uuid.New(), agent: uuid.New(),
		hiddenTaskDone: uuid.New(), hiddenTaskOpen: uuid.New(), sharedTask: uuid.New(),
	}
	sfx := f.ws.String()[:8]
	for i, u := range []uuid.UUID{f.sharedMember, f.hiddenMember, f.outside} {
		exec(`INSERT INTO users (id, email, password_hash, display_name, username, is_active) VALUES ($1,$2,'x',$3,$4,true)`,
			u, u.String()+"@analytics.test", []string{"Shared Member", "Hidden Member", "Outsider"}[i], "an-"+u.String()[:30])
	}
	exec(`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,'{}',$5,$5)`,
		f.ws, "Access WS", "access-ws-"+sfx, f.outside, now)
	statuses := map[uuid.UUID][2]uuid.UUID{}
	for name, p := range map[string]uuid.UUID{"Hidden": f.hidden, "Shared": f.shared} {
		exec(`INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings, created_at, updated_at) VALUES ($1,$2,$3,$4,'none','{}',$5,$5)`,
			p, f.ws, name+" project", "access-"+strings.ToLower(name)+"-"+sfx, now)
		open, done := uuid.New(), uuid.New()
		exec(`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$2,'Open','open','#00FF00',0,'todo',true,'{}')`, open, p)
		exec(`INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition) VALUES ($1,$2,'Done','done','#0000FF',1,'done',false,'{}')`, done, p)
		statuses[p] = [2]uuid.UUID{open, done}
	}
	exec(`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,'Access Agent',$3,'hash','agk_x')`,
		f.agent, f.ws, "access-agent-"+sfx)

	// A project member must be a workspace member (fk_pm_user_workspace_member).
	for _, u := range []uuid.UUID{f.sharedMember, f.hiddenMember, f.outside} {
		exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ($1,$2,$3,'member')`, uuid.New(), f.ws, u)
	}
	exec(`INSERT INTO project_members (id, project_id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.shared, f.ws, f.sharedMember)
	exec(`INSERT INTO project_members (id, project_id, workspace_id, user_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.hidden, f.ws, f.hiddenMember)
	exec(`INSERT INTO project_members (id, project_id, workspace_id, agent_id, role) VALUES ($1,$2,$3,$4,'member')`, uuid.New(), f.shared, f.ws, f.agent)

	task := func(id, project, status uuid.UUID, n int, assigneeType string, assignee *uuid.UUID, completed *time.Time) {
		exec(`INSERT INTO tasks (id, project_id, status_id, title, description, assignee_type, assignee_id, priority, position, custom_fields, labels, task_number, created_by, created_by_type, created_at, updated_at, completed_at)
			VALUES ($1,$2,$3,$4,'',$5,$6,'medium',$7,'{}','{}',$8,$9,'user',$10,$10,$11)`,
			id, project, status, "Task "+id.String()[:8], assigneeType, assignee, float64(n), n, f.outside, now, completed)
	}
	task(f.hiddenTaskDone, f.hidden, statuses[f.hidden][1], 1, "agent", &f.agent, &now)
	task(f.hiddenTaskOpen, f.hidden, statuses[f.hidden][0], 2, "unassigned", nil, nil)
	task(f.sharedTask, f.shared, statuses[f.shared][0], 1, "user", &f.sharedMember, nil)

	for _, ev := range []struct {
		project uuid.UUID
		typ     string
	}{{f.hidden, "error"}, {f.hidden, "error"}, {f.shared, "summary"}} {
		exec(`INSERT INTO event_bus_messages (id, workspace_id, project_id, event_type, subject, payload, created_at, expires_at) VALUES ($1,$2,$3,$4,'s','{}',$5,$6)`,
			uuid.New(), f.ws, ev.project, ev.typ, now.Add(-time.Minute), now.Add(24*time.Hour))
	}
	insertSession(t, db, f.ws, f.agent, &f.hiddenTaskDone, 5.00, 500, 500, "")
	insertSession(t, db, f.ws, f.agent, &f.sharedTask, 1.00, 100, 100, "")
	insertSession(t, db, f.ws, f.agent, nil, 0.50, 50, 50, "")

	t.Cleanup(func() {
		for _, q := range []string{
			"DELETE FROM agent_sessions WHERE workspace_id = $1",
			"DELETE FROM event_bus_messages WHERE workspace_id = $1",
			"DELETE FROM tasks WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)",
			"DELETE FROM project_members WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)",
			"DELETE FROM task_statuses WHERE project_id IN (SELECT id FROM projects WHERE workspace_id = $1)",
			"DELETE FROM workspace_members WHERE workspace_id = $1",
			"DELETE FROM agents WHERE workspace_id = $1",
			"DELETE FROM projects WHERE workspace_id = $1",
			"DELETE FROM workspaces WHERE id = $1",
		} {
			_, _ = db.ExecContext(ctx, q, f.ws)
		}
		_, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id IN ($1, $2, $3)", f.sharedMember, f.hiddenMember, f.outside)
	})
	return f
}

func accessMetrics(t *testing.T, db *sqlx.DB, f accessFixture, v AnalyticsViewer, project *uuid.UUID) (*AnalyticsMetrics, error) {
	t.Helper()
	return NewAnalyticsService(db).GetMetrics(context.Background(), AnalyticsFilter{
		WorkspaceID: f.ws,
		ProjectID:   project,
		From:        time.Now().Add(-time.Hour),
		To:          time.Now().Add(time.Hour),
		Viewer:      v,
	})
}

func timelineCreated(days []DayMetric) (n int) {
	for _, d := range days {
		n += d.Created
	}
	return n
}

// TestAnalyticsService_ProjectAccess: a workspace member outside project P sees
// none of P's tasks, events or costs; P's member (the positive control) does, and
// owner/admin see everything including the session no project can claim.
func TestAnalyticsService_ProjectAccess(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)

	t.Run("member of the other project sees none of P", func(t *testing.T) {
		m, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.sharedMember}, nil)
		require.NoError(t, err)
		require.Equal(t, 1, m.TaskMetrics.Total)
		require.Equal(t, 1, m.TaskMetrics.CreatedThisPeriod)
		require.Equal(t, 0, m.TaskMetrics.CompletedThisPeriod)
		require.Equal(t, 1, timelineCreated(m.Timeline))
		require.Len(t, m.TaskMetrics.ByProject, 1)
		require.Equal(t, f.shared, m.TaskMetrics.ByProject[0].ProjectID)
		require.Empty(t, m.AgentMetrics.TasksByAgent, "the agent's done task lives in P")
		require.Equal(t, 1, m.EventMetrics.TotalEvents)
		require.NotContains(t, m.EventMetrics.ByType, "error")
		require.InDelta(t, 1.00, m.CostMetrics.TotalCost, 1e-9)
		require.Equal(t, 1, m.CostMetrics.SessionCount)
		require.Len(t, m.CostMetrics.ByProject, 1)
		require.Equal(t, f.shared, m.CostMetrics.ByProject[0].ProjectID)
		require.Len(t, m.CostMetrics.TopTasks, 1)
		require.Equal(t, f.sharedTask, m.CostMetrics.TopTasks[0].TaskID)
		require.Len(t, m.CostMetrics.ByAgent, 1)
		require.InDelta(t, 1.00, m.CostMetrics.ByAgent[0].Cost, 1e-9)
		var dayCost float64
		for _, d := range m.CostMetrics.ByDay {
			dayCost += d.Cost
		}
		require.InDelta(t, 1.00, dayCost, 1e-9)
	})

	t.Run("asking for P by id is refused", func(t *testing.T) {
		_, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.sharedMember}, &f.hidden)
		var apiErr *apierror.Error
		require.True(t, errors.As(err, &apiErr), "got %v", err)
		require.Equal(t, http.StatusForbidden, apiErr.Code)
	})

	t.Run("positive control: P's member sees P", func(t *testing.T) {
		m, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.hiddenMember}, nil)
		require.NoError(t, err)
		require.Equal(t, 2, m.TaskMetrics.Total)
		require.Equal(t, 1, m.TaskMetrics.CompletedThisPeriod)
		require.Equal(t, 2, m.EventMetrics.ByType["error"])
		require.InDelta(t, 5.00, m.CostMetrics.TotalCost, 1e-9)
		require.Len(t, m.AgentMetrics.TasksByAgent, 1)
		require.Equal(t, 1, m.AgentMetrics.TasksByAgent[0].Completed)

		byID, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.hiddenMember}, &f.hidden)
		require.NoError(t, err)
		require.Equal(t, 2, byID.TaskMetrics.Total)
	})

	t.Run("owner and admin see every project and unplaced sessions", func(t *testing.T) {
		m, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.outside, AllProjects: true}, nil)
		require.NoError(t, err)
		require.Equal(t, 3, m.TaskMetrics.Total)
		require.Equal(t, 3, m.EventMetrics.TotalEvents)
		require.InDelta(t, 6.50, m.CostMetrics.TotalCost, 1e-9)
		require.Equal(t, 3, m.CostMetrics.SessionCount)
	})

	t.Run("owner and admin may not ask for a project of another workspace", func(t *testing.T) {
		other := seedAccessFixture(t, db)
		for _, p := range []uuid.UUID{other.hidden, uuid.New()} {
			_, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.outside, AllProjects: true}, &p)
			var apiErr *apierror.Error
			require.True(t, errors.As(err, &apiErr), "project %s: got %v", p, err)
			require.Equal(t, http.StatusForbidden, apiErr.Code)
		}
		// Positive control: the same viewer asking for a project of its own workspace.
		m, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.outside, AllProjects: true}, &f.hidden)
		require.NoError(t, err)
		require.Equal(t, 2, m.TaskMetrics.Total)
	})

	t.Run("agent sees its own memberships only", func(t *testing.T) {
		m, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.agent, Agent: true}, nil)
		require.NoError(t, err)
		require.Equal(t, 1, m.TaskMetrics.Total)
		require.InDelta(t, 1.00, m.CostMetrics.TotalCost, 1e-9)
	})

	t.Run("no project membership, no viewer: nothing", func(t *testing.T) {
		for _, v := range []AnalyticsViewer{{ActorID: f.outside}, {}} {
			m, err := accessMetrics(t, db, f, v, nil)
			require.NoError(t, err)
			require.Equal(t, 0, m.TaskMetrics.Total)
			require.Equal(t, 0, m.EventMetrics.TotalEvents)
			require.Zero(t, m.CostMetrics.TotalCost)
			require.Empty(t, m.TaskMetrics.ByAssignee)
		}
	})
}

// TestAnalyticsService_TaskBreakdowns covers the by-assignee and by-project cuts
// used by the tasks-breakdown widget.
func TestAnalyticsService_TaskBreakdowns(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)

	m, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.outside, AllProjects: true}, nil)
	require.NoError(t, err)

	got := map[string]AssigneeCountRow{}
	for _, r := range m.TaskMetrics.ByAssignee {
		got[r.AssigneeType] = r
	}
	require.Len(t, got, 3)
	require.Equal(t, AssigneeCountRow{AssigneeID: &f.agent, AssigneeType: "agent", Name: "Access Agent", Count: 1}, got["agent"])
	require.Equal(t, AssigneeCountRow{AssigneeID: &f.sharedMember, AssigneeType: "user", Name: "Shared Member", Count: 1}, got["user"])
	require.Equal(t, AssigneeCountRow{AssigneeType: "unassigned", Count: 1}, got["unassigned"])

	require.Equal(t, []ProjectCountRow{
		{ProjectID: f.hidden, ProjectName: "Hidden project", Count: 2},
		{ProjectID: f.shared, ProjectName: "Shared project", Count: 1},
	}, m.TaskMetrics.ByProject)

	scoped, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.outside, AllProjects: true}, &f.shared)
	require.NoError(t, err)
	require.Equal(t, []ProjectCountRow{{ProjectID: f.shared, ProjectName: "Shared project", Count: 1}}, scoped.TaskMetrics.ByProject)
	require.Len(t, scoped.TaskMetrics.ByAssignee, 1)
	require.Equal(t, "user", scoped.TaskMetrics.ByAssignee[0].AssigneeType)
}

// TestAnalyticsService_CostOtherWorkspaceTask: agent_sessions.workspace_id does not
// bind the session's task to the same workspace. A session of workspace A that
// points at a task of workspace B must not bring B's project or task into A's
// costs, not even for A's owner; A's own projects stay (positive control), and the
// session still counts in A's totals as a session without a task.
func TestAnalyticsService_CostOtherWorkspaceTask(t *testing.T) {
	db := testDB(t)
	a := seedAccessFixture(t, db)
	b := seedAccessFixture(t, db)
	insertSession(t, db, a.ws, a.agent, &b.hiddenTaskDone, 7.00, 700, 700, "")
	// Runs before the fixtures' own cleanups (LIFO): B's task cannot go while A's session points at it.
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM agent_sessions WHERE workspace_id = $1 AND task_id = $2", a.ws, b.hiddenTaskDone)
	})

	projectIDs := func(rows []ProjectCostRow) (ids []uuid.UUID) {
		for _, r := range rows {
			ids = append(ids, r.ProjectID)
		}
		return ids
	}
	taskIDs := func(rows []TaskCostRow) (ids []uuid.UUID) {
		for _, r := range rows {
			ids = append(ids, r.TaskID)
		}
		return ids
	}

	for _, tc := range []struct {
		name    string
		project *uuid.UUID
	}{{"whole workspace", nil}, {"filtered to A's project", &a.hidden}} {
		t.Run("owner of A, "+tc.name, func(t *testing.T) {
			m, err := accessMetrics(t, db, a, AnalyticsViewer{ActorID: a.outside, AllProjects: true}, tc.project)
			require.NoError(t, err)
			require.NotContains(t, projectIDs(m.CostMetrics.ByProject), b.hidden, "B's project in A's cost by project")
			require.NotContains(t, taskIDs(m.CostMetrics.TopTasks), b.hiddenTaskDone, "B's task in A's top tasks")
			require.Contains(t, projectIDs(m.CostMetrics.ByProject), a.hidden, "positive control: A's own project")
			require.Contains(t, taskIDs(m.CostMetrics.TopTasks), a.hiddenTaskDone, "positive control: A's own task")
		})
	}

	t.Run("owner of A: the session counts as one without a task", func(t *testing.T) {
		m, err := accessMetrics(t, db, a, AnalyticsViewer{ActorID: a.outside, AllProjects: true}, nil)
		require.NoError(t, err)
		require.InDelta(t, 5.00+1.00+0.50+7.00, m.CostMetrics.TotalCost, 1e-9)
		var byProject float64
		for _, r := range m.CostMetrics.ByProject {
			byProject += r.Cost
		}
		require.InDelta(t, 6.00, byProject, 1e-9, "only the sessions on A's tasks")
	})

	t.Run("member of A's project: B's session is not theirs", func(t *testing.T) {
		m, err := accessMetrics(t, db, a, AnalyticsViewer{ActorID: a.hiddenMember}, nil)
		require.NoError(t, err)
		require.InDelta(t, 5.00, m.CostMetrics.TotalCost, 1e-9)
		require.Equal(t, []uuid.UUID{a.hidden}, projectIDs(m.CostMetrics.ByProject))
	})
}

// TestAnalyticsService_Overdue: overdue = past due and not done or cancelled, now,
// over the viewer's projects only.
func TestAnalyticsService_Overdue(t *testing.T) {
	db := testDB(t)
	f := seedAccessFixture(t, db)
	ctx := context.Background()
	setDue := func(task uuid.UUID, due time.Time) {
		t.Helper()
		_, err := db.ExecContext(ctx, `UPDATE tasks SET due_date = $2 WHERE id = $1`, task, due)
		require.NoError(t, err)
	}
	yesterday, tomorrow := time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour)
	setDue(f.hiddenTaskOpen, yesterday) // overdue
	setDue(f.hiddenTaskDone, yesterday) // past due but done: not overdue
	setDue(f.sharedTask, tomorrow)      // not due yet

	for _, tc := range []struct {
		name   string
		viewer AnalyticsViewer
		want   int
	}{
		{"owner sees the overdue task", AnalyticsViewer{ActorID: f.outside, AllProjects: true}, 1},
		{"P's member sees it (positive control)", AnalyticsViewer{ActorID: f.hiddenMember}, 1},
		{"member of the other project does not", AnalyticsViewer{ActorID: f.sharedMember}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := accessMetrics(t, db, f, tc.viewer, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, m.TaskMetrics.Overdue)
		})
	}

	setDue(f.sharedTask, yesterday)
	m, err := accessMetrics(t, db, f, AnalyticsViewer{ActorID: f.sharedMember}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, m.TaskMetrics.Overdue, "the same member once their own task is past due")
}

// TestAnalyticsService_LeaderboardPeriod: tasks_by_agent (the agent-leaderboard
// widget) counts only done tasks whose completed_at falls inside [From, To],
// both bounds inclusive, and still only over the viewer's projects.
func TestAnalyticsService_LeaderboardPeriod(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	f := seedAccessFixture(t, db) // hiddenTaskDone: agent, done, completed now

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 2, 23, 59, 59, 0, time.UTC)
	statusOf := func(project uuid.UUID, category string) uuid.UUID {
		var id uuid.UUID
		require.NoError(t, db.GetContext(ctx, &id, `SELECT id FROM task_statuses WHERE project_id = $1 AND category = $2`, project, category))
		return id
	}
	n := 10
	task := func(project uuid.UUID, category string, completed time.Time) {
		n++
		_, err := db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, status_id, title, description, assignee_type, assignee_id, priority, position, custom_fields, labels, task_number, created_by, created_by_type, created_at, updated_at, completed_at)
			VALUES ($1,$2,$3,'Leaderboard task','','agent',$4,'medium',$5,'{}','{}',$6,$7,'user',$8,$8,$8)`,
			uuid.New(), project, statusOf(project, category), f.agent, float64(n), n, f.outside, completed)
		require.NoError(t, err)
	}
	// Hidden: before, both bounds, inside, after; plus an open task inside.
	task(f.hidden, "done", from.Add(-time.Second))
	task(f.hidden, "done", from)
	task(f.hidden, "done", from.Add(12*time.Hour))
	task(f.hidden, "done", to)
	task(f.hidden, "done", to.Add(time.Second))
	task(f.hidden, "todo", from.Add(time.Hour))
	// Shared: one done task inside the period.
	task(f.shared, "done", from.Add(time.Hour))

	leaderboard := func(v AnalyticsViewer, project *uuid.UUID, from, to time.Time) []AgentTaskRow {
		t.Helper()
		m, err := NewAnalyticsService(db).GetMetrics(ctx, AnalyticsFilter{WorkspaceID: f.ws, ProjectID: project, From: from, To: to, Viewer: v})
		require.NoError(t, err)
		return m.AgentMetrics.TasksByAgent
	}
	one := func(completed int) []AgentTaskRow {
		return []AgentTaskRow{{AgentID: f.agent, AgentName: "Access Agent", Completed: completed}}
	}
	hiddenViewer := AnalyticsViewer{ActorID: f.hiddenMember}
	sharedViewer := AnalyticsViewer{ActorID: f.sharedMember}
	owner := AnalyticsViewer{ActorID: f.outside, AllProjects: true}

	t.Run("P's member: only the in-period done tasks of P, bounds inclusive", func(t *testing.T) {
		require.Equal(t, one(3), leaderboard(hiddenViewer, nil, from, to))
		require.Equal(t, one(3), leaderboard(hiddenViewer, &f.hidden, from, to))
	})
	t.Run("member of the other project: its own in-period task, none of P", func(t *testing.T) {
		require.Equal(t, one(1), leaderboard(sharedViewer, nil, from, to))
	})
	t.Run("owner: every project, still bounded by the period", func(t *testing.T) {
		require.Equal(t, one(4), leaderboard(owner, nil, from, to))
		require.Equal(t, one(1), leaderboard(owner, &f.shared, from, to))
	})
	t.Run("a period with no completions is empty", func(t *testing.T) {
		future := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		require.Empty(t, leaderboard(owner, nil, future, future.Add(48*time.Hour-time.Second)))
		// Positive control: the same viewer over the fixture's own completion (now).
		require.Equal(t, one(1), leaderboard(owner, nil, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	})
	t.Run("no project membership: nothing", func(t *testing.T) {
		require.Empty(t, leaderboard(AnalyticsViewer{ActorID: f.outside}, nil, from, to))
	})
}
