package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestRecallCandidatesByIDs_TenantScopedAndReadFree(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	wsA, wsB := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{wsA, wsB} {
		_, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug, owner_id, settings) VALUES ($1, $2, $2, $3, '{}')`, id, "recall-cand-"+id.String(), uuid.New())
		require.NoError(t, err)
		id := id
		t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", id) })
	}
	repo := NewMemoryRepo(db)
	mem := func(ws uuid.UUID, key string) *domain.Memory {
		m := &domain.Memory{ID: uuid.New(), WorkspaceID: ws, Key: key, Content: "c", Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.5}
		require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
		return m
	}
	a, b := mem(wsA, "cand-a"), mem(wsB, "cand-b")

	none, err := repo.RecallCandidatesByIDs(ctx, wsA, nil)
	require.NoError(t, err)
	require.Empty(t, none)

	rows, err := repo.RecallCandidatesByIDs(ctx, wsA, []uuid.UUID{a.ID, b.ID})
	require.NoError(t, err)
	require.Len(t, rows, 1, "a foreign workspace ID is never hydrated")
	require.Equal(t, a.ID, rows[0].ID)

	var touched int
	require.NoError(t, db.Get(&touched, `SELECT count(*) FROM memories WHERE id = $1 AND last_accessed_at IS NOT NULL`, a.ID))
	require.Zero(t, touched, "hydrating graph candidates is not a read")

	tooMany := make([]uuid.UUID, 201)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}
	_, err = repo.RecallCandidatesByIDs(ctx, wsA, tooMany)
	require.Error(t, err)
}
