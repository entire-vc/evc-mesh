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

// WorkspaceProviderActivity counts one provider's links across a workspace and
// reports the latest sign of life. Same skip-if-no-DB convention as the other
// vcs_link repo tests.
func TestVCSLinkRepo_WorkspaceProviderActivity(t *testing.T) {
	db := vcsLinkUpsertTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)
	taskID := vcsLinkTestTask(t, db)

	var wsID uuid.UUID
	require.NoError(t, db.GetContext(ctx, &wsID, `SELECT p.workspace_id FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID))

	n, last, err := repo.WorkspaceProviderActivity(ctx, wsID, "gitlab")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Nil(t, last)

	_, err = repo.Upsert(ctx, &domain.VCSLink{
		ID: uuid.New(), TaskID: taskID, Provider: domain.VCSProviderGitLab,
		LinkType: domain.VCSLinkTypePR, ExternalID: "7",
		URL:    "https://gitlab.example.com/g/p/-/merge_requests/7",
		Status: domain.VCSLinkStatusOpen, CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	n, last, err = repo.WorkspaceProviderActivity(ctx, wsID, "gitlab")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.NotNil(t, last)

	n, _, err = repo.WorkspaceProviderActivity(ctx, wsID, "github")
	require.NoError(t, err)
	assert.Equal(t, 0, n, "another provider's links are not counted")
}
