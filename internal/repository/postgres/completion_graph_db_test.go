package postgres

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// These real PostgreSQL controls also run in the default coverage job. Without
// an explicitly configured database, local unit-only runs skip them.
type completionGraphFixture struct {
	db                               *sqlx.DB
	dsn                              string
	workspaceID, projectID, statusID uuid.UUID
}

func newCompletionGraphFixture(t *testing.T) *completionGraphFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for completion graph controls")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: "Completion graph", Slug: "graph-" + uuid.NewString(), OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))
	t.Cleanup(func() { _, err := db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws.ID); require.NoError(t, err) })
	p := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Graph", Slug: "graph-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, NewProjectRepo(db).Create(ctx, p))
	status := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Color: "#000000"}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))
	return &completionGraphFixture{db: db, dsn: dsn, workspaceID: ws.ID, projectID: p.ID, statusID: status.ID}
}

func (f *completionGraphFixture) newTask() *domain.Task {
	return &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID, Title: "Graph fixture", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

func graphTask(t *testing.T, f *completionGraphFixture, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	task := f.newTask()
	task.ParentTaskID = parent
	require.NoError(t, NewTaskRepo(f.db).Create(context.Background(), task, nil))
	var stored *uuid.UUID
	require.NoError(t, f.db.Get(&stored, `SELECT parent_task_id FROM tasks WHERE id=$1`, task.ID))
	require.Equal(t, parent, stored, "fixture hierarchy must actually be persisted")
	return task.ID
}

func graphEdge(f *completionGraphFixture, from, to uuid.UUID, kind string) error {
	return NewTaskDependencyRepo(f.db).Create(context.Background(), &domain.TaskDependency{ID: uuid.New(), TaskID: from, DependsOnTaskID: to, DependencyType: domain.DependencyType(kind), CreatedAt: time.Now()})
}

func TestCompletionGraphDirectMixedAndReverse(t *testing.T) {
	f := newCompletionGraphFixture(t)
	p := graphTask(t, f, nil)
	c := graphTask(t, f, &p)
	s := graphTask(t, f, nil)
	require.Error(t, graphEdge(f, c, p, "blocks"), "child waiting on parent is a completion cycle")
	require.NoError(t, graphEdge(f, p, c, "blocks"), "parent waiting on child is valid")
	require.NoError(t, graphEdge(f, c, s, "blocks"))
	require.Error(t, graphEdge(f, s, p, "blocks"), "transitive mixed cycle")
	require.NoError(t, graphEdge(f, c, p, "relates_to"), "informational edge does not block")
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2 AND dependency_type='blocks'`, c, p))
	require.Zero(t, n)
	a := graphTask(t, f, nil)
	b := graphTask(t, f, nil)
	require.NoError(t, graphEdge(f, b, a, "blocks"))
	_, err := f.db.Exec(`UPDATE tasks SET parent_task_id=$1 WHERE id=$2`, a, b)
	require.Error(t, err, "reverse operation order must also be rejected")
	var parent *uuid.UUID
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, b))
	require.Nil(t, parent)
}

func TestCompletionGraphHierarchyAndTypeChange(t *testing.T) {
	f := newCompletionGraphFixture(t)
	p := graphTask(t, f, nil)
	c := graphTask(t, f, &p)
	require.NoError(t, graphEdge(f, c, p, "relates_to"))
	_, err := f.db.Exec(`UPDATE task_dependencies SET dependency_type='blocks' WHERE task_id=$1 AND depends_on_task_id=$2`, c, p)
	require.Error(t, err)
	var kind string
	require.NoError(t, f.db.Get(&kind, `SELECT dependency_type FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, c, p))
	require.Equal(t, "relates_to", kind)
	_, err = f.db.Exec(`UPDATE tasks SET parent_task_id=$1 WHERE id=$2`, c, p)
	require.Error(t, err, "pure hierarchy cycle")
	task := f.newTask()
	task.ParentTaskID = &task.ID
	require.Error(t, NewTaskRepo(f.db).Create(context.Background(), task, nil), "self child create")
	a := graphTask(t, f, nil)
	b := graphTask(t, f, nil)
	d := graphTask(t, f, nil)
	require.NoError(t, graphEdge(f, a, b, "blocks"))
	require.NoError(t, graphEdge(f, b, d, "blocks"))
	require.Error(t, graphEdge(f, a, d, "is_child_of"), "parent side effect must participate in mixed graph")
	var count int
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, a, d))
	require.Zero(t, count, "no partial edge")
	var parent *uuid.UUID
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, a))
	require.Nil(t, parent)
	x := graphTask(t, f, nil)
	y := graphTask(t, f, nil)
	require.NoError(t, graphEdge(f, x, y, "is_child_of"))
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Equal(t, &y, parent, "edge and parent written together by repository")
}

func TestCompletionGraphConcurrentEndpoints(t *testing.T) {
	f := newCompletionGraphFixture(t)
	for _, kind := range []string{"insert", "type-change"} {
		t.Run(kind, func(t *testing.T) {
			for iteration := 0; iteration < 10; iteration++ {
				p := graphTask(t, f, nil)
				c := graphTask(t, f, nil)
				task, err := NewTaskRepo(f.db).GetByID(context.Background(), c)
				require.NoError(t, err)
				task.ParentTaskID = &p
				if kind == "type-change" {
					require.NoError(t, graphEdge(f, c, p, "relates_to"))
				}
				start := make(chan struct{})
				results := make(chan error, 2)
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					if kind == "insert" {
						results <- graphEdge(f, c, p, "blocks")
					} else {
						_, err := f.db.Exec(`UPDATE task_dependencies SET dependency_type='blocks' WHERE task_id=$1 AND depends_on_task_id=$2`, c, p)
						results <- err
					}
				}()
				go func() {
					defer wg.Done()
					<-start
					if kind == "insert" {
						results <- NewTaskRepo(f.db).Update(context.Background(), task)
						return
					}
					_, err := f.db.Exec(`UPDATE tasks SET parent_task_id=$1 WHERE id=$2`, p, c)
					results <- err
				}()
				close(start)
				wg.Wait()
				close(results)
				successes := 0
				for err := range results {
					if err == nil {
						successes++
					}
				}
				require.Equal(t, 1, successes, "competing cycle-closing writes cannot both commit")
				var cyclic bool
				require.NoError(t, f.db.Get(&cyclic, `SELECT EXISTS(SELECT 1 FROM tasks c JOIN task_dependencies d ON d.task_id=c.id AND d.depends_on_task_id=c.parent_task_id AND d.dependency_type='blocks' WHERE c.id=$1)`, c))
				require.False(t, cyclic, "SELECT proves stored final graph, not goroutine return values")
			}
		})
	}
}

func TestCompletionGraphAtomicParentAndConversions(t *testing.T) {
	f := newCompletionGraphFixture(t)
	ctx := context.Background()
	x := graphTask(t, f, nil)
	y := graphTask(t, f, nil)
	// Raw INSERT also applies the parent in the same transaction. This closes
	// the older service's partial-write gap; its pre-insert CAS was already
	// invalidated by the existing dependency snapshot trigger.
	task, err := NewTaskRepo(f.db).GetByID(ctx, x)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'is_child_of')`, uuid.New(), x, y)
	require.NoError(t, err)
	var version int64
	require.NoError(t, f.db.Get(&version, `SELECT version FROM tasks WHERE id=$1`, x))
	require.Equal(t, task.Version+2, version, "parent and dependency snapshot each invalidate the task version")
	var parent *uuid.UUID
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Equal(t, &y, parent)
	_, err = f.db.Exec(`UPDATE task_dependencies SET dependency_type='blocks' WHERE task_id=$1 AND depends_on_task_id=$2`, x, y)
	require.NoError(t, err, "replacing hierarchy with blocks removes the old parent atomically")
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Nil(t, parent)
	_, err = f.db.Exec(`UPDATE task_dependencies SET dependency_type='is_child_of' WHERE task_id=$1 AND depends_on_task_id=$2`, x, y)
	require.NoError(t, err, "new type must be visible while applying parent")
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Equal(t, &y, parent)
	var depID uuid.UUID
	require.NoError(t, f.db.Get(&depID, `SELECT id FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, x, y))
	require.NoError(t, NewTaskDependencyRepo(f.db).Delete(ctx, depID))
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Nil(t, parent)

	// Force the second write to fail, proving the first does not survive it.
	name := "graph_parent_failure_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = f.db.Exec( // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL safely quotes identifier and UUID literal with lib/pq.
		`CREATE FUNCTION ` + pq.QuoteIdentifier(name) + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id=` + pq.QuoteLiteral(x.String()) + `::uuid THEN RAISE EXCEPTION 'injected parent failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER ` + pq.QuoteIdentifier(name) + ` BEFORE UPDATE OF parent_task_id ON tasks FOR EACH ROW EXECUTE FUNCTION ` + pq.QuoteIdentifier(name) + `()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := f.db.Exec(`DROP TRIGGER ` + pq.QuoteIdentifier(name) + ` ON tasks; DROP FUNCTION ` + pq.QuoteIdentifier(name) + `()`) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL quotes generated identifier with lib/pq.
		require.NoError(t, err)
	})
	require.ErrorContains(t, graphEdge(f, x, y, "is_child_of"), "injected parent failure")
	var count int
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, x, y))
	require.Zero(t, count, "failed parent side effect rolls back dependency INSERT")
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Nil(t, parent)
}

func TestCompletionGraphReadFailureClosed(t *testing.T) {
	f := newCompletionGraphFixture(t)
	p := graphTask(t, f, nil)
	c := graphTask(t, f, nil)
	role := "graph_reader_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// Disposable role authentication must work with both trust and SCRAM CI databases.
	rolePassword := uuid.NewString()
	// This role can write dependencies and read task scope, but cannot read the
	// existing dependency graph. A failed graph read must abort the INSERT.
	_, err := f.db.Exec(`CREATE ROLE ` + pq.QuoteIdentifier(role) + ` LOGIN PASSWORD ` + pq.QuoteLiteral(rolePassword) + `; GRANT USAGE ON SCHEMA public TO ` + pq.QuoteIdentifier(role) + `; GRANT SELECT ON tasks,projects TO ` + pq.QuoteIdentifier(role) + `; GRANT INSERT ON task_dependencies TO ` + pq.QuoteIdentifier(role)) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL quotes role identifiers and password literal with lib/pq.
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := f.db.Exec(`DROP OWNED BY ` + pq.QuoteIdentifier(role) + `; DROP ROLE ` + pq.QuoteIdentifier(role)) // nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL quotes generated role identifier with lib/pq.
		require.NoError(t, cleanupErr)
	})
	dsn, err := url.Parse(f.dsn)
	require.NoError(t, err)
	dsn.User = url.UserPassword(role, rolePassword)
	options := dsn.Query()
	options.Set("options", "-c app.current_workspace_id="+f.workspaceID.String())
	dsn.RawQuery = options.Encode()
	db, err := sqlx.Connect("postgres", dsn.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	dep := &domain.TaskDependency{ID: uuid.New(), TaskID: c, DependsOnTaskID: p, DependencyType: domain.DependencyTypeBlocks, CreatedAt: time.Now()}
	require.ErrorContains(t, NewTaskDependencyRepo(db).Create(context.Background(), dep), "permission denied for table task_dependencies")
	var count int
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM task_dependencies WHERE id=$1`, dep.ID))
	require.Zero(t, count)
	require.NoError(t, NewTaskDependencyRepo(f.db).Create(context.Background(), dep), "same candidate succeeds when graph can be read")
}

func TestCompletionGraphReparentUsesActualHierarchy(t *testing.T) {
	f := newCompletionGraphFixture(t)
	p := graphTask(t, f, nil)
	q := graphTask(t, f, nil)
	c := graphTask(t, f, nil)
	require.NoError(t, graphEdge(f, c, p, "is_child_of"))
	task, err := NewTaskRepo(f.db).GetByID(context.Background(), c)
	require.NoError(t, err)
	task.ParentTaskID = &q
	require.NoError(t, NewTaskRepo(f.db).Update(context.Background(), task))
	var parent *uuid.UUID
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, c))
	require.Equal(t, &q, parent)
	r := graphTask(t, f, nil)
	require.NoError(t, graphEdge(f, c, r, "blocks"))
	require.NoError(t, graphEdge(f, r, p, "blocks"), "stale is_child_of row is not a second completion parent")
}
