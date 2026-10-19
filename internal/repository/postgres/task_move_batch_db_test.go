package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// moveFixture adds a destination project (own statuses) to the graph fixture.
type moveFixture struct {
	*completionGraphFixture
	dstProject                uuid.UUID
	srcDone, dstTodo, dstDone uuid.UUID
}

func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	f := newCompletionGraphFixture(t)
	m := &moveFixture{completionGraphFixture: f, dstProject: uuid.New(), srcDone: uuid.New(), dstTodo: uuid.New(), dstDone: uuid.New()}
	_, err := f.db.Exec(`INSERT INTO projects(id,workspace_id,name,slug) VALUES($1,$2,'Marketing',$3)`, m.dstProject, f.workspaceID, m.dstProject.String())
	require.NoError(t, err)
	for _, s := range []struct {
		id, project uuid.UUID
		slug, cat   string
	}{{m.srcDone, f.projectID, "done", "done"}, {m.dstTodo, m.dstProject, "todo", "todo"}, {m.dstDone, m.dstProject, "done", "done"}} {
		_, err = f.db.Exec(`INSERT INTO task_statuses(id,project_id,name,slug,category) VALUES($1,$2,$3,$3,$4)`, s.id, s.project, s.slug, s.cat)
		require.NoError(t, err)
	}
	return m
}

func (m *moveFixture) mapping() map[uuid.UUID]uuid.UUID {
	return map[uuid.UUID]uuid.UUID{m.statusID: m.dstTodo, m.srcDone: m.dstDone}
}

func (m *moveFixture) in(leaf string, ids ...uuid.UUID) domain.MoveBatchInput {
	return domain.MoveBatchInput{WorkspaceID: m.workspaceID, ProjectID: m.dstProject, TaskIDs: ids, StatusMapping: m.mapping(), LeafMode: leaf}
}

func requireStatus(t *testing.T, err error, code int) *apierror.Error {
	t.Helper()
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "want apierror, got %v", err)
	require.Equal(t, code, ae.Code, ae.Error())
	return ae
}

func mustProject(t *testing.T, f *moveFixture, id uuid.UUID) uuid.UUID {
	t.Helper()
	var p uuid.UUID
	require.NoError(t, f.db.Get(&p, `SELECT project_id FROM tasks WHERE id=$1`, id))
	return p
}

func depRows(t *testing.T, f *moveFixture, ids ...uuid.UUID) string {
	t.Helper()
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	var out sql.NullString
	require.NoError(t, f.db.Get(&out, `SELECT string_agg(id||':'||task_id||'>'||depends_on_task_id||':'||dependency_type, ',' ORDER BY id)
		FROM task_dependencies WHERE task_id=ANY($1::uuid[]) OR depends_on_task_id=ANY($1::uuid[])`, pq.Array(strs)))
	return out.String
}

func TestMoveBatch_ParentedLeafKeepsBlocksBothDirections(t *testing.T) {
	f := newMoveFixture(t)
	parent := graphTask(t, f.completionGraphFixture, nil)
	leaf := graphTask(t, f.completionGraphFixture, &parent)
	upstream := graphTask(t, f.completionGraphFixture, nil)
	downstream := graphTask(t, f.completionGraphFixture, nil)
	require.NoError(t, graphEdge(f.completionGraphFixture, leaf, upstream, "blocks"))   // leaf waits on upstream
	require.NoError(t, graphEdge(f.completionGraphFixture, downstream, leaf, "blocks")) // downstream waits on leaf
	require.NoError(t, graphEdge(f.completionGraphFixture, leaf, downstream, "relates_to"))
	// gates, dates, status survive
	_, err := f.db.Exec(`UPDATE tasks SET human_gate=true, due_date=now()+interval '3 day', status_id=$2 WHERE id=$1`, leaf, f.srcDone)
	require.NoError(t, err)
	var gateBefore, createdBefore, dueBefore sql.NullString
	q := `SELECT human_gate::text, created_at::text, due_date::text FROM tasks WHERE id=$1`
	require.NoError(t, f.db.QueryRow(q, leaf).Scan(&gateBefore, &createdBefore, &dueBefore))
	depsBefore := depRows(t, f, leaf)

	// refuse mode: nothing changes
	_, err = NewTaskRepo(f.db).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafRefuse, leaf))
	requireStatus(t, err, 422)
	require.Equal(t, f.projectID, mustProject(t, f, leaf))

	res, err := NewTaskRepo(f.db).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, leaf))
	require.NoError(t, err)
	require.Len(t, res.Detached, 1)
	require.Equal(t, "created", res.Detached[0].BlocksAction)
	require.Equal(t, f.dstProject, mustProject(t, f, leaf))
	require.Equal(t, f.projectID, mustProject(t, f, parent), "parent stays in the product project")
	var pt *uuid.UUID
	require.NoError(t, f.db.Get(&pt, `SELECT parent_task_id FROM tasks WHERE id=$1`, leaf))
	require.Nil(t, pt)
	var st uuid.UUID
	require.NoError(t, f.db.Get(&st, `SELECT status_id FROM tasks WHERE id=$1`, leaf))
	require.Equal(t, f.dstDone, st, "status mapped")
	var gateAfter, createdAfter, dueAfter sql.NullString
	require.NoError(t, f.db.QueryRow(q, leaf).Scan(&gateAfter, &createdAfter, &dueAfter))
	require.Equal(t, []sql.NullString{gateBefore, createdBefore, dueBefore}, []sql.NullString{gateAfter, createdAfter, dueAfter})
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2 AND dependency_type='blocks'`, parent, leaf))
	require.Equal(t, 1, n, "parent now waits on the leaf")
	// every pre-existing dependency row, ID and direction included, is still there
	for _, line := range strings.Split(depsBefore, ",") {
		require.Contains(t, depRows(t, f, leaf), line)
	}
}

// A family moved together must not depend on row order: here the child has the
// lower task_number, so a single multi-row UPDATE would reach it before its parent.
func TestMoveBatch_FamilyMovesTogetherChildLowerNumberThanParent(t *testing.T) {
	f := newMoveFixture(t)
	var children, parents, all []uuid.UUID
	for i := 0; i < 8; i++ {
		children = append(children, graphTask(t, f.completionGraphFixture, nil))
	}
	for i := 0; i < 8; i++ {
		parents = append(parents, graphTask(t, f.completionGraphFixture, nil))
	}
	for i := range children {
		_, err := f.db.Exec(`UPDATE tasks SET parent_task_id=$1 WHERE id=$2`, parents[i], children[i])
		require.NoError(t, err)
	}
	// children first in the input, with the lower task_numbers
	all = append(append(all, children...), parents...)

	res, err := NewTaskRepo(f.db).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafRefuse, all...))
	require.NoError(t, err)
	require.Empty(t, res.Detached)
	for i := range children {
		require.Equal(t, f.dstProject, mustProject(t, f, parents[i]))
		require.Equal(t, f.dstProject, mustProject(t, f, children[i]))
		var pt *uuid.UUID
		require.NoError(t, f.db.Get(&pt, `SELECT parent_task_id FROM tasks WHERE id=$1`, children[i]))
		require.NotNil(t, pt)
		require.Equal(t, parents[i], *pt, "parent link survives the joint move")
	}
}

func TestMoveBatch_AtomicRollbackAndStatusMapping(t *testing.T) {
	f := newMoveFixture(t)
	a, b := graphTask(t, f.completionGraphFixture, nil), graphTask(t, f.completionGraphFixture, nil)
	repo := NewTaskRepo(f.db)
	// one unknown id -> nothing moves
	_, err := repo.MoveBatchToProject(context.Background(), f.in(domain.MoveLeafRefuse, a, uuid.New(), b))
	requireStatus(t, err, 404)
	require.Equal(t, f.projectID, mustProject(t, f, a))
	require.Equal(t, f.projectID, mustProject(t, f, b))
	// unmapped status -> 422 for the whole batch
	in := f.in(domain.MoveLeafRefuse, a, b)
	in.StatusMapping = map[uuid.UUID]uuid.UUID{f.srcDone: f.dstDone}
	_, err = repo.MoveBatchToProject(context.Background(), in)
	requireStatus(t, err, 422)
	// mapping into a status of another project -> 422
	in.StatusMapping = map[uuid.UUID]uuid.UUID{f.statusID: f.statusID}
	_, err = repo.MoveBatchToProject(context.Background(), in)
	requireStatus(t, err, 422)
	require.Equal(t, f.projectID, mustProject(t, f, a))
	// duplicates
	_, err = repo.MoveBatchToProject(context.Background(), f.in(domain.MoveLeafRefuse, a, a))
	requireStatus(t, err, 400)
	// good batch: numbers continue after the existing max, unique
	_, err = f.db.Exec(`INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by,created_by_type) VALUES($1,$2,$3,'x',7,$4,'system')`, uuid.New(), f.dstProject, f.dstTodo, uuid.Nil)
	require.NoError(t, err)
	_, err = repo.MoveBatchToProject(context.Background(), f.in(domain.MoveLeafRefuse, a, b))
	require.NoError(t, err)
	var nums []int64
	require.NoError(t, f.db.Select(&nums, `SELECT task_number FROM tasks WHERE project_id=$1 AND id=ANY($2::uuid[]) ORDER BY task_number`, f.dstProject, pq.Array([]string{a.String(), b.String()})))
	require.Equal(t, []int64{8, 9}, nums)
}

func TestMoveBatch_RootWithRemainingChildrenRefused_ClosedAndDeletedToo(t *testing.T) {
	f := newMoveFixture(t)
	root := graphTask(t, f.completionGraphFixture, nil)
	live := graphTask(t, f.completionGraphFixture, &root)
	closed := graphTask(t, f.completionGraphFixture, &root)
	deleted := graphTask(t, f.completionGraphFixture, &root)
	_, err := f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, closed, f.srcDone)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, deleted)
	require.NoError(t, err)
	repo := NewTaskRepo(f.db)
	_, err = repo.MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, root))
	ae := requireStatus(t, err, 422)
	for _, c := range []uuid.UUID{live, closed, deleted} {
		require.Contains(t, ae.Details, c.String())
	}
	require.Equal(t, f.projectID, mustProject(t, f, root))
	// only the live+closed children listed: root with them moves together, deleted child still blocks
	_, err = repo.MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, root, live, closed))
	ae = requireStatus(t, err, 422)
	require.Contains(t, ae.Details, deleted.String())
	// without the deleted child the whole family moves together and stays a family
	_, err = f.db.Exec(`UPDATE tasks SET parent_task_id=NULL WHERE id=$1`, deleted)
	require.NoError(t, err)
	_, err = repo.MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, root, live, closed))
	require.NoError(t, err)
	var pt *uuid.UUID
	require.NoError(t, f.db.Get(&pt, `SELECT parent_task_id FROM tasks WHERE id=$1`, live))
	require.Equal(t, &root, pt)
	require.Equal(t, f.dstProject, mustProject(t, f, live))
}

func TestMoveBatch_ActiveCheckoutRefusedWithoutPartialChange(t *testing.T) {
	f := newMoveFixture(t)
	a, b := graphTask(t, f.completionGraphFixture, nil), graphTask(t, f.completionGraphFixture, nil)
	agent := uuid.New()
	_, err := f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'mover',$3,'x','t')`, agent, f.workspaceID, agent.String())
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE tasks SET checked_out_by=$2, checkout_token=$3, checkout_expires=now()+interval '1 hour' WHERE id=$1`, b, agent, uuid.New())
	require.NoError(t, err)
	_, err = NewTaskRepo(f.db).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafRefuse, a, b))
	ae := requireStatus(t, err, 409)
	require.Contains(t, ae.Message, b.String())
	require.Equal(t, f.projectID, mustProject(t, f, a), "a must not have moved")
}

func TestMoveBatch_CycleRejectedAndRolledBack(t *testing.T) {
	f := newMoveFixture(t)
	parent := graphTask(t, f.completionGraphFixture, nil)
	leaf := graphTask(t, f.completionGraphFixture, &parent)
	// A historical cycle (leaf waits on parent while parent waits on leaf
	// through the hierarchy) can only exist from before the guard.
	_, err := f.db.Exec(`ALTER TABLE task_dependencies DISABLE TRIGGER completion_graph_dependencies`)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), leaf, parent)
	_, _ = f.db.Exec(`ALTER TABLE task_dependencies ENABLE TRIGGER completion_graph_dependencies`)
	require.NoError(t, err)
	_, err = NewTaskRepo(f.db).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, leaf))
	requireStatus(t, err, 422)
	require.Equal(t, f.projectID, mustProject(t, f, leaf))
	var pt *uuid.UUID
	require.NoError(t, f.db.Get(&pt, `SELECT parent_task_id FROM tasks WHERE id=$1`, leaf))
	require.Equal(t, &parent, pt, "detach rolled back with the rest")
}

func TestMoveBatch_DryRunChangesNothingAndForeignWorkspaceRefused(t *testing.T) {
	f := newMoveFixture(t)
	a := graphTask(t, f.completionGraphFixture, nil)
	repo := NewTaskRepo(f.db)
	in := f.in(domain.MoveLeafRefuse, a)
	in.DryRun = true
	res, err := repo.MoveBatchToProject(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, f.dstProject, res.After[0].ProjectID)
	require.Equal(t, f.projectID, res.Before[0].ProjectID)
	require.Equal(t, f.projectID, mustProject(t, f, a), "dry run rolled back")
	// destination in another workspace / caller workspace mismatch
	in = f.in(domain.MoveLeafRefuse, a)
	in.WorkspaceID = uuid.New()
	_, err = repo.MoveBatchToProject(context.Background(), in)
	requireStatus(t, err, 403)
}

func TestMoveBatch_FlagIsScopedAndGuardStillRefusesWithoutIt(t *testing.T) {
	f := newMoveFixture(t)
	parent := graphTask(t, f.completionGraphFixture, nil)
	child := graphTask(t, f.completionGraphFixture, &parent)
	refused := func(err error) {
		t.Helper()
		var pqErr *pq.Error
		require.ErrorAs(t, err, &pqErr)
		require.EqualValues(t, "23514", pqErr.Code)
	}
	move := func(conn execer, id uuid.UUID) error {
		_, err := conn.Exec(`UPDATE tasks SET project_id=$2 WHERE id=$1`, id, f.dstProject)
		return err
	}
	// red run: a plain UPDATE of a connected task keeps failing with 23514
	refused(move(f.db, parent))

	// a session-scoped SET with the old constant never opens the guard
	conn, err := f.db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(context.Background(), `SET mesh.atomic_move='on'`)
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), `UPDATE tasks SET project_id=$2 WHERE id=$1`, parent, f.dstProject)
	refused(err)

	// a session-scoped SET carrying a real txid is stale in the next transaction
	var stale string
	require.NoError(t, conn.QueryRowContext(context.Background(), `SELECT txid_current()::text`).Scan(&stale))
	_, err = conn.ExecContext(context.Background(), `SELECT set_config('mesh.atomic_move', $1, false)`, stale)
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), `UPDATE tasks SET project_id=$2 WHERE id=$1`, parent, f.dstProject)
	refused(err)
	_, err = conn.ExecContext(context.Background(), `RESET mesh.atomic_move`)
	require.NoError(t, err)

	// the flag set in a finished transaction does not leak to the next one
	tx, err := f.db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`SELECT set_config('mesh.atomic_move', txid_current()::text, true)`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	refused(move(f.db, parent))

	// with the flag on, the other invariants still hold: a child cannot leave its parent behind
	tx, err = f.db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(`SELECT set_config('mesh.atomic_move', txid_current()::text, true)`)
	require.NoError(t, err)
	refused(move(tx, child))
	_ = tx.Rollback()
	require.Equal(t, f.projectID, mustProject(t, f, child))
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func TestMoveBatch_MigrationDowngradeRemovesFlag(t *testing.T) {
	f := newMoveFixture(t)
	parent := graphTask(t, f.completionGraphFixture, nil)
	raw, err := os.ReadFile("../../../migrations/20261017004_atomic_project_move_flag.sql")
	require.NoError(t, err)
	idx := strings.Index(string(raw), "-- +goose Down")
	require.Positive(t, idx)
	down := string(raw)[idx:]
	down = strings.NewReplacer("-- +goose StatementBegin", "", "-- +goose StatementEnd", "", "-- +goose Down", "").Replace(down)
	tx, err := f.db.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(down)
	require.NoError(t, err)
	_, err = tx.Exec(`SELECT set_config('mesh.atomic_move', txid_current()::text, true)`)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE tasks SET project_id=$2 WHERE id=$1`, parent, f.dstProject)
	require.NoError(t, err, "unconnected task still moves after downgrade")
	child := uuid.New()
	_, err = tx.Exec(`INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by,created_by_type,parent_task_id) VALUES($1,$2,$3,'c',99,$4,'system',$5)`, child, f.dstProject, f.dstTodo, uuid.Nil, parent)
	require.NoError(t, err)
	_, err = tx.Exec(`SAVEPOINT s`)
	require.NoError(t, err)
	_, err = tx.Exec(`UPDATE tasks SET project_id=$2 WHERE id=$1`, parent, f.projectID)
	var pqErr *pq.Error
	require.ErrorAs(t, err, &pqErr, "downgraded guard ignores the flag")
	require.EqualValues(t, "23514", pqErr.Code)
}

func TestMoveBatch_InputRefusals(t *testing.T) {
	f := newMoveFixture(t)
	repo := NewTaskRepo(f.db)
	ctx := context.Background()
	a := graphTask(t, f.completionGraphFixture, nil)

	_, err := repo.MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse))
	requireStatus(t, err, 400)

	in := f.in(domain.MoveLeafRefuse, a)
	in.ProjectID = uuid.New()
	_, err = repo.MoveBatchToProject(ctx, in)
	requireStatus(t, err, 404)

	_, err = f.db.Exec(`UPDATE projects SET deleted_at=now() WHERE id=$1`, f.dstProject)
	require.NoError(t, err)
	_, err = repo.MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse, a))
	requireStatus(t, err, 404) // a soft-deleted destination is invisible to normal APIs
	_, err = f.db.Exec(`UPDATE projects SET deleted_at=NULL WHERE id=$1`, f.dstProject)
	require.NoError(t, err)

	other := newCompletionGraphFixture(t)
	foreign := graphTask(t, other, nil)
	_, err = repo.MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse, a, foreign))
	requireStatus(t, err, 403)
	require.Equal(t, f.projectID, mustProject(t, f, a))

	_, err = f.db.Exec(`UPDATE tasks SET project_id=$2, status_id=$3 WHERE id=$1`, a, f.dstProject, f.dstTodo)
	require.NoError(t, err)
	_, err = repo.MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse, a))
	requireStatus(t, err, 422)
}

func TestMoveBatch_LeafParentLinkVariants(t *testing.T) {
	repo := func(f *moveFixture) *TaskRepo { return NewTaskRepo(f.db) }
	cases := map[string]struct {
		prepare func(t *testing.T, f *moveFixture, parent, leaf uuid.UUID)
		action  string
	}{
		"blocks edge already present": {func(t *testing.T, f *moveFixture, parent, leaf uuid.UUID) {
			require.NoError(t, graphEdge(f.completionGraphFixture, parent, leaf, "blocks"))
		}, "already_present"},
		"deleted parent needs no blocks edge": {func(t *testing.T, f *moveFixture, parent, leaf uuid.UUID) {
			_, err := f.db.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, parent)
			require.NoError(t, err)
		}, "skipped_parent_deleted"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newMoveFixture(t)
			parent := graphTask(t, f.completionGraphFixture, nil)
			leaf := graphTask(t, f.completionGraphFixture, &parent)
			tc.prepare(t, f, parent, leaf)
			res, err := repo(f).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, leaf))
			require.NoError(t, err)
			require.Len(t, res.Detached, 1)
			require.Equal(t, tc.action, res.Detached[0].BlocksAction)
			require.Equal(t, f.dstProject, mustProject(t, f, leaf))
		})
	}
}

func TestMoveBatch_ActiveRuntimeReservationRefused(t *testing.T) {
	f := newMoveFixture(t)
	a, b := graphTask(t, f.completionGraphFixture, nil), graphTask(t, f.completionGraphFixture, nil)
	ctx := context.Background()
	// Only the task link matters here; skip the FK chain to integrations and agents.
	conn, err := f.db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = conn.ExecContext(ctx, `SET session_replication_role = replica`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO runtime_reservations(id,fence,integration_id,binding_ref,agent_id,workspace_id,grant_id,
		grant_fingerprint,idempotency_key,scope_digest,task_id,checkout_generation,controller_ref,worker_ref,profile_ref,
		catalog_revision,catalog_digest,admission_revision,profile_revision,pools,pool_set_digest,state,expires_at)
		VALUES($1,1,$2,'b',$2,$3,$2,'g','k','s',$4,1,'c','w','p',1,'d',1,'r','[]','p','reserved',now()+interval '1 hour')`,
		uuid.New(), uuid.New(), f.workspaceID, b)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `SET session_replication_role = DEFAULT`)
	require.NoError(t, err)

	_, err = NewTaskRepo(f.db).MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse, a, b))
	ae := requireStatus(t, err, 409)
	require.Contains(t, ae.Message, b.String())
	require.Equal(t, f.projectID, mustProject(t, f, a), "a must not have moved")
}

// Real cards carry the hierarchy as an is_child_of dependency row (the trigger
// mirrors it into parent_task_id); the batch must remove exactly that row.
func TestMoveBatch_LeafWithIsChildOfRowDetachesAndDropsOnlyThatRow(t *testing.T) {
	f := newMoveFixture(t)
	parent := graphTask(t, f.completionGraphFixture, nil)
	leaf := graphTask(t, f.completionGraphFixture, nil)
	require.NoError(t, graphEdge(f.completionGraphFixture, leaf, parent, "is_child_of"))
	var pt *uuid.UUID
	require.NoError(t, f.db.Get(&pt, `SELECT parent_task_id FROM tasks WHERE id=$1`, leaf))
	require.NotNil(t, pt, "the is_child_of row must set the parent")

	res, err := NewTaskRepo(f.db).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, leaf))
	require.NoError(t, err)
	require.Len(t, res.Detached, 1)
	require.Equal(t, "created", res.Detached[0].BlocksAction)
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2 AND dependency_type='is_child_of'`, leaf, parent))
	require.Zero(t, n, "the hierarchy row is gone")
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2 AND dependency_type='blocks'`, parent, leaf))
	require.Equal(t, 1, n, "the parent now waits on the leaf")
	require.NoError(t, f.db.Get(&pt, `SELECT parent_task_id FROM tasks WHERE id=$1`, leaf))
	require.Nil(t, pt)
}

// A project deletion that commits between the destination check and the task
// UPDATE must not leave live tasks in a hidden project: the check holds a share
// lock, so the deletion waits for the move, or the move sees the deletion.
func TestMoveBatch_DestinationDeletionIsSerialisedWithMove(t *testing.T) {
	f := newMoveFixture(t)
	a := graphTask(t, f.completionGraphFixture, nil)
	ctx := context.Background()

	// the deleter holds the project row first, then commits the soft delete
	del, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = del.ExecContext(ctx, `SELECT 1 FROM projects WHERE id=$1 FOR UPDATE`, f.dstProject)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, merr := NewTaskRepo(f.db).MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse, a))
		done <- merr
	}()
	select {
	case merr := <-done:
		t.Fatalf("the move must wait for the deleting transaction, returned %v", merr)
	case <-time.After(500 * time.Millisecond):
	}
	_, err = del.ExecContext(ctx, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.dstProject)
	require.NoError(t, err)
	require.NoError(t, del.Commit())

	requireStatus(t, <-done, 404)
	require.Equal(t, f.projectID, mustProject(t, f, a), "nothing moved into the deleted project")
}

// Contention on the locks the move takes surfaces as an error and changes nothing.
func TestMoveBatch_LockContentionFailsWithoutPartialChange(t *testing.T) {
	cases := map[string]func(h *sql.Tx, f *moveFixture, task uuid.UUID) error{
		"project number lock": func(h *sql.Tx, f *moveFixture, _ uuid.UUID) error {
			_, err := h.Exec(`SELECT pg_advisory_xact_lock(hashtext($1::text))`, f.dstProject.String())
			return err
		},
		"task rows": func(h *sql.Tx, _ *moveFixture, task uuid.UUID) error {
			_, err := h.Exec(`SELECT 1 FROM tasks WHERE id=$1 FOR UPDATE`, task)
			return err
		},
	}
	for name, hold := range cases {
		t.Run(name, func(t *testing.T) {
			f := newMoveFixture(t)
			a := graphTask(t, f.completionGraphFixture, nil)
			holder, err := f.db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			defer func() { _ = holder.Rollback() }()
			require.NoError(t, hold(holder, f, a))

			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			defer cancel()
			_, err = NewTaskRepo(f.db).MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse, a))
			require.Error(t, err)
			require.Equal(t, f.projectID, mustProject(t, f, a))
		})
	}
}

func TestMoveBatch_CancelledContextFailsCleanly(t *testing.T) {
	f := newMoveFixture(t)
	a := graphTask(t, f.completionGraphFixture, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewTaskRepo(f.db).MoveBatchToProject(ctx, f.in(domain.MoveLeafRefuse, a))
	require.Error(t, err)
	require.Equal(t, f.projectID, mustProject(t, f, a))
}

// An existing relationship other than blocks between the staying parent and the
// leaf is never rewritten into blocks: the move is refused and nothing changes.
func TestMoveBatch_ExistingNonBlocksDependencyIsNotRewritten(t *testing.T) {
	f := newMoveFixture(t)
	parent := graphTask(t, f.completionGraphFixture, nil)
	leaf := graphTask(t, f.completionGraphFixture, &parent)
	require.NoError(t, graphEdge(f.completionGraphFixture, parent, leaf, "relates_to"))
	before := depRows(t, f, parent, leaf)

	_, err := NewTaskRepo(f.db).MoveBatchToProject(context.Background(), f.in(domain.MoveLeafDetachKeepBlocks, leaf))
	ae := requireStatus(t, err, 422)
	require.Contains(t, ae.Message, "relates_to")
	require.Equal(t, f.projectID, mustProject(t, f, leaf))
	require.Equal(t, before, depRows(t, f, parent, leaf), "dependency rows untouched")
}
