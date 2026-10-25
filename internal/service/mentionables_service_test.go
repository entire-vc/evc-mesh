package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type mentionablesAgentRepo struct {
	repository.AgentRepository
	agents []domain.Agent
}

func (r mentionablesAgentRepo) SearchByPrefix(context.Context, uuid.UUID, string, int) ([]domain.Agent, error) {
	return r.agents, nil
}

type mentionablesUserRepo struct {
	repository.UserRepository
	users []domain.User
}

func (r mentionablesUserRepo) SearchInWorkspace(context.Context, uuid.UUID, string, int) ([]domain.User, error) {
	return r.users, nil
}

func TestMentionablesSearch_ServiceUserMarker(t *testing.T) {
	agentID := uuid.New()
	svc := NewMentionablesService(
		mentionablesAgentRepo{agents: []domain.Agent{{ID: agentID, Name: "Hana", Slug: "hana"}}},
		mentionablesUserRepo{users: []domain.User{
			{ID: uuid.New(), Name: "Hana (QA Mesh)", Username: "hana-qa", ServiceAgentID: &agentID},
			// Same display-name shape, but no agent link: a person, not a service user.
			{ID: uuid.New(), Name: "Hana (QA X)", Username: "hana-x"},
		}},
	)

	got, err := svc.Search(context.Background(), uuid.New(), "hana", 10)
	require.NoError(t, err)

	byName := map[string]domain.Mentionable{}
	for _, m := range got {
		byName[m.DisplayName] = m
	}

	svcUser := byName["Hana (QA Mesh)"]
	assert.True(t, svcUser.IsServiceUser)
	require.NotNil(t, svcUser.AgentID)
	assert.Equal(t, agentID, *svcUser.AgentID)

	person := byName["Hana (QA X)"]
	assert.False(t, person.IsServiceUser, "a real user named like a service user must not be flagged")
	assert.Nil(t, person.AgentID)

	agent := byName["Hana"]
	assert.False(t, agent.IsServiceUser)
	assert.Nil(t, agent.AgentID)
}
