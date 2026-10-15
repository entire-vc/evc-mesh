package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// keyRepoStub is the minimum AgentAPIKeyRepository Issue touches; everything
// else panics so an unexpected call is loud.
type keyRepoStub struct {
	createdRows int
}

func (k *keyRepoStub) Create(_ context.Context, row *domain.AgentAPIKey) error {
	k.createdRows++
	return nil
}
func (k *keyRepoStub) CountActiveByAgent(context.Context, uuid.UUID) (int, error) { return 0, nil }
func (k *keyRepoStub) GetByPrefix(context.Context, uuid.UUID, string) (*domain.AgentAPIKey, error) {
	panic("unexpected")
}
func (k *keyRepoStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.AgentAPIKey, error) {
	panic("unexpected")
}
func (k *keyRepoStub) IsRevoked(context.Context, uuid.UUID) (bool, error) { panic("unexpected") }
func (k *keyRepoStub) Revoke(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	panic("unexpected")
}
func (k *keyRepoStub) ListByAgent(context.Context, uuid.UUID) ([]domain.AgentAPIKey, error) {
	panic("unexpected")
}

// Registering an agent as a human records that human as the sponsor; with no
// human in the context the named supervisor is used.
func TestAgentService_Register_RecordsSponsor(t *testing.T) {
	svc, _, ws := setupAgentService()
	rec := &sponsorRecorder{}
	svc.SetAgentSponsorRecorder(rec)

	human, supervisor := uuid.New(), uuid.New()
	ctx := actorctx.WithActor(context.Background(), human, domain.ActorTypeUser)
	out, err := svc.Register(ctx, RegisterAgentInput{WorkspaceID: ws.ID, Name: "Scout", AgentType: domain.AgentTypeClaudeCode})
	require.NoError(t, err)
	assert.Equal(t, 1, rec.calls)
	assert.Equal(t, out.Agent.ID, rec.agent)
	assert.Equal(t, human, rec.user)

	rec2 := &sponsorRecorder{}
	svc.SetAgentSponsorRecorder(rec2)
	out, err = svc.Register(context.Background(), RegisterAgentInput{WorkspaceID: ws.ID, Name: "Bot", AgentType: domain.AgentTypeClaudeCode, SupervisorUserID: &supervisor})
	require.NoError(t, err)
	assert.Equal(t, supervisor, rec2.user)
	assert.Equal(t, out.Agent.ID, rec2.agent)
}

// A key issued by a human makes that human the sponsor; a key issued by a
// non-human actor changes nothing.
func TestAgentKeyService_Issue_RecordsSponsor(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	wsRepo := NewMockWorkspaceRepository()
	ws := &domain.Workspace{ID: uuid.New(), Name: "Acme", Slug: "acme"}
	wsRepo.items[ws.ID] = ws
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Scout", AgentType: domain.AgentTypeClaudeCode}
	require.NoError(t, agentRepo.Create(context.Background(), agent))

	keys := &keyRepoStub{}
	svc := NewAgentKeyService(agentRepo, wsRepo, keys, NewMockActivityLogRepository(), nil, nil).(*agentKeyService)
	rec := &sponsorRecorder{}
	svc.SetAgentSponsorRecorder(rec)

	human := uuid.New()
	ctx := actorctx.WithActor(context.Background(), human, domain.ActorTypeUser)
	_, err := svc.Issue(ctx, agent.ID, "ci", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, keys.createdRows)
	assert.Equal(t, 1, rec.calls)
	assert.Equal(t, agent.ID, rec.agent)
	assert.Equal(t, human, rec.user)

	rec2 := &sponsorRecorder{}
	svc.SetAgentSponsorRecorder(rec2)
	_, err = svc.Issue(actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent), agent.ID, "ci", time.Hour)
	require.NoError(t, err)
	assert.Zero(t, rec2.calls, "a non-human issuer does not become the sponsor")
}
