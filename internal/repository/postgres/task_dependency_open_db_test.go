package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

// Untagged on purpose: it runs in the `test` job (DATABASE_URL, skipped when no
// Postgres is reachable) so the coverage gate sees HasOpenBlockedDependents,
// which only real SQL can prove. The reaper reads it to keep a card that open
// work waits on out of backlog (#53f30091 → #14580276, 11.10).

type depFixture struct {
	db         *sqlx.DB
	project    uuid.UUID
	inProgress uuid.UUID
}

func newDepFixture(t *testing.T) *depFixture {
	t.Helper()
	db := agentDigestTestDB(t)
	ctx := context.Background()

	ws := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, created_at, updated_at)
		 VALUES ($1, 'dep-open', $2, $3, now(), now())`,
		ws, "dep-open-"+ws.String()[:8], uuid.New())
	require.NoError(t, err)

	proj := uuid.New()
	_, err = db.ExecContext(ctx,
		`INSERT INTO projects (id, workspace_id, name, slug, created_at, updated_at)
		 VALUES ($1, $2, 'dep-open', $3, now(), now())`,
		proj, ws, "dep-open-"+proj.String()[:8])
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `CREATE SEQUENCE IF NOT EXISTS dep_open_task_number`)
	require.NoError(t, err)

	f := &depFixture{db: db, project: proj}
	f.inProgress = f.insertStatus(t, "in_progress")
	return f
}

func (f *depFixture) insertStatus(t *testing.T, category string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO task_statuses (id, project_id, name, slug, category, position)
		 VALUES ($1, $2, $3, $3, $4::task_status_category, 9)`,
		id, f.project, category+"-"+id.String()[:8], category)
	require.NoError(t, err)
	return id
}

func (f *depFixture) insertTaskIn(t *testing.T, statusID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by, position, created_at, updated_at)
		 VALUES ($1, $2, $3, 'dependent', nextval('dep_open_task_number'), $4, 1, now(), now())`,
		id, f.project, statusID, uuid.New())
	require.NoError(t, err)
	return id
}

// dependent depends on blocker, i.e. blocker blocks dependent.
func (f *depFixture) insertDependency(t *testing.T, dependent, blocker uuid.UUID, depType string) {
	t.Helper()
	_, err := f.db.ExecContext(context.Background(),
		`INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type)
		 VALUES ($1, $2, $3, $4::dependency_type)`,
		uuid.New(), dependent, blocker, depType)
	require.NoError(t, err)
}

func TestTaskDependencyRepo_HasOpenBlockedDependents(t *testing.T) {
	f := newDepFixture(t)
	repo := NewTaskDependencyRepo(f.db)
	ctx := context.Background()

	todo := f.insertStatus(t, "todo")
	done := f.insertStatus(t, "done")
	cancelled := f.insertStatus(t, "cancelled")

	// 53f30091 shape: the stale blocker has an open dependent that waits on it.
	openBlocker := f.insertTaskIn(t, f.inProgress)
	f.insertDependency(t, f.insertTaskIn(t, todo), openBlocker, "blocks")
	// an in_progress dependent is open too
	ipBlocker := f.insertTaskIn(t, f.inProgress)
	f.insertDependency(t, f.insertTaskIn(t, f.inProgress), ipBlocker, "blocks")

	// every dependent closed: nothing waits on the card any more
	closedBlocker := f.insertTaskIn(t, f.inProgress)
	f.insertDependency(t, f.insertTaskIn(t, done), closedBlocker, "blocks")
	f.insertDependency(t, f.insertTaskIn(t, cancelled), closedBlocker, "blocks")

	// a non-blocking edge does not hold anything up
	relatesOnly := f.insertTaskIn(t, f.inProgress)
	f.insertDependency(t, f.insertTaskIn(t, todo), relatesOnly, "relates_to")

	// the edge in the other direction: the card itself waits on an open task
	dependentOnly := f.insertTaskIn(t, f.inProgress)
	f.insertDependency(t, dependentOnly, f.insertTaskIn(t, todo), "blocks")

	// a soft-deleted dependent is not open work
	deletedDep := f.insertTaskIn(t, f.inProgress)
	gone := f.insertTaskIn(t, todo)
	f.insertDependency(t, gone, deletedDep, "blocks")
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET deleted_at = now() WHERE id = $1`, gone)
	require.NoError(t, err)

	// a dependent waiting in backlog/triage is exactly what tryUnblockTask
	// promotes when the blocker finishes: it counts as open
	backlog := f.insertStatus(t, "backlog")
	backlogBlocker := f.insertTaskIn(t, f.inProgress)
	f.insertDependency(t, f.insertTaskIn(t, backlog), backlogBlocker, "blocks")

	// one closed and one open dependent on the same blocker: open wins
	mixedBlocker := f.insertTaskIn(t, f.inProgress)
	f.insertDependency(t, f.insertTaskIn(t, done), mixedBlocker, "blocks")
	f.insertDependency(t, f.insertTaskIn(t, todo), mixedBlocker, "blocks")

	// a legacy cross-workspace edge is ignored. The completion-graph trigger now
	// refuses such a row, so it is written with triggers off, inside one
	// transaction so SET LOCAL and the INSERT share a connection.
	other := newDepFixture(t)
	otherTodo := other.insertStatus(t, "todo")
	crossBlocker := f.insertTaskIn(t, f.inProgress)
	foreign := other.insertTaskIn(t, otherTodo)
	tx, err := f.db.BeginTxx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO task_dependencies (id, task_id, depends_on_task_id, dependency_type)
		 VALUES ($1, $2, $3, 'blocks')`, uuid.New(), foreign, crossBlocker)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	noDeps := f.insertTaskIn(t, f.inProgress)

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		want bool
	}{
		{"open todo dependent", openBlocker, true},
		{"open in_progress dependent", ipBlocker, true},
		{"only done/cancelled dependents", closedBlocker, false},
		{"relates_to edge only", relatesOnly, false},
		{"card is the dependent, not the blocker", dependentOnly, false},
		{"dependent soft-deleted", deletedDep, false},
		{"backlog dependent", backlogBlocker, true},
		{"one closed and one open dependent", mixedBlocker, true},
		{"cross-workspace legacy edge", crossBlocker, false},
		{"no dependencies", noDeps, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.HasOpenBlockedDependents(ctx, tc.id)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
