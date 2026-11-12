package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The addressee's inbox (GET /agents/me/decision-requests): the open questions
// asked of an agent on live cards of one workspace it belongs to, oldest first.

// insertDecision stores a request as written by ask_agent (or later closed),
// with a chosen created_at so the order is under the test's control.
func (f *typedOutcomeFixture) insertDecision(t *testing.T, task, from, to uuid.UUID, question string,
	state domain.DecisionState, age time.Duration) uuid.UUID {
	t.Helper()
	var answeredAt *time.Time
	if state == domain.DecisionAnswered {
		now := time.Now()
		answeredAt = &now
	}
	var id uuid.UUID
	require.NoError(t, f.db.Get(&id, `INSERT INTO decision_requests
		(task_id, from_agent_id, to_agent_id, question, state, answered_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		task, from, to, question, state, answeredAt, time.Now().Add(-age)))
	return id
}

func TestDecisionInbox_ListsOpenRequestsToTheAgent_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	_, workspaceID, err := NewTurnCommandRepo(f.db).TaskScope(ctx, f.task)
	require.NoError(t, err)
	card, err := NewTaskRepo(f.db).GetByID(ctx, f.task)
	require.NoError(t, err)

	newer := f.insertDecision(t, f.other, f.agent, f.agent2, "Which index?", domain.DecisionOpen, time.Hour)
	older := f.insertDecision(t, f.task, f.agent, f.agent2, "Which schema?", domain.DecisionOpen, 2*time.Hour)
	f.insertDecision(t, f.task, f.agent, f.agent2, "Already answered", domain.DecisionAnswered, 3*time.Hour)
	f.insertDecision(t, f.task, f.agent, f.agent2, "Taken back", domain.DecisionWithdrawn, 3*time.Hour)
	f.insertDecision(t, f.task, f.agent2, f.agent, "Asked of the other agent", domain.DecisionOpen, 3*time.Hour)

	// A question on a card that was deleted afterwards is nobody's work.
	gone := &domain.Task{ID: uuid.New(), ProjectID: card.ProjectID, StatusID: card.StatusID, Title: "deleted card",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
	require.NoError(t, NewTaskRepo(f.db).Create(ctx, gone, nil))
	f.insertDecision(t, gone.ID, f.agent, f.agent2, "On a deleted card", domain.DecisionOpen, 3*time.Hour)
	_, err = f.db.Exec(`UPDATE tasks SET deleted_at = now() WHERE id = $1`, gone.ID)
	require.NoError(t, err)

	items, err := f.asks.ListOpenForAgent(ctx, f.agent2, workspaceID, 100)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, []uuid.UUID{older, newer}, []uuid.UUID{items[0].ID, items[1].ID}, "oldest first")

	name, slug := f.agentName(t, f.agent)
	first := items[0]
	assert.Equal(t, f.task, first.TaskID)
	assert.Equal(t, "Which schema?", first.Question)
	assert.Equal(t, domain.DecisionOpen, first.State)
	assert.False(t, first.CreatedAt.IsZero())
	assert.Equal(t, domain.AgentRef{ID: f.agent, Name: name, Slug: slug}, first.FromAgent)
	assert.Equal(t, domain.DecisionRequestTask{ID: f.task, Title: "waiting card", ProjectID: card.ProjectID,
		WorkspaceID: workspaceID, StatusCategory: domain.StatusCategoryTodo}, first.Task)
	assert.Equal(t, f.other, items[1].Task.ID)

	limited, err := f.asks.ListOpenForAgent(ctx, f.agent2, workspaceID, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	assert.Equal(t, older, limited[0].ID)

	mine, err := f.asks.ListOpenForAgent(ctx, f.agent, workspaceID, 100)
	require.NoError(t, err)
	require.Len(t, mine, 1, "only the question addressed to the agent itself")
	assert.Equal(t, "Asked of the other agent", mine[0].Question)

	elsewhere, err := f.asks.ListOpenForAgent(ctx, f.agent2, uuid.New(), 100)
	require.NoError(t, err)
	assert.Empty(t, elsewhere, "a key of another workspace sees none of this workspace's questions")
	assert.NotNil(t, elsewhere, "an empty inbox is [] not null")
}

func TestDecisionInbox_GuestAgentByActiveGrant_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	_, workspaceID, err := NewTurnCommandRepo(f.db).TaskScope(ctx, f.task)
	require.NoError(t, err)

	s := uuid.New().String()[:8]
	home := &domain.Workspace{ID: uuid.New(), Name: "inbox-home", Slug: "inbox-home-" + s, OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(f.db).Create(ctx, home))
	guest := &domain.Agent{
		ID: uuid.New(), WorkspaceID: home.ID, Name: "Lead-" + s, Slug: "lead-" + s,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + s, APIKeyPrefix: s, APIKeySHA256: "typed-" + uuid.New().String(),
	}
	require.NoError(t, NewAgentRepo(f.db).Create(ctx, guest))
	asked := f.insertDecision(t, f.task, f.agent, guest.ID, "Ship it?", domain.DecisionOpen, time.Minute)

	list := func(ws uuid.UUID) []domain.InboxDecisionRequest {
		t.Helper()
		items, lerr := f.asks.ListOpenForAgent(ctx, guest.ID, ws, 100)
		require.NoError(t, lerr)
		return items
	}
	assert.Empty(t, list(workspaceID), "an agent of another workspace without a grant")

	_, err = f.db.Exec(`INSERT INTO agent_workspace_grants (agent_id, workspace_id, api_key_prefix, api_key_hash)
		VALUES ($1, $2, $3, 'x')`, guest.ID, workspaceID, "g"+s)
	require.NoError(t, err)
	items := list(workspaceID)
	require.Len(t, items, 1)
	assert.Equal(t, asked, items[0].ID)
	assert.Equal(t, workspaceID, items[0].Task.WorkspaceID)
	assert.Empty(t, list(home.ID), "the home key does not list the guest workspace's questions")

	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET revoked_at = now() WHERE agent_id = $1`, guest.ID)
	require.NoError(t, err)
	assert.Empty(t, list(workspaceID), "a revoked grant hides the workspace's questions")
}
