package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/handler"
	"github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// These exercise the real handler, projection and SQL, including SQL redaction.
// Only authentication is replaced; RequireProjectMember uses the actual database.
type graphDBFixture struct {
	db                            *sqlx.DB
	ws, project, todo, done, user uuid.UUID
}

func newGraphDBFixture(t *testing.T, count int) graphDBFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("DATABASE_URL is required for graph PostgreSQL checks in CI; refusing to skip silently")
		}
		t.Skip("DATABASE_URL is required for graph PostgreSQL checks")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err, "graph test database must be reachable")
	t.Cleanup(func() { _ = db.Close() })
	f := graphDBFixture{db: db, ws: uuid.New(), project: uuid.New(), todo: uuid.New(), done: uuid.New(), user: uuid.New()}
	suffix := f.ws.String()
	f.exec(t, `INSERT INTO users(id,email,password_hash,display_name,username) VALUES($1,$2,'x','Graph reader',$3)`, f.user, suffix+"@example.test", suffix)
	f.exec(t, `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Graph fixture',$2,$3)`, f.ws, suffix, f.user)
	f.exec(t, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES($1,$2,$3,'member')`, uuid.New(), f.ws, f.user)
	f.addProject(t, f.project, f.todo, f.done, true)
	f.exec(t, `INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by) SELECT gen_random_uuid(),$1,$2,'Task '||i,i,$3 FROM generate_series(1,$4) i`, f.project, f.todo, f.user, count)
	t.Cleanup(func() {
		_, err := db.Exec(`DELETE FROM project_members WHERE workspace_id=$1`, f.ws)
		require.NoError(t, err)
		_, err = db.Exec(`DELETE FROM workspaces WHERE id=$1`, f.ws)
		require.NoError(t, err)
		_, err = db.Exec(`DELETE FROM users WHERE id=$1`, f.user)
		require.NoError(t, err)
	})
	return f
}

func (f graphDBFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := f.db.Exec(q, args...)
	require.NoError(t, err)
}

// Seed edges retained from before the completion-graph write guard existed.
// The exclusive table lock and trigger change remain inside this transaction:
// concurrent writers cannot observe the disabled guard, and rollback restores it.
func (f graphDBFixture) seedHistoricalEdges(t *testing.T, q string, args ...any) {
	t.Helper()
	tx, err := f.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`ALTER TABLE task_dependencies DISABLE TRIGGER completion_graph_dependencies`)
	require.NoError(t, err)
	_, err = tx.Exec(q, args...)
	require.NoError(t, err)
	_, err = tx.Exec(`ALTER TABLE task_dependencies ENABLE TRIGGER completion_graph_dependencies`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
}

func (f graphDBFixture) addProject(t *testing.T, project, todo, done uuid.UUID, member bool) {
	f.exec(t, `INSERT INTO projects(id,workspace_id,name,slug) VALUES($1,$2,'Graph project',$3)`, project, f.ws, project.String())
	f.exec(t, `INSERT INTO task_statuses(id,project_id,name,slug,color,position,category,is_default) VALUES($1,$2,'Todo','todo','#ffffff',0,'todo',true),($3,$2,'Done','done','#000000',1,'done',false)`, todo, project, done)
	if member {
		f.exec(t, `INSERT INTO project_members(id,workspace_id,project_id,user_id) VALUES($1,$2,$3,$4)`, uuid.New(), f.ws, project, f.user)
	}
}

func (f graphDBFixture) ids(t *testing.T) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	require.NoError(t, f.db.Select(&ids, `SELECT id FROM tasks WHERE project_id=$1 ORDER BY task_number`, f.project))
	return ids
}

func (f graphDBFixture) read(t *testing.T, focus uuid.UUID) *domain.GraphSnapshot {
	t.Helper()
	s, err := postgres.NewTaskDependencyRepo(f.db).ReadGraph(context.Background(), f.project, focus, domain.GraphScope{WorkspaceID: f.ws, ActorID: f.user})
	require.NoError(t, err)
	return s
}

func graphFixtureNode(t *testing.T, s *domain.GraphSnapshot, id uuid.UUID) domain.GraphNode {
	t.Helper()
	for _, n := range s.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("missing fixture node %s", id)
	return domain.GraphNode{}
}

func TestDependencyGraphDB_FocusReadsNeighborhood(t *testing.T) {
	f := newGraphDBFixture(t, 201)
	ids := f.ids(t)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), ids[0], ids[200])
	s := f.read(t, ids[0])
	assert.Len(t, s.Nodes, 2, "focus must not fetch unrelated project metadata")
	assert.Len(t, s.Dependencies, 1)
	assert.Equal(t, 1, graphFixtureNode(t, s, ids[0]).UnfinishedBlockers)
	rec := f.request(t, "", f.user, "member")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var legacy service.GraphResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &legacy))
	assert.Len(t, legacy.Tasks, 201, "existing route must retain its complete graph contract")
	assert.Len(t, legacy.Dependencies, 1)
}

func TestDependencyGraphDB_RestrictedStatusDoesNotLeak(t *testing.T) {
	f := newGraphDBFixture(t, 1)
	center := f.ids(t)[0]
	project, todo, done, blocker := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.addProject(t, project, todo, done, false)
	f.exec(t, `INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by,human_gate) VALUES($1,$2,$3,'Private title',1,$4,true)`, blocker, project, done, f.user)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), center, blocker)
	s := f.read(t, center)
	n := graphFixtureNode(t, s, center)
	assert.Equal(t, 1, n.Blockers)
	assert.Equal(t, 1, n.UnknownBlockers)
	assert.Equal(t, 1, n.UnfinishedBlockers, "unknown blockers must not imply ready")
	view, err := service.ProjectGraph(s, f.project, service.GraphOptions{Search: "Task 1"})
	require.NoError(t, err)
	assert.Equal(t, 1, view.Unknown, "overview reports unique restricted boundary nodes")
	assert.False(t, view.VisibilityComplete)
	private := graphFixtureNode(t, s, blocker)
	assert.Equal(t, domain.GraphNode{ID: blocker, Availability: "restricted", UpdatedAt: time.Unix(0, 0).UTC()}, private)
	f.exec(t, `UPDATE tasks SET status_id=$1, updated_at=now() WHERE id=$2`, todo, blocker)
	changed := f.read(t, center)
	assert.Equal(t, s.Revision, changed.Revision, "private status changes must not leak via counts or revision")
	assert.Equal(t, s.Nodes, changed.Nodes)
	// Positive control: explicit membership reveals canonical metadata and status.
	f.exec(t, `INSERT INTO project_members(id,workspace_id,project_id,user_id) VALUES($1,$2,$3,$4)`, uuid.New(), f.ws, project, f.user)
	s = f.read(t, center)
	assert.Equal(t, "Private title", graphFixtureNode(t, s, blocker).Title)
	assert.Zero(t, graphFixtureNode(t, s, center).UnknownBlockers)
	f.exec(t, `UPDATE tasks SET status_id=$1 WHERE id=$2`, done, blocker)
	assert.Zero(t, graphFixtureNode(t, f.read(t, center), center).UnfinishedBlockers)
	// Deleted endpoints remain diagnostic identities, never terminal-ready evidence.
	f.exec(t, `UPDATE tasks SET deleted_at=now() WHERE id=$1`, blocker)
	s = f.read(t, center)
	assert.Equal(t, "missing", graphFixtureNode(t, s, blocker).Availability)
	assert.Equal(t, 1, graphFixtureNode(t, s, center).UnknownBlockers)
	assert.Equal(t, 1, graphFixtureNode(t, s, center).UnfinishedBlockers)
}

func TestDependencyGraphDB_RevisionTracksVisibleMetadata(t *testing.T) {
	f := newGraphDBFixture(t, 1)
	before := f.read(t, uuid.Nil)
	f.exec(t, `UPDATE projects SET name='Renamed project' WHERE id=$1`, f.project)
	after := f.read(t, uuid.Nil)
	assert.NotEqual(t, before.Revision, after.Revision)
	_, err := service.ProjectGraph(after, f.project, service.GraphOptions{Revision: before.Revision})
	require.Error(t, err, "stale continuation must be rejected")
}

func TestDependencyGraphDB_RestrictedCycleTopologyDoesNotLeak(t *testing.T) {
	f := newGraphDBFixture(t, 2)
	ids := f.ids(t)
	project, todo, done, private, extra := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.addProject(t, project, todo, done, false)
	f.exec(t, `INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by) VALUES($1,$3,$4,'Private cycle',1,$5),($2,$3,$4,'Private extension',2,$5)`, private, extra, project, todo, f.user)
	f.seedHistoricalEdges(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES(gen_random_uuid(),$1,$2,'blocks'),(gen_random_uuid(),$2,$1,'blocks'),(gen_random_uuid(),$1,$3,'blocks'),(gen_random_uuid(),$3,$1,'blocks')`, ids[0], ids[1], private)
	queries := []string{"", "?mode=overview", "?focus=" + ids[0].String()}
	before := make([]string, len(queries))
	for i, query := range queries {
		rec := f.request(t, query, f.user, "member")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		before[i] = rec.Body.String()
	}
	// Change only inaccessible internal edges: the direct boundary identities,
	// visible task metadata and visible incident edges remain exactly the same.
	f.seedHistoricalEdges(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES(gen_random_uuid(),$1,$2,'blocks'),(gen_random_uuid(),$2,$1,'blocks')`, private, extra)
	for i, query := range queries {
		rec := f.request(t, query, f.user, "member")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, before[i], rec.Body.String(), "private cycle extension must not affect any observable payload: %s", query)
	}
	var graph service.GraphResponse
	rec := f.request(t, queries[2], f.user, "member")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &graph))
	assert.Equal(t, 2, graph.CycleCount, "only authorized topology contributes cycle diagnostics")
	assert.ElementsMatch(t, ids, graph.CycleIDs, "an entirely visible cycle is still diagnosed")
	assert.False(t, graph.VisibilityComplete, "restricted direct boundaries remain explicit")
	// Positive control: authorized cross-project topology is diagnosed in full,
	// including the accessible component member outside the one-hop payload.
	f.exec(t, `INSERT INTO project_members(id,workspace_id,project_id,user_id) VALUES($1,$2,$3,$4)`, uuid.New(), f.ws, project, f.user)
	rec = f.request(t, queries[2], f.user, "member")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &graph))
	assert.Equal(t, 4, graph.CycleCount)
	assert.ElementsMatch(t, append(ids, private), graph.CycleIDs)
	assert.NotContains(t, graph.CycleIDs, extra, "cycle IDs remain bounded to the payload")
	assert.True(t, graph.VisibilityComplete)
}

func TestDependencyGraphDB_RestrictedCycleExistenceDoesNotLeak(t *testing.T) {
	f := newGraphDBFixture(t, 1)
	center := f.ids(t)[0]
	project, todo, done, a, b := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.addProject(t, project, todo, done, false)
	f.exec(t, `INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by) VALUES($1,$3,$4,'Private A',1,$5),($2,$3,$4,'Private B',2,$5)`, a, b, project, todo, f.user)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES(gen_random_uuid(),$1,$2,'blocks'),(gen_random_uuid(),$3,$1,'blocks')`, center, b, a)
	queries := []string{"", "?mode=overview", "?focus=" + center.String()}
	before := make([]string, len(queries))
	for i, query := range queries {
		rec := f.request(t, query, f.user, "member")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		before[i] = rec.Body.String()
	}
	// Close center→private A→private B→center using only a private edge.
	f.seedHistoricalEdges(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES(gen_random_uuid(),$1,$2,'blocks')`, b, a)
	for i, query := range queries {
		rec := f.request(t, query, f.user, "member")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.JSONEq(t, before[i], rec.Body.String(), "restricted cycle existence is not observable: %s", query)
	}
	f.exec(t, `INSERT INTO project_members(id,workspace_id,project_id,user_id) VALUES($1,$2,$3,$4)`, uuid.New(), f.ws, project, f.user)
	rec := f.request(t, queries[2], f.user, "member")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var graph service.GraphResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &graph))
	assert.Equal(t, 3, graph.CycleCount, "authorized cross-project cycle remains observable")
	assert.ElementsMatch(t, []uuid.UUID{center, a, b}, graph.CycleIDs)
}

func TestDependencyGraphDB_RBACAndTenantBoundary(t *testing.T) {
	f := newGraphDBFixture(t, 2)
	ids := f.ids(t)
	privateProject, todo, done := uuid.New(), uuid.New(), uuid.New()
	f.addProject(t, privateProject, todo, done, false)
	repo := postgres.NewTaskDependencyRepo(f.db)
	ctx := context.Background()
	_, err := repo.ReadGraph(ctx, privateProject, uuid.Nil, domain.GraphScope{WorkspaceID: f.ws, ActorID: f.user})
	require.Error(t, err)
	_, err = repo.ReadGraph(ctx, privateProject, uuid.Nil, domain.GraphScope{WorkspaceID: f.ws, ActorID: f.user, Admin: true})
	require.NoError(t, err, "human workspace administrator retains existing policy")
	agent := uuid.New()
	f.exec(t, `INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'Graph agent',$3,'x','test')`, agent, f.ws, agent.String())
	_, err = repo.ReadGraph(ctx, f.project, uuid.Nil, domain.GraphScope{WorkspaceID: f.ws, ActorID: agent, Agent: true, Admin: true})
	require.Error(t, err, "agent role never bypasses explicit project membership")
	f.exec(t, `INSERT INTO project_members(id,workspace_id,project_id,agent_id) VALUES($1,$2,$3,$4)`, uuid.New(), f.ws, f.project, agent)
	_, err = repo.ReadGraph(ctx, f.project, ids[0], domain.GraphScope{WorkspaceID: f.ws, ActorID: agent, Agent: true, Admin: true})
	require.NoError(t, err)
	foreign := newGraphDBFixture(t, 1)
	focus := foreign.ids(t)[0]
	// Even an accessible foreign-workspace task must not cross this project's tenant.
	f.exec(t, `INSERT INTO workspace_members(id,workspace_id,user_id) VALUES($1,$2,$3)`, uuid.New(), foreign.ws, f.user)
	f.exec(t, `INSERT INTO project_members(id,workspace_id,project_id,user_id) VALUES($1,$2,$3,$4)`, uuid.New(), foreign.ws, foreign.project, f.user)
	_, err = repo.ReadGraph(ctx, f.project, focus, domain.GraphScope{WorkspaceID: f.ws, ActorID: f.user})
	require.Error(t, err)
	// Historical cross-workspace edges are invisible to this workspace: no edge, no
	// foreign node (not even a restricted id), and no contribution to blocker counts.
	f.seedHistoricalEdges(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), ids[0], focus)
	// The fixture must not weaken subsequent normal writes.
	_, err = f.db.Exec(`INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), ids[1], focus)
	require.ErrorContains(t, err, "dependency endpoints must be distinct available tasks in the same workspace", "the write guard must be restored after historical seeding")
	for _, centre := range []uuid.UUID{uuid.Nil, ids[0]} {
		s := f.read(t, centre)
		for _, n := range s.Nodes {
			assert.NotEqual(t, focus, n.ID, "a foreign-workspace task id must not be returned, even as a restricted node")
		}
		for _, d := range s.Dependencies {
			assert.NotEqual(t, focus, d.DependsOnTaskID, "a cross-workspace edge must not be returned")
			assert.NotEqual(t, focus, d.TaskID, "a cross-workspace edge must not be returned")
		}
		assert.Empty(t, s.Topology)
		local := graphFixtureNode(t, s, ids[0])
		assert.Zero(t, local.Blockers, "a cross-workspace blocker must not be counted")
		assert.Zero(t, local.UnknownBlockers, "a cross-workspace blocker must not surface as an unknown one")
	}
	// The foreign side sees its own workspace only: the edge is not served from there either.
	fs, err := repo.ReadGraph(ctx, foreign.project, uuid.Nil, domain.GraphScope{WorkspaceID: foreign.ws, ActorID: f.user})
	require.NoError(t, err)
	for _, d := range fs.Dependencies {
		assert.NotEqual(t, ids[0], d.TaskID)
	}
	assert.Zero(t, graphFixtureNode(t, fs, focus).Dependents)
}

func (f graphDBFixture) request(t *testing.T, query string, actor uuid.UUID, role string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(middleware.ContextKeyUserID, actor)
			c.Set(middleware.ContextKeyWorkspaceID, f.ws)
			c.Set(middleware.ContextKeyWorkspaceRole, role)
			return next(c)
		}
	})
	h := handler.NewDependencyHandler(nil, nil).WithGraphReader(postgres.NewTaskDependencyRepo(f.db))
	e.GET("/projects/:proj_id/dependency-graph", h.DependencyGraph, middleware.RequireProjectMember(f.db))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/"+f.project.String()+"/dependency-graph"+query, http.NoBody))
	return rec
}

func TestDependencyGraphDB_HTTPDensePaginationAndFocus(t *testing.T) {
	f := newGraphDBFixture(t, 1000)
	// Three distinct prerequisites per task, with canonical cyclic connectivity.
	f.seedHistoricalEdges(t, `WITH numbered AS (SELECT id,task_number AS n FROM tasks WHERE project_id=$1) INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) SELECT gen_random_uuid(),a.id,b.id,'blocks' FROM numbered a CROSS JOIN generate_series(1,3) step JOIN numbered b ON b.n=((a.n+step-1)%1000)+1`, f.project)
	// Fresh disposable fixtures otherwise retain empty-table planner estimates
	// from earlier cleanup. Measure the seeded graph with current statistics.
	f.exec(t, `ANALYZE tasks, task_dependencies, projects, project_members`)
	var revision string
	seen := map[uuid.UUID]bool{}
	var last string
	for page := 1; page <= 5; page++ {
		rec := f.request(t, fmt.Sprintf("?page=%d&page_size=200&revision=%s", page, revision), f.user, "member")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var graph service.GraphResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &graph))
		require.Equal(t, 1000, graph.Total)
		require.Len(t, graph.Tasks, 200)
		if page == 1 {
			revision = graph.Revision
		}
		require.Equal(t, revision, graph.Revision)
		assert.Equal(t, page < 5, graph.HasMore)
		assert.Equal(t, page == 5, graph.Complete)
		for _, n := range graph.Tasks {
			assert.False(t, seen[n.ID])
			seen[n.ID] = true
			assert.Greater(t, n.ID.String(), last)
			last = n.ID.String()
			assert.Equal(t, 3, n.Blockers)
			assert.Equal(t, 3, n.UnfinishedBlockers)
			assert.Equal(t, 3, n.Dependents)
		}
	}
	require.Len(t, seen, 1000)
	ids := f.ids(t)
	rec := f.request(t, "?focus="+ids[999].String()+"&search=does-not-match", f.user, "member")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var graph service.GraphResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &graph))
	assert.Len(t, graph.Tasks, 7)
	assert.Len(t, graph.Dependencies, 6)
	assert.Equal(t, 1000, graph.CycleCount)
	assert.Equal(t, 3, graph.Before.Total)
	assert.Equal(t, 3, graph.After.Total)
	assert.True(t, graph.Complete)
	// No graph GET may mutate canonical tasks/edges.
	var edges int
	require.NoError(t, f.db.Get(&edges, `SELECT count(*) FROM task_dependencies d JOIN tasks t ON t.id=d.task_id WHERE t.project_id=$1`, f.project))
	assert.Equal(t, 3000, edges)
	assert.Equal(t, http.StatusForbidden, f.request(t, "", uuid.New(), "member").Code)
	assert.Equal(t, http.StatusNotFound, f.request(t, "?focus="+uuid.New().String(), f.user, "member").Code)
	f.exec(t, `UPDATE tasks SET title='Updated' WHERE id=$1`, ids[0])
	assert.Equal(t, http.StatusConflict, f.request(t, "?page=2&revision="+revision, f.user, "member").Code)
	for _, query := range []string{"?page=-1", "?page=1000001", "?focus=invalid", "?completed=invalid", "?page_size=1000"} {
		assert.Equal(t, http.StatusBadRequest, f.request(t, query, f.user, "member").Code, query)
	}
	// Optional instrumentation on the dedicated verification DB, never reset global stats.
	var enabled bool
	require.NoError(t, f.db.Get(&enabled, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements')`))
	readCalls := func() int64 {
		var n int64
		require.NoError(t, f.db.Get(&n, `SELECT COALESCE(sum(calls),0) FROM pg_stat_statements WHERE query LIKE '/* graph_read */%'`))
		return n
	}
	latencies := make([]time.Duration, 0, 20)
	for i := 0; i < 20; i++ {
		var before int64
		if enabled {
			before = readCalls()
		}
		started := time.Now()
		s := f.read(t, ids[999])
		_, err := service.ProjectGraph(s, f.project, service.GraphOptions{Focus: ids[999]})
		require.NoError(t, err)
		latencies = append(latencies, time.Since(started))
		if enabled {
			assert.Equal(t, int64(6), readCalls()-before, "focus SQL reads must stay constant, never N+1")
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("real PostgreSQL 1000 nodes / 3000 edges: focus p50=%s p95=%s; SQL instrumentation=%t", latencies[10], latencies[18], enabled)
}

// A member of project A who is not a member of project B must not receive B's
// task fields through the HTTP endpoint, whether B's task is the focus itself or
// only linked to an A task by a dependency.
func TestDependencyGraphDB_HTTPForeignProjectFocusDoesNotLeakFields(t *testing.T) {
	f := newGraphDBFixture(t, 1)
	local := f.ids(t)[0]
	other, todo, done, secret := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.addProject(t, other, todo, done, false)
	f.exec(t, `INSERT INTO tasks(id,project_id,status_id,title,description,task_number,created_by,assignee_id,assignee_type) VALUES($1,$2,$3,'Leaky-title-7f3a','Leaky-description-7f3a',1,$4,$4,'user')`, secret, other, todo, f.user)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), local, secret)

	// Positive control: once the user is a member of B the same marker is served,
	// so the absence assertions below cannot pass on an empty or erroring body.
	f.exec(t, `INSERT INTO project_members(id,workspace_id,project_id,user_id) VALUES($1,$2,$3,$4)`, uuid.New(), f.ws, other, f.user)
	rec := f.request(t, "?focus="+local.String(), f.user, "member")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Leaky-title-7f3a", "control: member of B must see B's title")
	require.Contains(t, rec.Body.String(), "Graph reader", "control: member of B must see B's assignee name")
	f.exec(t, `DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`, other, f.user)

	// The HTTP layer would also reject a redacted focus, so pin the repository
	// guard itself: without it ReadGraph would return a snapshot with a restricted
	// centre node instead of this error.
	_, err := postgres.NewTaskDependencyRepo(f.db).ReadGraph(context.Background(), f.project, secret, domain.GraphScope{WorkspaceID: f.ws, ActorID: f.user})
	require.EqualError(t, err, "[404] Task not found", "repository must refuse a focus task of a project the actor is not a member of")

	for name, query := range map[string]string{
		"focus on foreign task":              "?focus=" + secret.String(),
		"focus on foreign task, overview":    "?mode=overview&focus=" + secret.String(),
		"focus on local task linked to it":   "?focus=" + local.String(),
		"overview linked to foreign blocker": "?mode=overview&page=1&page_size=50",
		"legacy full graph":                  "",
	} {
		rec := f.request(t, query, f.user, "member")
		body := rec.Body.String()
		assert.NotContains(t, body, "Leaky-title-7f3a", name)
		assert.NotContains(t, body, "Leaky-description-7f3a", name)
		assert.NotContains(t, body, "Graph reader", name+": assignee name of the foreign task")
		if node := findGraphNode(t, body, secret.String()); node != nil {
			for _, field := range []string{"title", "description", "assignee_name", "assignee_id"} {
				assert.Empty(t, node[field], name+": restricted node must not carry "+field)
			}
		} else if name == "focus on local task linked to it" {
			t.Fatalf("%s: expected the foreign blocker as a restricted node, got %s", name, body)
		}
		if query == "?focus="+secret.String() || query == "?mode=overview&focus="+secret.String() {
			assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, rec.Code, name+": "+body)
		}
	}
}

// findGraphNode returns the JSON object whose id is wanted, anywhere in body.
func findGraphNode(t *testing.T, body, id string) map[string]any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return nil
	}
	var walk func(v any) map[string]any
	walk = func(v any) map[string]any {
		switch x := v.(type) {
		case map[string]any:
			if x["id"] == id {
				return x
			}
			for _, c := range x {
				if n := walk(c); n != nil {
					return n
				}
			}
		case []any:
			for _, c := range x {
				if n := walk(c); n != nil {
					return n
				}
			}
		}
		return nil
	}
	return walk(doc)
}

// A `blocks` edge on a perpetual monitor never clears by status. With a
// condition met by evidence it must stop holding the dependent in every reader:
// the graph's unfinished_blockers, HasOpenBlockedDependents and the stored edge.
// 15 dependents mirror the 24.10 CRM incident; the plain task_done edge and the
// edge with a condition but no evidence stay held.
func TestDependencyGraphDB_ConditionEvidenceReleasesMonitorBlocker(t *testing.T) {
	f := newGraphDBFixture(t, 18)
	ids := f.ids(t)
	monitor := ids[0]
	repo := postgres.NewTaskDependencyRepo(f.db)
	ctx := context.Background()

	released := ids[1:16]
	for _, dep := range released {
		f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,evidence_url,condition_met_at) VALUES($1,$2,$3,'blocks','access_ready','https://mesh.example/evidence',now())`, uuid.New(), dep, monitor)
	}
	plain, noEvidence := ids[16], ids[17]
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), plain, monitor)
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition,evidence_url,condition_met_at) VALUES($1,$2,$3,'blocks','access_ready',NULL,now())`, uuid.New(), noEvidence, monitor)

	s := f.read(t, uuid.Nil)
	for _, dep := range released {
		assert.Equal(t, 0, graphFixtureNode(t, s, dep).UnfinishedBlockers, "evidence-satisfied edge must not hold the dependent")
	}
	assert.Equal(t, 1, graphFixtureNode(t, s, plain).UnfinishedBlockers, "task_done edge keeps its old behaviour")
	assert.Equal(t, 1, graphFixtureNode(t, s, noEvidence).UnfinishedBlockers, "a met condition without evidence_url does not release")

	edges, err := repo.ListByTask(ctx, released[0])
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, domain.DependencyConditionAccessReady, edges[0].Condition)
	assert.True(t, edges[0].ConditionSatisfied)
	assert.True(t, edges[0].EvidenceSatisfied())
	old, err := repo.ListByTask(ctx, plain)
	require.NoError(t, err)
	assert.Equal(t, domain.DependencyConditionTaskDone, old[0].Condition)
	assert.False(t, old[0].ConditionSatisfied)

	// plain and noEvidence still wait on the monitor.
	open, err := repo.HasOpenBlockedDependents(ctx, monitor)
	require.NoError(t, err)
	assert.True(t, open)
	f.exec(t, `DELETE FROM task_dependencies WHERE task_id IN ($1,$2)`, plain, noEvidence)
	open, err = repo.HasOpenBlockedDependents(ctx, monitor)
	require.NoError(t, err)
	assert.False(t, open, "only evidence-satisfied edges remain: nothing waits on the monitor")

	// UpdateCondition marks a plain edge met and the graph follows.
	id := uuid.New()
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, id, plain, monitor)
	url, now := "https://mesh.example/e2", time.Now()
	upd, err := repo.UpdateCondition(ctx, id, domain.DependencyConditionDeployed, &url, &now)
	require.NoError(t, err)
	require.NotNil(t, upd)
	assert.True(t, upd.ConditionSatisfied)
	assert.Equal(t, 0, graphFixtureNode(t, f.read(t, uuid.Nil), plain).UnfinishedBlockers)
}

func TestTaskDependencyDB_UpdateConditionUnknownEdge(t *testing.T) {
	f := newGraphDBFixture(t, 1)
	url := "https://mesh.example/e"
	got, err := postgres.NewTaskDependencyRepo(f.db).UpdateCondition(context.Background(), uuid.New(), domain.DependencyConditionDeployed, &url, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "an unknown edge is (nil, nil), like GetByID")
}
