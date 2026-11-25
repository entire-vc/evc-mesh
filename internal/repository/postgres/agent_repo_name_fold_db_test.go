package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ListByNameFold backs the @Name mention fallback (Mesh 8a018bf6): an agent is
// found by its display name, case-insensitively, inside its own workspace only,
// never once deleted, and the limit lets the caller see a second match as
// ambiguity.

func TestAgentRepoListByNameFold_MatchesNameCaseInsensitively(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	nova := seedAgent(t, db, wsID, "Nova", "atlas-"+uuid.New().String()[:8], nil)
	seedAgent(t, db, wsID, "Novasz", "novasz-"+uuid.New().String()[:8], nil)

	got, err := NewAgentRepo(db).ListByNameFold(context.Background(), wsID, "nova", 2)
	require.NoError(t, err)
	require.Len(t, got, 1, "exact name only, not a prefix")
	assert.Equal(t, nova.ID, got[0].ID)
	assert.Equal(t, nova.Slug, got[0].Slug)
	assert.Equal(t, nova.APIKeyPrefix, got[0].APIKeyPrefix, "full row is read back")
}

func TestAgentRepoListByNameFold_ScopesToWorkspaceAndSkipsDeleted(t *testing.T) {
	db := agentDigestTestDB(t)
	mine := seedAgentWorkspace(t, db)
	theirs := seedAgentWorkspace(t, db)
	seedAgent(t, db, theirs, "Nova", "nova-theirs-"+uuid.New().String()[:8], nil)
	gone := seedAgent(t, db, mine, "Nova", "nova-gone-"+uuid.New().String()[:8], nil)
	repo := NewAgentRepo(db)
	require.NoError(t, repo.Delete(context.Background(), gone.ID))

	got, err := repo.ListByNameFold(context.Background(), mine, "NOVA", 2)
	require.NoError(t, err)
	assert.Empty(t, got, "another workspace's agent and a deleted one never match")
}

func TestAgentRepoListByNameFold_LimitShowsAmbiguity(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	for i := 0; i < 3; i++ {
		seedAgent(t, db, wsID, "Twin", "twin-"+uuid.New().String()[:8], nil)
	}

	got, err := NewAgentRepo(db).ListByNameFold(context.Background(), wsID, "twin", 2)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}
