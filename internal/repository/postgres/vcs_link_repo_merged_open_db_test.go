package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

func seedMergedPR(t *testing.T, repo *VCSLinkRepo, fx coverageFixture, status domain.VCSLinkStatus, n string) {
	t.Helper()
	_, err := repo.Upsert(context.Background(), &domain.VCSLink{
		ID: uuid.New(), TaskID: fx.taskID, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR,
		ExternalID: n, URL: "https://git.example.com/g/p-" + fx.suffix + "/-/merge_requests/" + n,
		Status: status, CreatedAt: time.Now(),
	})
	require.NoError(t, err)
}

func mergedTaskIDs(rows []repository.MergedPROpenTask) map[uuid.UUID]bool {
	m := map[uuid.UUID]bool{}
	for _, r := range rows {
		m[r.TaskID] = true
	}
	return m
}

func TestListOpenTasksWithMergedPR_OnlyFlaggedProjectsMergedAndOpenCards(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	ctx := context.Background()
	repo := NewVCSLinkRepo(db)

	flagged := newCoverageFixture(t, db)
	unflagged := newCoverageFixture(t, db)
	openPR := newCoverageFixture(t, db)
	for _, fx := range []coverageFixture{flagged, openPR} {
		_, err := db.ExecContext(ctx, `INSERT INTO project_rules (project_id, rule_type, config) VALUES ($1, 'workflow', '{"mid_pipeline":{"close_on_merge":true}}')`, fx.projectID)
		require.NoError(t, err)
	}
	seedMergedPR(t, repo, flagged, domain.VCSLinkStatusMerged, "1")
	seedMergedPR(t, repo, unflagged, domain.VCSLinkStatusMerged, "1")
	seedMergedPR(t, repo, openPR, domain.VCSLinkStatusMerged, "1")
	seedMergedPR(t, repo, openPR, domain.VCSLinkStatusOpen, "2") // a second, still-open PR keeps the card open

	var got map[uuid.UUID]bool
	for off := 0; off < 5000; off += 200 { // shared DB: page until our rows are seen
		rows, err := repo.ListOpenTasksWithMergedPR(ctx, 200, off)
		require.NoError(t, err)
		for id := range mergedTaskIDs(rows) {
			if got == nil {
				got = map[uuid.UUID]bool{}
			}
			got[id] = true
		}
		if len(rows) < 200 {
			break
		}
	}
	assert.True(t, got[flagged.taskID], "flagged project, merged PR, open card must be listed")
	assert.False(t, got[unflagged.taskID], "project without the flag must never occupy the page")
	assert.False(t, got[openPR.taskID], "a card with a still-open PR is not merged-but-open")

	_, err := db.ExecContext(ctx, `INSERT INTO activity_log (id, workspace_id, entity_type, entity_id, action, actor_id, actor_type, changes)
		VALUES ($1, $2, 'task', $3, 'workflow.shadow_close_on_merge', $4, 'system', '{}')`, uuid.New(), flagged.workspaceID, flagged.taskID, uuid.Nil)
	require.NoError(t, err)
	rows, err := repo.ListOpenTasksWithMergedPR(ctx, 5000, 0)
	require.NoError(t, err)
	assert.False(t, mergedTaskIDs(rows)[flagged.taskID], "an already-journaled card is not listed again")
}
