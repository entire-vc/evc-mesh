package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No //go:build integration tag on purpose — see userRepoTestDB's doc comment
// in user_repo_test.go. This proves migration 20260821005's whole point end to
// end through the real repo code: workspace_id survives a forget, which is
// exactly the field the Revisions handler now authorizes against instead of a
// GetByID lookup that a forget breaks (b043153b).

func setupMemoryRevisionWorkspaceDBTest(t *testing.T) (*MemoryRepo, uuid.UUID) {
	t.Helper()
	db := userRepoTestDB(t)

	wsRepo := NewWorkspaceRepo(db)
	ws := &domain.Workspace{
		ID:      uuid.New(),
		Name:    "revision-workspace-db-test-ws",
		Slug:    "revision-workspace-db-test-" + uuid.New().String()[:8],
		OwnerID: uuid.New(),
	}
	require.NoError(t, wsRepo.Create(context.Background(), ws))

	return NewMemoryRepo(db), ws.ID
}

// TestMemoryRepoDB_Upsert_StampsWorkspaceIDOnTheRevision proves the create/
// update path (Upsert) writes workspace_id on every revision it appends, not
// only on the live memories row.
func TestMemoryRepoDB_Upsert_StampsWorkspaceIDOnTheRevision(t *testing.T) {
	repo, wsID := setupMemoryRevisionWorkspaceDBTest(t)
	ctx := context.Background()

	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "upsert-stamps-ws-" + uuid.New().String()[:8],
		Content:     "v1",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, mem, domain.MemoryWriteIntent{Reason: "create"}))

	mem.Content = "v2"
	require.NoError(t, repo.Upsert(ctx, mem, domain.MemoryWriteIntent{Reason: "update"}))

	revs, err := repo.ListRevisions(ctx, mem.ID, 10)
	require.NoError(t, err)
	require.Len(t, revs, 2, "one revision per Upsert call")
	for _, r := range revs {
		require.NotNil(t, r.WorkspaceID, "every revision from Upsert must carry the workspace")
		assert.Equal(t, wsID, *r.WorkspaceID)
	}
}

// TestMemoryRepoDB_AppendRevision_StampsWorkspaceIDForForget proves the
// forget path (AppendRevision, called with no live memories row backing it —
// exactly how memory_service.Forget uses it, snapshotting immediately before
// the delete) writes workspace_id too.
func TestMemoryRepoDB_AppendRevision_StampsWorkspaceIDForForget(t *testing.T) {
	repo, wsID := setupMemoryRevisionWorkspaceDBTest(t)
	ctx := context.Background()
	memID := uuid.New()

	err := repo.AppendRevision(ctx, domain.MemoryRevision{
		MemoryID:    memID,
		Version:     2,
		Content:     "captured just before delete",
		Action:      domain.MemoryActionForgotten,
		WorkspaceID: &wsID,
	})
	require.NoError(t, err)

	revs, err := repo.ListRevisions(ctx, memID, 10)
	require.NoError(t, err)
	require.Len(t, revs, 1)
	require.NotNil(t, revs[0].WorkspaceID)
	assert.Equal(t, wsID, *revs[0].WorkspaceID)
	assert.Equal(t, domain.MemoryActionForgotten, revs[0].Action)
}

// TestMemoryRepoDB_FullForgetCycle_HistoryStaysReadableWithCorrectWorkspace
// runs the real end-to-end sequence this whole fix exists for: create a
// memory, forget it (snapshot then delete, matching memoryService.Forget's
// order), and confirm the revision history — including the 'forgotten' row —
// is still readable and still correctly attributed to its workspace, with no
// live `memories` row left to depend on.
func TestMemoryRepoDB_FullForgetCycle_HistoryStaysReadableWithCorrectWorkspace(t *testing.T) {
	repo, wsID := setupMemoryRevisionWorkspaceDBTest(t)
	ctx := context.Background()

	mem := &domain.Memory{
		ID:          uuid.New(),
		WorkspaceID: wsID,
		Key:         "full-forget-cycle-" + uuid.New().String()[:8],
		Content:     "sensitive prod detail",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
	}
	require.NoError(t, repo.Upsert(ctx, mem, domain.MemoryWriteIntent{Reason: "create"}))

	// Mirrors memory_service.Forget: snapshot BEFORE delete, memory's own
	// WorkspaceID is still in hand at this point.
	require.NoError(t, repo.AppendRevision(ctx, domain.MemoryRevision{
		MemoryID:    mem.ID,
		Version:     mem.Version + 1,
		Content:     mem.Content,
		Action:      domain.MemoryActionForgotten,
		Reason:      strPtr("cleanup"),
		WorkspaceID: &mem.WorkspaceID,
	}))
	require.NoError(t, repo.Delete(ctx, mem.ID))

	// The live row is gone — this is the exact state that used to 404 through
	// GetByID-based authorization.
	gone, err := repo.GetByID(ctx, mem.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "the memory must actually be gone for this test to mean anything")

	revs, err := repo.ListRevisions(ctx, mem.ID, 10)
	require.NoError(t, err)
	require.Len(t, revs, 2, "the created revision AND the forgotten revision must both survive")

	for _, r := range revs {
		require.NotNil(t, r.WorkspaceID, "workspace_id must survive the memory's own deletion")
		assert.Equal(t, wsID, *r.WorkspaceID)
	}

	var sawForgotten bool
	for _, r := range revs {
		if r.Action == domain.MemoryActionForgotten {
			sawForgotten = true
			assert.Equal(t, "sensitive prod detail", r.Content, "the forgotten snapshot must keep the real prior content")
		}
	}
	assert.True(t, sawForgotten, "the forgotten action must be in the history")
}
