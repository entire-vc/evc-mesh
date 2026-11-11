package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// A guest agent — homed in another workspace, admitted here by an active
// grant — is in this workspace's team directory: a question or a hand-off
// reaches it like a home agent. Measured on 2026-11-11: a reviewer could not
// hand a card back to the lead that serves two workspaces (not_an_agent by
// name, slug and id), and the card stayed on the reviewer.
func TestTypedCommands_GuestAgentOfTheWorkspace_DB(t *testing.T) {
	f := newTypedOutcomeFixture(t)
	ctx := context.Background()
	repo := NewTurnCommandRepo(f.db)
	_, workspaceID, err := repo.TaskScope(ctx, f.task)
	require.NoError(t, err)

	s := uuid.New().String()[:8]
	home := &domain.Workspace{ID: uuid.New(), Name: "guest-home", Slug: "guest-home-" + s, OwnerID: uuid.New()}
	require.NoError(t, NewWorkspaceRepo(f.db).Create(ctx, home))
	guest := &domain.Agent{
		ID: uuid.New(), WorkspaceID: home.ID, Name: "Guest-" + s, Slug: "guest-" + s,
		AgentType: domain.AgentTypeClaudeCode, Status: domain.AgentStatusOffline,
		APIKeyHash: "$2a$12$" + s, APIKeyPrefix: s, APIKeySHA256: "typed-" + uuid.New().String(),
	}
	require.NoError(t, NewAgentRepo(f.db).Create(ctx, guest))

	// Without a grant the agent of another workspace is nobody here.
	_, _, found, err := repo.ResolveAgent(ctx, workspaceID, guest.Slug)
	require.NoError(t, err)
	assert.False(t, found, "an agent of another workspace without a grant")
	_, err = NewTaskRepo(f.db).AskAgent(ctx, f.ask(guest.Slug))
	assert.Equal(t, domain.CodeNotAnAgent, outcomeCodeOf(t, err))

	_, err = f.db.Exec(`INSERT INTO agent_workspace_grants (agent_id, workspace_id, api_key_prefix, api_key_hash)
		VALUES ($1, $2, $3, 'x')`, guest.ID, workspaceID, "g"+s)
	require.NoError(t, err)

	for _, to := range []string{guest.ID.String(), guest.Slug, "GUEST-" + s} {
		id, name, ok, rerr := repo.ResolveAgent(ctx, workspaceID, to)
		require.NoError(t, rerr)
		require.True(t, ok, to)
		assert.Equal(t, guest.ID, id)
		assert.Equal(t, guest.Name, name)
	}

	asked, err := NewTaskRepo(f.db).AskAgent(ctx, f.ask("@"+guest.Slug))
	require.NoError(t, err)
	assert.Equal(t, guest.ID, asked.To.ID)

	to := guest.ID
	f.holdCheckout(t, f.task, f.agent)
	res, err := repo.CommitTurn(ctx, &domain.TurnCommand{TaskID: f.task, AgentID: f.agent, Kind: domain.TurnOutcomeHandoff,
		HandoffTo: &to, Note: "back to the lead", CommentBody: "Handed over.\n\nback to the lead"})
	require.NoError(t, err)
	require.NotNil(t, res.Outcome)
	var assignee uuid.UUID
	require.NoError(t, f.db.Get(&assignee, `SELECT assignee_id FROM tasks WHERE id = $1`, f.task))
	assert.Equal(t, guest.ID, assignee)

	// A revoked grant takes the guest out of the directory again.
	_, err = f.db.Exec(`UPDATE agent_workspace_grants SET revoked_at = now() WHERE agent_id = $1`, guest.ID)
	require.NoError(t, err)
	_, _, found, err = repo.ResolveAgent(ctx, workspaceID, guest.Slug)
	require.NoError(t, err)
	assert.False(t, found, "a revoked grant")
}
