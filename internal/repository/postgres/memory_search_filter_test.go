//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// Both Recall arms enforce scope/tags in SQL (task #2c087b2a)
//
// The service-level tests prove the filter is PASSED to both arms. These prove the
// SQL each arm builds from it actually restricts rows — mock tests cannot, because a
// mock arm returns whatever the test hands it regardless of the predicates.
//
// Pre-fix, FullTextSearchRanked took no filter at all and VectorSearch built
// `tags && …` (OR) for the AND-semantics Tags field.
// ---------------------------------------------------------------------------

// filterTestWorkspace inserts a workspace so the memories FK is satisfied, and registers
// cleanup of it plus every memory row that hangs off it.
func filterTestWorkspace(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	wsID := uuid.New()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
		 VALUES ($1, 'FilterTest WS', $2, $3, '{}', NOW(), NOW())`,
		wsID, "ft-ws-"+wsID.String()[:8], uuid.New(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.ExecContext(ctx, `DELETE FROM memories WHERE workspace_id = $1`, wsID)
		_, _ = db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = $1`, wsID)
	})
	return wsID
}

func TestSearchFilter_BothArms_EnforceScopeAndTags(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryRepo(db)
	ctx := context.Background()

	wsID := filterTestWorkspace(t, db)
	marker := "zqxjfilterprobe" // distinctive token so FTS matches only our rows

	type seed struct {
		scope string
		tags  []string
		id    uuid.UUID
	}
	seeds := []*seed{
		{scope: "agent", tags: []string{"kind:decision", "project:mesh"}},
		{scope: "agent", tags: []string{"kind:decision"}},
		{scope: "workspace", tags: []string{"kind:decision", "project:mesh"}},
		{scope: "project", tags: []string{"pavel-decision"}},
	}

	for _, s := range seeds {
		s.id = uuid.New()
		_, err := db.ExecContext(ctx, `
			INSERT INTO memories (id, workspace_id, key, content, scope, tags, status,
			                      relevance, importance_score, freshness_score,
			                      archived, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'active', 1.0, 0.8, 1.0, false, NOW(), NOW())`,
			s.id, wsID, "k-"+s.id.String()[:8], marker+" content body", s.scope, pq.Array(s.tags),
		)
		require.NoError(t, err)
	}
	idsOf := func(rows []domain.ScoredMemory) map[uuid.UUID]bool {
		out := make(map[uuid.UUID]bool, len(rows))
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}

	t.Run("bm25 arm enforces scope", func(t *testing.T) {
		rows, err := repo.FullTextSearchRanked(ctx, wsID, nil, marker,
			domain.MemorySearchFilter{Scope: "agent"}, 50)
		require.NoError(t, err)
		require.NotEmpty(t, rows, "FTS must match the seeded marker token")

		for _, r := range rows {
			assert.Equal(t, domain.MemoryScope("agent"), r.Scope,
				"BM25 arm returned a %s-scoped row under scope=agent", r.Scope)
		}
		got := idsOf(rows)
		assert.True(t, got[seeds[0].id])
		assert.True(t, got[seeds[1].id])
		assert.False(t, got[seeds[2].id], "workspace-scoped row leaked through the BM25 arm")
		assert.False(t, got[seeds[3].id], "project-scoped row leaked through the BM25 arm")
	})

	t.Run("bm25 arm treats Tags as AND", func(t *testing.T) {
		rows, err := repo.FullTextSearchRanked(ctx, wsID, nil, marker,
			domain.MemorySearchFilter{Tags: []string{"kind:decision", "project:mesh"}}, 50)
		require.NoError(t, err)

		got := idsOf(rows)
		assert.True(t, got[seeds[0].id], "row carrying both tags must match")
		assert.True(t, got[seeds[2].id], "row carrying both tags must match")
		assert.False(t, got[seeds[1].id],
			"Tags is AND: a row with only one of the two required tags must not match")
	})

	t.Run("bm25 arm treats TagsAny as OR", func(t *testing.T) {
		rows, err := repo.FullTextSearchRanked(ctx, wsID, nil, marker,
			domain.MemorySearchFilter{TagsAny: []string{"pavel-decision", "project:mesh"}}, 50)
		require.NoError(t, err)

		got := idsOf(rows)
		assert.True(t, got[seeds[0].id])
		assert.True(t, got[seeds[2].id])
		assert.True(t, got[seeds[3].id], "row carrying one of the tags_any values must match")
		assert.False(t, got[seeds[1].id], "row carrying none of the tags_any values must not match")
	})

	t.Run("scope and tags compose", func(t *testing.T) {
		rows, err := repo.FullTextSearchRanked(ctx, wsID, nil, marker,
			domain.MemorySearchFilter{
				Scope:   "agent",
				TagsAny: []string{"project:mesh"},
			}, 50)
		require.NoError(t, err)

		got := idsOf(rows)
		assert.True(t, got[seeds[0].id], "agent-scoped row with project:mesh must match")
		assert.False(t, got[seeds[1].id], "agent-scoped row without project:mesh must not match")
		assert.False(t, got[seeds[2].id], "project:mesh row in the wrong scope must not match")
	})
}

// TestSearchFilter_VectorArm_EnforcesScopeAndAndTags covers the dense arm's SQL. It seeds
// embeddings so the rows are eligible for VectorSearch (which requires embedding IS NOT NULL).
func TestSearchFilter_VectorArm_EnforcesScopeAndAndTags(t *testing.T) {
	db := testDB(t)
	repo := NewMemoryRepo(db)
	ctx := context.Background()

	wsID := filterTestWorkspace(t, db)
	embedding := `[1.0,0.0,0.0]`

	type seed struct {
		scope string
		tags  []string
		id    uuid.UUID
	}
	seeds := []*seed{
		{scope: "agent", tags: []string{"kind:decision", "project:mesh"}},
		{scope: "agent", tags: []string{"kind:decision"}},
		{scope: "workspace", tags: []string{"kind:decision", "project:mesh"}},
	}

	for _, s := range seeds {
		s.id = uuid.New()
		_, err := db.ExecContext(ctx, `
			INSERT INTO memories (id, workspace_id, key, content, scope, tags, status,
			                      relevance, importance_score, freshness_score, archived,
			                      embedding, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'active', 1.0, 0.8, 1.0, false, $7, NOW(), NOW())`,
			s.id, wsID, "kv-"+s.id.String()[:8], "vector filter probe", s.scope,
			pq.Array(s.tags), embedding,
		)
		require.NoError(t, err)
	}
	queryVec := []float32{1, 0, 0}

	t.Run("scope enforced", func(t *testing.T) {
		rows, err := repo.VectorSearch(ctx, queryVec, wsID, nil,
			domain.MemorySearchFilter{Scope: "agent"}, 50)
		require.NoError(t, err)
		for _, r := range rows {
			assert.Equal(t, domain.MemoryScope("agent"), r.Scope)
		}
	})

	t.Run("Tags is AND not overlap", func(t *testing.T) {
		rows, err := repo.VectorSearch(ctx, queryVec, wsID, nil,
			domain.MemorySearchFilter{Tags: []string{"kind:decision", "project:mesh"}}, 50)
		require.NoError(t, err)

		found := map[uuid.UUID]bool{}
		for _, r := range rows {
			found[r.ID] = true
		}
		assert.True(t, found[seeds[0].id])
		assert.True(t, found[seeds[2].id])
		assert.False(t, found[seeds[1].id],
			"pre-fix this arm used `tags && …` (OR) and wrongly matched the single-tag row")
	})
}
