package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// No //go:build integration tag on purpose — see userRepoTestDB's doc comment
// in user_repo_test.go. These are the regression tests for the agentSelectCols
// refactor (explicit columns instead of SELECT *): the whole point of that
// change is that these reads work against the REAL schema, which a sqlmock
// test cannot exercise — sqlmock replays whatever rows the test wrote, it
// never asks PostgreSQL whether the column list actually matches the table.

func agentRepoDBTestWorkspace(t *testing.T) uuid.UUID {
	t.Helper()
	db := userRepoTestDB(t)
	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "agent-repo-db-test-ws",
		Slug:    "agent-repo-db-test-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))
	return ws.ID
}

func agentRepoDBTestAgent(t *testing.T, repo *AgentRepo, wsID uuid.UUID, parentID *uuid.UUID) *domain.Agent {
	t.Helper()
	slug := "agent-repo-db-test-" + uuid.New().String()[:8]
	agent := &domain.Agent{
		ID:            uuid.New(),
		WorkspaceID:   wsID,
		ParentAgentID: parentID,
		Name:          slug,
		Slug:          slug,
		AgentType:     domain.AgentTypeCustom,
		Status:        domain.AgentStatusOffline,
		APIKeyHash:    "irrelevant-hash",
		APIKeyPrefix:  slug[:8],
	}
	require.NoError(t, repo.Create(context.Background(), agent))
	return agent
}

func TestAgentRepoDB_GetByID(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewAgentRepo(db)
	wsID := agentRepoDBTestWorkspace(t)
	created := agentRepoDBTestAgent(t, repo, wsID, nil)

	got, err := repo.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, created.Slug, got.Slug)

	// Unknown ID -> (nil, nil), not an error — proves the explicit column list
	// still scans cleanly on the "no rows" path.
	missing, err := repo.GetByID(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestAgentRepoDB_GetByAPIKeyPrefix(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewAgentRepo(db)
	wsID := agentRepoDBTestWorkspace(t)
	created := agentRepoDBTestAgent(t, repo, wsID, nil)

	got, err := repo.GetByAPIKeyPrefix(context.Background(), wsID, created.APIKeyPrefix)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)

	missing, err := repo.GetByAPIKeyPrefix(context.Background(), wsID, "no-such-prefix")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestAgentRepoDB_GetBySlug(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewAgentRepo(db)
	wsID := agentRepoDBTestWorkspace(t)
	created := agentRepoDBTestAgent(t, repo, wsID, nil)

	got, err := repo.GetBySlug(context.Background(), wsID, created.Slug)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)

	missing, err := repo.GetBySlug(context.Background(), wsID, "no-such-slug")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestAgentRepoDB_List(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewAgentRepo(db)
	wsID := agentRepoDBTestWorkspace(t)
	created := agentRepoDBTestAgent(t, repo, wsID, nil)

	page, err := repo.List(context.Background(), wsID, repository.AgentFilter{}, pagination.Params{})
	require.NoError(t, err)
	require.NotNil(t, page)

	var found bool
	for _, a := range page.Items {
		if a.ID == created.ID {
			found = true
		}
	}
	assert.True(t, found, "List must include the agent just created in this workspace")
}

func TestAgentRepoDB_SearchByPrefix(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewAgentRepo(db)
	wsID := agentRepoDBTestWorkspace(t)
	created := agentRepoDBTestAgent(t, repo, wsID, nil)

	results, err := repo.SearchByPrefix(context.Background(), wsID, created.Slug, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, created.ID, results[0].ID)

	empty, err := repo.SearchByPrefix(context.Background(), wsID, "no-such-agent-search-hit", 10)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestAgentRepoDB_GetSubAgentTree(t *testing.T) {
	db := userRepoTestDB(t)
	repo := NewAgentRepo(db)
	wsID := agentRepoDBTestWorkspace(t)

	parent := agentRepoDBTestAgent(t, repo, wsID, nil)
	child := agentRepoDBTestAgent(t, repo, wsID, &parent.ID)
	grandchild := agentRepoDBTestAgent(t, repo, wsID, &child.ID)

	tree, err := repo.GetSubAgentTree(context.Background(), parent.ID)
	require.NoError(t, err)
	require.Len(t, tree, 2, "subtree must contain the child and grandchild, not the parent itself")

	ids := map[uuid.UUID]bool{}
	for _, a := range tree {
		ids[a.ID] = true
	}
	assert.True(t, ids[child.ID])
	assert.True(t, ids[grandchild.ID])
}
