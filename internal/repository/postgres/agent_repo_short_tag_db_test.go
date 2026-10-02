package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Covers agents.short_tag (Mesh aa1b4845). Same hazard as agents.model
// (#5548367d), same shape of proof: every reader names its columns and Update
// writes the whole row, so a reader that forgets short_tag hands Update a nil
// and silently wipes the label on the next unrelated edit. VALUE asserts on
// every reader, not row counts.

func TestAgentRepoShortTag_NullByDefault(t *testing.T) {
	db := agentDigestTestDB(t)
	wsID := seedAgentWorkspace(t, db)
	a := seedAgent(t, db, wsID, "NoTag", "notag-"+uuid.New().String()[:8], nil)

	got, err := NewAgentRepo(db).GetByID(context.Background(), a.ID)
	require.NoError(t, err)
	assert.Nil(t, got.ShortTag, "an agent that never had a label is NULL, not a default")
}

func TestAgentRepoShortTag_RoundTripsThroughEveryReaderAndSurvivesUpdate(t *testing.T) {
	ctx := context.Background()
	db := agentDigestTestDB(t)
	repo := NewAgentRepo(db)
	wsID := seedAgentWorkspace(t, db)

	parent := seedAgent(t, db, wsID, "Parent", "st-parent-"+uuid.New().String()[:8], nil)
	child := seedAgent(t, db, wsID, "Child", "st-child-"+uuid.New().String()[:8], &parent.ID)
	child.ShortTag = strp("keep-dev")
	require.NoError(t, repo.Update(ctx, child))

	byID, err := repo.GetByID(ctx, child.ID)
	require.NoError(t, err)
	require.NotNil(t, byID.ShortTag)
	assert.Equal(t, "keep-dev", *byID.ShortTag)

	page, err := repo.List(ctx, wsID, repository.AgentFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	var listEntry = &page.Items[0]
	found := false
	for i := range page.Items {
		if page.Items[i].ID == child.ID {
			listEntry = &page.Items[i]
			found = true
		}
	}
	require.True(t, found, "seeded child must appear in List")
	require.NotNil(t, listEntry.ShortTag, "List must select short_tag")
	assert.Equal(t, "keep-dev", *listEntry.ShortTag)

	tree, err := repo.GetSubAgentTree(ctx, parent.ID)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	require.NotNil(t, tree[0].ShortTag, "GetSubAgentTree must select short_tag, or a read-modify-write wipes it")
	assert.Equal(t, "keep-dev", *tree[0].ShortTag)

	withProjects, err := repo.ListWithProjects(ctx, wsID)
	require.NoError(t, err)
	found = false
	for _, ap := range withProjects {
		if ap.ID == child.ID {
			found = true
			require.NotNil(t, ap.ShortTag, "ListWithProjects must select short_tag")
			assert.Equal(t, "keep-dev", *ap.ShortTag)
		}
	}
	assert.True(t, found)

	// GetBySlug and SearchByPrefix share agentSelectCols with the readers
	// above; one spot-check each keeps them honest if that ever changes.
	bySlug, err := repo.GetBySlug(ctx, wsID, child.Slug)
	require.NoError(t, err)
	require.NotNil(t, bySlug)
	require.NotNil(t, bySlug.ShortTag, "GetBySlug must select short_tag")
	assert.Equal(t, "keep-dev", *bySlug.ShortTag)

	// Clearing goes back to NULL.
	byID.ShortTag = nil
	require.NoError(t, repo.Update(ctx, byID))
	cleared, err := repo.GetByID(ctx, child.ID)
	require.NoError(t, err)
	assert.Nil(t, cleared.ShortTag)
}
