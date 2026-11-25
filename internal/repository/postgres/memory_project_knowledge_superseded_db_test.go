package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Exercise the real SQL in this package so coverage measures both the count
// and data queries, in addition to the HTTP regression in handler.
func TestMemoryRepoDB_ProjectKnowledgeExcludeSuperseded(t *testing.T) {
	repo, wsID := setupMemoryScopeIdentityDBTest(t)
	ctx := context.Background()
	projectID := createScopeIdentityTestProject(t, NewProjectRepo(repo.db), wsID)
	t.Cleanup(func() {
		_, _ = repo.db.Exec(`DELETE FROM memory_revisions WHERE workspace_id=$1`, wsID)
		_, _ = repo.db.Exec(`DELETE FROM memories WHERE workspace_id=$1`, wsID)
		_, _ = repo.db.Exec(`DELETE FROM projects WHERE workspace_id=$1`, wsID)
	})

	ids := make(map[string]uuid.UUID)
	for _, scope := range []domain.MemoryScope{domain.ScopeProject, domain.ScopeWorkspace} {
		for _, status := range []domain.MemoryStatus{domain.MemoryStatusActive, domain.MemoryStatusSuperseded} {
			key := string(scope) + "-" + string(status)
			m := &domain.Memory{
				ID: uuid.New(), WorkspaceID: wsID, Key: key, Content: key,
				Scope: scope, SourceType: domain.SourceAgent,
			}
			if scope == domain.ScopeProject {
				m.ProjectID = &projectID
			}
			require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
			_, err := repo.db.Exec(`UPDATE memories SET status=$1 WHERE id=$2`, status, m.ID)
			require.NoError(t, err)
			var storedStatus domain.MemoryStatus
			require.NoError(t, repo.db.Get(&storedStatus, `SELECT status FROM memories WHERE id=$1`, m.ID))
			require.Equal(t, status, storedStatus)
			ids[key] = m.ID
		}
	}

	for _, tc := range []struct {
		name    string
		project *uuid.UUID
		exclude bool
		keys    []string
	}{
		{"project excludes superseded", &projectID, true, []string{"project-active"}},
		{"project retains history", &projectID, false, []string{"project-active", "project-superseded"}},
		{"workspace excludes superseded", nil, true, []string{"project-active", "workspace-active"}},
		{"workspace retains history", nil, false, []string{"project-active", "project-superseded", "workspace-active", "workspace-superseded"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, total, err := repo.ListByWorkspaceProject(ctx, wsID, tc.project, domain.MemoryListFilter{ExcludeSuperseded: tc.exclude})
			require.NoError(t, err)
			require.Equal(t, int64(len(tc.keys)), total, "count query applies the same status filter")
			wantIDs := make([]uuid.UUID, 0, len(tc.keys))
			for _, key := range tc.keys {
				wantIDs = append(wantIDs, ids[key])
			}
			gotIDs := make([]uuid.UUID, 0, len(rows))
			for _, row := range rows {
				gotIDs = append(gotIDs, row.ID)
				if tc.exclude {
					require.NotEqual(t, domain.MemoryStatusSuperseded, row.Status)
				}
			}
			require.ElementsMatch(t, wantIDs, gotIDs)
		})
	}
}
