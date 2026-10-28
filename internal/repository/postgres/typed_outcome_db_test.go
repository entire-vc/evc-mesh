package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The typed-outcome guarantees that only the table can give: one active wait
// per (task, agent) even when two writers race past the supersede step, an
// append-only journal, and one open request per repeated question. Runs when a
// Postgres is reachable (deliveryOutcomeTestDB skips loudly otherwise).

type typedOutcomeFixture struct {
	db     *sqlx.DB
	task   uuid.UUID
	other  uuid.UUID
	agent  uuid.UUID
	agent2 uuid.UUID
	waits  *TaskWaitRepo
	turns  *TurnOutcomeRepo
	asks   *DecisionRequestRepo
}

func newTypedOutcomeFixture(t *testing.T) *typedOutcomeFixture {
	t.Helper()
	db := deliveryOutcomeTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]

	ws := &domain.Workspace{ID: uuid.New(), Name: "typed-ws", Slug: "typed-ws-" + suffix, OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, ws))
	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "typed-proj",
		Slug: "typed-proj-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))
	status := &domain.TaskStatus{
		ID: uuid.New(), ProjectID: proj.ID, Name: "Open", Slug: "open",
		Color: "#00FF00", Category: domain.StatusCategoryTodo, IsDefault: true,
	}
	require.NoError(t, NewTaskStatusRepo(db).Create(ctx, status))

	newTask := func(title string) uuid.UUID {
		task := &domain.Task{
			ID: uuid.New(), ProjectID: proj.ID, StatusID: status.ID, Title: title,
			AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
			CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		}
		require.NoError(t, NewTaskRepo(db).Create(ctx, task, nil))
		return task.ID
	}
	newAgent := func(name string) uuid.UUID {
		s := uuid.New().String()[:8]
		a := &domain.Agent{
			ID: uuid.New(), WorkspaceID: ws.ID, Name: name, Slug: name + "-" + s,
			AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
			APIKeyHash: "$2a$12$" + s, APIKeyPrefix: s, APIKeySHA256: "typed-" + uuid.New().String(),
		}
		require.NoError(t, NewAgentRepo(db).Create(ctx, a))
		return a.ID
	}

	return &typedOutcomeFixture{
		db: db, task: newTask("waiting card"), other: newTask("target card"),
		agent: newAgent("typed-a"), agent2: newAgent("typed-b"),
		waits: NewTaskWaitRepo(db), turns: NewTurnOutcomeRepo(db), asks: NewDecisionRequestRepo(db),
	}
}

func (f *typedOutcomeFixture) cardWait(agent uuid.UUID) *domain.TaskWait {
	other := f.other
	return &domain.TaskWait{TaskID: f.task, AgentID: agent, Kind: domain.TaskWaitCard, TargetTaskID: &other, Note: "needs the merged contract"}
}

func TestTaskWait_SecondActiveRejectedByIndex_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	require.NoError(t, f.waits.Declare(ctx, f.cardWait(f.agent)))

	// A writer that skips the supersede step — what a racing second Declare
	// amounts to — cannot add a second active wait for the same pair.
	_, err := f.db.ExecContext(ctx, `INSERT INTO task_waits (task_id, agent_id, kind, target_task_id)
		VALUES ($1, $2, 'card', $3)`, f.task, f.agent, f.other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task_waits_one_active")

	// Another agent on the same card is a different pair.
	require.NoError(t, f.waits.Declare(ctx, f.cardWait(f.agent2)))
}

func TestTaskWait_DeclareSupersedes_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	first := f.cardWait(f.agent)
	require.NoError(t, f.waits.Declare(ctx, first))

	until := time.Now().Add(time.Hour)
	second := &domain.TaskWait{TaskID: f.task, AgentID: f.agent, Kind: domain.TaskWaitTime, UntilAt: &until}
	require.NoError(t, f.waits.Declare(ctx, second))

	active, err := f.waits.GetActive(ctx, f.task, &f.agent)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, second.ID, active.ID)

	var res string
	require.NoError(t, f.db.GetContext(ctx, &res, `SELECT resolution FROM task_waits WHERE id = $1`, first.ID))
	assert.Equal(t, string(domain.WaitResolutionSuperseded), res)

	ok, err := f.waits.Resolve(ctx, second.ID, domain.WaitResolutionMet)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = f.waits.Resolve(ctx, second.ID, domain.WaitResolutionMet)
	require.NoError(t, err)
	assert.False(t, ok, "a resolved wait stays resolved")

	active, err = f.waits.GetActive(ctx, f.task, nil)
	require.NoError(t, err)
	assert.Nil(t, active)
}

func TestTaskWait_TableRefusesBadShapes_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	for name, q := range map[string]string{
		"self wait":          `INSERT INTO task_waits (task_id, agent_id, kind, target_task_id) VALUES ($1, $2, 'card', $1)`,
		"mr without iid":     `INSERT INTO task_waits (task_id, agent_id, kind, provider, project_path, until_state) VALUES ($1, $2, 'mr', 'gitlab', 'g/p', 'merged')`,
		"pipeline bad until": `INSERT INTO task_waits (task_id, agent_id, kind, project_path, pipeline_id, until_state) VALUES ($1, $2, 'pipeline', 'g/p', 1, 'merged')`,
		"time without until": `INSERT INTO task_waits (task_id, agent_id, kind) VALUES ($1, $2, 'time')`,
		"resolved no reason": `INSERT INTO task_waits (task_id, agent_id, kind, until_at, resolved_at) VALUES ($1, $2, 'time', now(), now())`,
		"unknown kind":       `INSERT INTO task_waits (task_id, agent_id, kind) VALUES ($1, $2, 'someday')`,
	} {
		_, err := f.db.ExecContext(ctx, q, f.task, f.agent)
		assert.Error(t, err, name)
	}
}

func TestTurnOutcomes_AppendOnlyAndSessionOrder_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	s1, s2 := "sess-1", "sess-2"

	empty, err := f.turns.ListByTask(ctx, f.task, &s1, 20)
	require.NoError(t, err)
	assert.NotNil(t, empty, "no outcome is an empty list, not null")
	assert.Empty(t, empty)

	w := f.cardWait(f.agent)
	require.NoError(t, f.waits.Declare(ctx, w))
	first := &domain.TurnOutcome{TaskID: f.task, AgentID: f.agent, SessionID: &s1, Kind: domain.TurnOutcomeNext, Note: "edited"}
	require.NoError(t, f.turns.Append(ctx, first))
	rk := domain.ResultKindChange
	second := &domain.TurnOutcome{TaskID: f.task, AgentID: f.agent, SessionID: &s1, Kind: domain.TurnOutcomeWait, WaitID: &w.ID, ResultKind: &rk}
	require.NoError(t, f.turns.Append(ctx, second))
	require.NoError(t, f.turns.Append(ctx, &domain.TurnOutcome{TaskID: f.task, AgentID: f.agent, SessionID: &s2, Kind: domain.TurnOutcomeDone}))

	got, err := f.turns.ListByTask(ctx, f.task, &s1, 20)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, second.ID, got[0].ID, "newest first")
	assert.Equal(t, first.ID, got[1].ID)

	all, err := f.turns.ListByTask(ctx, f.task, nil, 20)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	_, err = f.db.ExecContext(ctx, `UPDATE turn_outcomes SET note = 'rewritten' WHERE id = $1`, first.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")

	err = f.turns.Append(ctx, &domain.TurnOutcome{TaskID: f.task, AgentID: f.agent, Kind: domain.TurnOutcomeWait})
	assert.Error(t, err, "a wait outcome names its wait")
}

func TestDecisionRequests_ReuseOpenQuestion_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	q := &domain.DecisionRequest{TaskID: f.task, FromAgentID: f.agent, ToAgentID: f.agent2, Question: "Merge order: contract first?"}

	first, reused, err := f.asks.Open(ctx, q)
	require.NoError(t, err)
	assert.False(t, reused)
	assert.Equal(t, domain.DecisionOpen, first.State)

	again, reused, err := f.asks.Open(ctx, &domain.DecisionRequest{TaskID: f.task, FromAgentID: f.agent, ToAgentID: f.agent2, Question: "  merge order: contract first?  "})
	require.NoError(t, err)
	assert.True(t, reused)
	assert.Equal(t, first.ID, again.ID)

	ok, err := f.asks.Answer(ctx, first.ID, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = f.asks.Withdraw(ctx, first.ID)
	require.NoError(t, err)
	assert.False(t, ok, "only an open request can be withdrawn")

	got, err := f.asks.GetByID(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.DecisionAnswered, got.State)
	assert.NotNil(t, got.AnsweredAt)

	fresh, reused, err := f.asks.Open(ctx, q)
	require.NoError(t, err)
	assert.False(t, reused, "an answered question asked again is a new request")
	assert.NotEqual(t, first.ID, fresh.ID)

	_, _, err = f.asks.Open(ctx, &domain.DecisionRequest{TaskID: f.task, FromAgentID: f.agent, ToAgentID: f.agent, Question: "ask myself?"})
	assert.Error(t, err, "a question to yourself is refused")
}

func TestTasksResultKind_Column_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET result_kind = 'research' WHERE id = $1`, f.task)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE tasks SET result_kind = 'monitor' WHERE id = $1`, f.task)
	assert.Error(t, err)
}
