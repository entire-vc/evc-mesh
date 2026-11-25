package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Exercise the ordinary service against SQL, including saved feedback. Only the
// external embedding provider is fixed (finalRecallTestEmbedder).
func TestFinalRecall_RecencyDecay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decay    bool
		order    string
		halfLife float64
		wantOld  bool
	}{
		{name: "default-off", wantOld: true},
		{name: "explicit-false-with-short-half-life", halfLife: 1, wantOld: true},
		{name: "enabled-default-half-life", decay: true},
		{name: "enabled-custom-half-life", decay: true, halfLife: 30},
		{name: "order-alone", order: "decayed_relevance:desc", halfLife: 30},
		{name: "long-half-life-retains-semantic-winner", decay: true, halfLife: 100000, wantOld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, offset := range []int{0, 1, 2} {
				t.Run(string(rune('0'+offset)), func(t *testing.T) {
					db := versionTestDB(t)
					ws := versionTestWorkspace(t, db)
					repo := postgres.NewMemoryRepo(db)
					svc := NewMemoryService(repo, postgres.NewMemoryEdgesRepo(db), finalRecallTestEmbedder{})
					ctx := context.Background()
					ids := make([]uuid.UUID, 3)
					for i, cosine := range []float64{0.90, 0.89, 0.88} {
						m := &domain.Memory{ID: uuid.New(), WorkspaceID: ws.ID,
							Key:     []string{"answer-old", "answer-new", "answer-neighbour"}[i],
							Content: "cobalt checkout answer", Scope: domain.ScopeWorkspace,
							SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.8}
						require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
						require.NoError(t, repo.UpdateEmbedding(ctx, m.ID,
							[]float32{float32(cosine), float32(math.Sqrt(1 - cosine*cosine)), 0}, "final-recall-test", 3))
						age := 1
						if i == 0 {
							age = 180
						}
						_, err := db.Exec(`UPDATE memories SET created_at=NOW()-($2 * interval '1 day') WHERE id=$1`, m.ID, age)
						require.NoError(t, err)
						stored, err := repo.GetByID(ctx, m.ID)
						require.NoError(t, err)
						require.WithinDuration(t, time.Now().Add(-time.Duration(age)*24*time.Hour), stored.CreatedAt, time.Minute)
						ids[i] = m.ID
					}
					want := []uuid.UUID{ids[1], ids[2], ids[0]}
					if tc.wantOld {
						want = ids
					}
					opts := domain.RecallOpts{WorkspaceID: ws.ID, Query: "cobalt checkout", Limit: 1,
						Offset: offset, ApplyDecay: tc.decay, OrderBy: tc.order, HalfLifeDays: tc.halfLife}
					res, err := svc.(FinalRecallService).FinalRecall(ctx, domain.FinalRecallOpts{RecallOpts: opts})
					require.NoError(t, err)
					require.Len(t, res.Items, 1)
					require.Equal(t, want[offset], res.Items[0].ID, "final page must preserve requested decay and half-life")
					for _, id := range ids {
						stored, err := repo.GetByID(ctx, id)
						require.NoError(t, err)
						if id == want[offset] {
							require.NotNil(t, stored.LastAccessedAt)
							require.InDelta(t, 0.5, stored.Relevance, 0.00001)
						} else {
							require.Nil(t, stored.LastAccessedAt, "unreturned candidates get no feedback")
							require.InDelta(t, 0.4, stored.Relevance, 0.00001)
						}
					}
				})
			}
		})
	}
}
