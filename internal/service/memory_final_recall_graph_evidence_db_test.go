package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func TestFinalRecall_GraphRequiresBodyEvidence(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(fmt.Sprintf("graph=%v", graph), func(t *testing.T) {
			db := versionTestDB(t)
			ws := versionTestWorkspace(t, db)
			repo := postgres.NewMemoryRepo(db)
			edges := postgres.NewMemoryEdgesRepo(db)
			// No embedding provider: exercise the real sparse-only fallback and graph.
			svc := NewMemoryService(repo, edges, nil).(FinalRecallService)
			ctx := context.Background()
			ids := make([]uuid.UUID, 3)
			for i, content := range []string{
				"The user owns a canine breed from the kennel.",
				"User: I picked up a Weimaraner from the kennel.\nAssistant: Congratulations!",
				"User: The user picked up a Weimaraner from the kennel.",
			} {
				m := &domain.Memory{ID: uuid.New(), WorkspaceID: ws.ID, Key: fmt.Sprintf("node-%d", i),
					Content: content, Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent,
					Relevance: 0.4, ImportanceScore: 0.8}
				require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
				ids[i] = m.ID
			}
			for i, weight := range []float32{1, 0.8} {
				require.NoError(t, edges.UpsertEdge(ctx, &domain.MemoryEdge{WorkspaceID: ws.ID,
					MemoryFromID: ids[0], MemoryToID: ids[i+1], RelationshipType: domain.EdgeRelatesTo, Weight: weight}))
			}
			res, err := svc.FinalRecall(ctx, domain.FinalRecallOpts{RecallOpts: domain.RecallOpts{
				WorkspaceID: ws.ID, Query: "canine breed user", Limit: 4}, Graph: graph})
			require.NoError(t, err)
			require.Zero(t, res.Stats.DenseRows)
			require.Equal(t, 1, res.Stats.SparseRows, "positive control: the sparse seed must be found")
			want := []uuid.UUID{ids[0]}
			if graph {
				want = append(want, ids[2])
				require.Equal(t, 1, res.GraphBoostCount)
			}
			got := make([]uuid.UUID, 0, len(res.Items))
			for _, item := range res.Items {
				got = append(got, item.ID)
				if item.ID == ids[2] {
					require.Equal(t, domain.ProvenanceGraph, item.Provenance)
				}
			}
			require.Equal(t, want, got, "a transcript role alone must not admit a graph neighbour")
			for i, id := range ids {
				stored, err := repo.GetByID(ctx, id)
				require.NoError(t, err)
				if i == 0 || (graph && i == 2) {
					require.NotNil(t, stored.LastAccessedAt)
					require.InDelta(t, 0.5, stored.Relevance, 0.00001)
				} else {
					require.Nil(t, stored.LastAccessedAt)
					require.InDelta(t, 0.4, stored.Relevance, 0.00001)
				}
			}
		})
	}
}
