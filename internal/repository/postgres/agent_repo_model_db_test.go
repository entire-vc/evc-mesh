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

// Covers agents.model (#5548367d). The hazard is the same one the readers test
// describes: every reader names its columns, and Update writes the whole row, so
// a reader that forgets `model` hands Update a nil and silently wipes the value.
// Each reader is therefore asserted on the VALUE, not on a row count.

func strp(s string) *string { return &s }

func TestAgentRepoModel_NullByDefault(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	a := seedAgent(t, db, wsID, "NoModel", "nomodel-"+uuid.New().String()[:8], nil)

	got, err := NewAgentRepo(db).GetByID(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Model, "an agent that never reported a model is NULL, not a default")
}

func TestAgentRepoModel_RoundTripsThroughEveryReaderAndSurvivesUpdate(t *testing.T) {
	ctx := context.Background()
	db := agentDigestTestDB(t)
	repo := NewAgentRepo(db)
	wsID := seedAgentWorkspace(t, db)

	parent := seedAgent(t, db, wsID, "Parent", "parent-"+uuid.New().String()[:8], nil)
	child := seedAgent(t, db, wsID, "Child", "child-"+uuid.New().String()[:8], &parent.ID)
	child.Model = strp("gpt-6.1-sol")
	require.NoError(t, repo.Update(ctx, child))

	byID, err := repo.GetByID(ctx, child.ID)
	require.NoError(t, err)
	require.NotNil(t, byID.Model)
	assert.Equal(t, "gpt-6.1-sol", *byID.Model)

	page, err := repo.List(ctx, wsID, repository.AgentFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	var fromList *domain.Agent
	for i := range page.Items {
		if page.Items[i].ID == child.ID {
			fromList = &page.Items[i]
		}
	}
	require.NotNil(t, fromList)
	require.NotNil(t, fromList.Model, "List must select model")
	assert.Equal(t, "gpt-6.1-sol", *fromList.Model)

	tree, err := repo.GetSubAgentTree(ctx, parent.ID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	require.NotNil(t, tree[0].Model, "GetSubAgentTree must select model, or a read-modify-write wipes it")
	assert.Equal(t, "gpt-6.1-sol", *tree[0].Model)

	withProjects, err := repo.ListWithProjects(ctx, wsID)
	require.NoError(t, err)
	found := false
	for _, ap := range withProjects {
		if ap.ID == child.ID {
			found = true
			require.NotNil(t, ap.Model, "ListWithProjects must select model")
			assert.Equal(t, "gpt-6.1-sol", *ap.Model)
		}
	}
	assert.True(t, found)

	// Clearing goes back to NULL.
	byID.Model = nil
	require.NoError(t, repo.Update(ctx, byID))
	cleared, err := repo.GetByID(ctx, child.ID)
	require.NoError(t, err)
	assert.Nil(t, cleared.Model)
}

func TestAgentRepoModel_HeartbeatSelfReport(t *testing.T) {
	ctx := context.Background()
	db := agentDigestTestDB(t)
	repo := NewAgentRepo(db)
	wsID := seedAgentWorkspace(t, db)
	a := seedAgent(t, db, wsID, "HB", "hb-"+uuid.New().String()[:8], nil)

	require.NoError(t, repo.UpdateHeartbeat(ctx, a.ID, &repository.UpdateHeartbeatParams{
		Status: "working", AgentType: domain.AgentTypeCodex, Model: strp("gpt-6.1-sol"),
	}))
	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.AgentTypeCodex, got.AgentType)
	require.NotNil(t, got.Model)
	assert.Equal(t, "gpt-6.1-sol", *got.Model)
	assert.Equal(t, "working", got.HeartbeatStatus)

	// A heartbeat that does not mention them leaves them alone.
	require.NoError(t, repo.UpdateHeartbeat(ctx, a.ID, &repository.UpdateHeartbeatParams{Status: "idle"}))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.AgentTypeCodex, got.AgentType)
	require.NotNil(t, got.Model)

	// "" clears.
	require.NoError(t, repo.UpdateHeartbeat(ctx, a.ID, &repository.UpdateHeartbeatParams{Model: strp("")}))
	got, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Model)
}
