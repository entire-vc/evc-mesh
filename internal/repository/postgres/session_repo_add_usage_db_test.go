package postgres

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// AddUsage against real Postgres (no build tag, like the other *_db_test.go
// files; skipped when no database is reachable). It proves what a mock cannot:
// the sum is taken by the database on the current row, task-scoped and
// agent-wide usage land on separate rows, concurrent first reports share one
// session, and a database failure surfaces instead of being swallowed.

type usageFixture struct {
	db      *sqlx.DB
	repo    *SessionRepo
	wsID    uuid.UUID
	agentID uuid.UUID
}

func newUsageFixture(t *testing.T) *usageFixture {
	t.Helper()
	db := agentDigestTestDB(t)
	ctx := context.Background()
	wsID, agentID := uuid.New(), uuid.New()
	sfx := wsID.String()[:8]
	_, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1,'usage',$2,$3)`, wsID, "usage-ws-"+sfx, uuid.New())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		VALUES ($1,$2,'Usage Agent',$3,'not-a-real-hash','test')`, agentID, wsID, "usage-agent-"+sfx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agent_sessions WHERE agent_id=$1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM agents WHERE id=$1`, agentID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id=$1`, wsID)
	})
	return &usageFixture{db: db, repo: NewSessionRepo(db), wsID: wsID, agentID: agentID}
}

func (f *usageFixture) sessions(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM agent_sessions WHERE agent_id=$1`, f.agentID))
	return n
}

func TestSessionRepo_AddUsage_CreatesThenAdds_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()

	first, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, nil, 100, 10, 0.5, "model-a")
	require.NoError(t, err)
	assert.EqualValues(t, 100, first.TokensIn, "no active session: one is created, seeded with the report")
	assert.Equal(t, f.wsID, first.WorkspaceID)
	assert.Nil(t, first.TaskID)

	second, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, nil, 30, 2, 0.25, "")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "the second report lands on the same session")
	assert.EqualValues(t, 130, second.TokensIn)
	assert.EqualValues(t, 12, second.TokensOut)
	assert.InDelta(t, 0.75, second.EstimatedCost, 1e-9)
	assert.Equal(t, "model-a", second.ModelUsed, "an empty model does not clobber the stored one")

	third, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, nil, 0, 0, 0, "model-b")
	require.NoError(t, err)
	assert.Equal(t, "model-b", third.ModelUsed, "the latest non-empty model wins")
	assert.Equal(t, 1, f.sessions(t))
}

// seedTask creates a real task (agent_sessions.task_id references tasks).
func (f *usageFixture) seedTask(t *testing.T) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	proj, st, task := uuid.New(), uuid.New(), uuid.New()
	sfx := task.String()[:8]
	_, err := f.db.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings) VALUES ($1,$2,'usage','usage-'||$3,'none','{}')`, proj, f.wsID, sfx)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `INSERT INTO task_statuses (id, project_id, name, slug, color, position, category, is_default, auto_transition)
		VALUES ($1,$2,'Open','open','#00FF00',0,'todo',true,'{}')`, st, proj)
	require.NoError(t, err)
	require.NoError(t, NewTaskRepo(f.db).Create(ctx, &domain.Task{
		ID: task, ProjectID: proj, StatusID: st, Title: "usage task",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CustomFields: json.RawMessage(`{}`), CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil))
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), `DELETE FROM agent_sessions WHERE task_id=$1`, task)
		_, _ = f.db.ExecContext(context.Background(), `DELETE FROM tasks WHERE id=$1`, task)
		_, _ = f.db.ExecContext(context.Background(), `DELETE FROM task_statuses WHERE project_id=$1`, proj)
		_, _ = f.db.ExecContext(context.Background(), `DELETE FROM projects WHERE id=$1`, proj)
	})
	return task
}

func TestSessionRepo_AddUsage_TaskScopedRowIsSeparate_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)
	ctx := context.Background()
	taskA, taskB := f.seedTask(t), f.seedTask(t)

	wide, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, nil, 1, 1, 0, "")
	require.NoError(t, err)
	a1, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, &taskA, 5, 5, 0, "")
	require.NoError(t, err)
	a2, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, &taskA, 6, 1, 0, "")
	require.NoError(t, err)
	b1, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, &taskB, 9, 9, 0, "")
	require.NoError(t, err)

	assert.Equal(t, a1.ID, a2.ID)
	assert.EqualValues(t, 11, a2.TokensIn)
	require.NotNil(t, a2.TaskID)
	assert.Equal(t, taskA, *a2.TaskID)
	assert.NotEqual(t, wide.ID, a1.ID, "a task report never piles onto the agent-wide session")
	assert.NotEqual(t, a1.ID, b1.ID, "two tasks, two sessions")
	assert.EqualValues(t, 9, b1.TokensIn)
	assert.Equal(t, 3, f.sessions(t))
}

func TestSessionRepo_AddUsage_ConcurrentFirstReportsShareOneSession_RealPostgres(t *testing.T) {
	for round := 0; round < 5; round++ {
		f := newUsageFixture(t)
		const n = 25
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, errs[i] = f.repo.AddUsage(context.Background(), f.agentID, f.wsID, nil, 3, 2, 0, "")
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			require.NoError(t, err, "round %d report %d", round, i)
		}
		var in, out int64
		require.NoError(t, f.db.QueryRow(`SELECT COALESCE(sum(tokens_in),0), COALESCE(sum(tokens_out),0) FROM agent_sessions WHERE agent_id=$1`, f.agentID).Scan(&in, &out))
		require.Equal(t, 1, f.sessions(t), "round %d: one active session", round)
		require.EqualValues(t, 3*n, in, "round %d", round)
		require.EqualValues(t, 2*n, out, "round %d", round)
	}
}

func TestSessionRepo_AddUsage_DatabaseFailuresSurface_RealPostgres(t *testing.T) {
	f := newUsageFixture(t)

	t.Run("cannot begin", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s, err := f.repo.AddUsage(ctx, f.agentID, f.wsID, nil, 1, 1, 0, "")
		require.Error(t, err)
		assert.Nil(t, s)
		assert.Equal(t, 0, f.sessions(t))
	})
	t.Run("insert refused by a foreign key", func(t *testing.T) {
		s, err := f.repo.AddUsage(context.Background(), f.agentID, uuid.New(), nil, 1, 1, 0, "")
		require.Error(t, err, "a session for a workspace that does not exist must not be created")
		assert.Nil(t, s)
		assert.Equal(t, 0, f.sessions(t))
	})
	t.Run("update overflows the column", func(t *testing.T) {
		_, err := f.repo.AddUsage(context.Background(), f.agentID, f.wsID, nil, math.MaxInt64, 0, 0, "")
		require.NoError(t, err)
		s, err := f.repo.AddUsage(context.Background(), f.agentID, f.wsID, nil, 1, 0, 0, "")
		require.Error(t, err, "bigint overflow is the database's refusal, not a silent wrap")
		assert.Nil(t, s)
		var in int64
		require.NoError(t, f.db.Get(&in, `SELECT tokens_in FROM agent_sessions WHERE agent_id=$1`, f.agentID))
		assert.EqualValues(t, math.MaxInt64, in, "the refused addition left the row unchanged")
	})
}
