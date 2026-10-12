package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// mock: external boundary — the embedding provider's network response is fixed;
// service, graph traversal, SQL ranking and read feedback all run for real.
type finalRecallTestEmbedder struct{}

func (finalRecallTestEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}
func (finalRecallTestEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (finalRecallTestEmbedder) Model() string   { return "final-recall-test" }
func (finalRecallTestEmbedder) Dimensions() int { return 3 }

type finalRecallSvcEnv struct {
	svc   FinalRecallService
	repo  *postgres.MemoryRepo
	ws    uuid.UUID
	seeds []uuid.UUID
	graph []uuid.UUID
	db    *sqlx.DB
}

func newFinalRecallSvcEnv(t *testing.T) *finalRecallSvcEnv {
	t.Helper()
	db := versionTestDB(t)
	ws := versionTestWorkspace(t, db)
	repo := postgres.NewMemoryRepo(db)
	edges := postgres.NewMemoryEdgesRepo(db)
	svc := NewMemoryService(repo, edges, finalRecallTestEmbedder{}).(FinalRecallService)
	env := &finalRecallSvcEnv{svc: svc, repo: repo, ws: ws.ID, db: db}
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		m := &domain.Memory{ID: uuid.New(), WorkspaceID: ws.ID, Key: fmt.Sprintf("cobalt-base-%02d", i), Content: "cobalt checkout base answer", Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.8, Tags: pq.StringArray{"allowed"}}
		require.NoError(t, repo.Upsert(ctx, m, domain.MemoryWriteIntent{}))
		require.NoError(t, repo.UpdateEmbedding(ctx, m.ID, []float32{1, 0, 0}, "final-recall-test", 3))
		env.seeds = append(env.seeds, m.ID)
	}
	for _, name := range []string{"one", "two"} {
		n := &domain.Memory{ID: uuid.New(), WorkspaceID: ws.ID, Key: "graph-cobalt-" + name, Content: "cobalt neighbour answer", Scope: domain.ScopeWorkspace, SourceType: domain.SourceAgent, Relevance: 0.4, ImportanceScore: 0.8, Tags: pq.StringArray{"allowed"}}
		require.NoError(t, repo.Upsert(ctx, n, domain.MemoryWriteIntent{}))
		env.graph = append(env.graph, n.ID)
		for _, seed := range env.seeds {
			require.NoError(t, edges.UpsertEdge(ctx, &domain.MemoryEdge{WorkspaceID: ws.ID, MemoryFromID: seed, MemoryToID: n.ID, RelationshipType: domain.EdgeRelatesTo, Weight: 0.9}))
		}
	}
	return env
}

func (e *finalRecallSvcEnv) opts(limit, offset int, graph bool) domain.FinalRecallOpts {
	return domain.FinalRecallOpts{RecallOpts: domain.RecallOpts{WorkspaceID: e.ws, Query: "cobalt checkout", Limit: limit, Offset: offset}, Graph: graph}
}

func (e *finalRecallSvcEnv) touched(t *testing.T) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	require.NoError(t, e.db.Select(&ids, `SELECT id FROM memories WHERE workspace_id=$1 AND last_accessed_at IS NOT NULL`, e.ws))
	return ids
}

func TestFinalRecall_FeedbackOnlyForReturnedPage(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(fmt.Sprintf("graph=%v", graph), func(t *testing.T) {
			env := newFinalRecallSvcEnv(t)
			res, err := env.svc.FinalRecall(context.Background(), env.opts(5, 0, graph))
			require.NoError(t, err)
			require.Len(t, res.Items, 5)
			require.GreaterOrEqual(t, res.CandidateLimit, 10)
			final := []uuid.UUID{}
			for _, item := range res.Items {
				final = append(final, item.ID)
			}
			require.ElementsMatch(t, final, env.touched(t), "touched IDs must equal the returned page")
			if graph {
				require.Positive(t, res.GraphBoostCount, "graph neighbour with query overlap is admitted")
			} else {
				require.Zero(t, res.GraphBoostCount)
			}
		})
	}
}

func TestFinalRecall_OffsetPageAndEmptyResultGetNoUnrelatedFeedback(t *testing.T) {
	env := newFinalRecallSvcEnv(t)
	res, err := env.svc.FinalRecall(context.Background(), env.opts(3, 3, false))
	require.NoError(t, err)
	require.Len(t, res.Items, 3)
	first := map[uuid.UUID]bool{}
	for _, item := range res.Items {
		first[item.ID] = true
	}
	require.Len(t, env.touched(t), 3)

	empty := env.opts(5, 0, true)
	empty.Query = "zzzzqqqq unmatched"
	empty.Tags = []string{"no-such-tag"}
	before := len(env.touched(t))
	res, err = env.svc.FinalRecall(context.Background(), empty)
	require.NoError(t, err)
	require.Empty(t, res.Items)
	require.NotNil(t, res.Items)
	require.Equal(t, before, len(env.touched(t)), "an empty page writes no feedback")
}

func TestFinalRecall_RejectsOutOfBoundsPaging(t *testing.T) {
	env := newFinalRecallSvcEnv(t)
	for _, c := range [][2]int{{0, 0}, {51, 0}, {5, -1}, {5, 151}} {
		_, err := env.svc.FinalRecall(context.Background(), env.opts(c[0], c[1], false))
		require.Error(t, err, "limit=%d offset=%d", c[0], c[1])
	}
	require.Empty(t, env.touched(t))
}

func TestFinalRecall_GraphSkipsExpiredAndForeignNeighbours(t *testing.T) {
	env := newFinalRecallSvcEnv(t)
	_, err := env.db.Exec(`UPDATE memories SET expires_at = NOW() - interval '1 hour' WHERE id = $1`, env.graph[0])
	require.NoError(t, err)
	res, err := env.svc.FinalRecall(context.Background(), env.opts(5, 0, true))
	require.NoError(t, err)
	for _, item := range res.Items {
		require.NotEqual(t, env.graph[0], item.ID, "expired neighbour is not admitted")
		require.Equal(t, env.ws, item.WorkspaceID)
	}

	other := newFinalRecallSvcEnv(t)
	foreign := other.opts(5, 0, true)
	foreign.WorkspaceID = env.ws
	res, err = other.svc.FinalRecall(context.Background(), foreign)
	require.NoError(t, err)
	for _, item := range res.Items {
		require.Equal(t, env.ws, item.WorkspaceID)
	}
}

func TestFinalRerankAndEligibility(t *testing.T) {
	ws, project := uuid.New(), uuid.New()
	now := time.Now()
	mk := func(key string, score float64, imp float64) domain.ScoredMemory {
		return domain.ScoredMemory{Memory: domain.Memory{ID: uuid.New(), WorkspaceID: ws, Key: key, ImportanceScore: float32(imp), Scope: domain.ScopeWorkspace}, Score: score}
	}
	exact := mk("deploy-runbook", 0.001, 0.1)
	partial := mk("deploy-runbook-notes", 0.002, 0.1)
	lowA, lowB := mk("alpha", 0.0100, 0.2), mk("beta", 0.0095, 0.9)
	rows := finalRerank([]domain.ScoredMemory{lowA, lowB, partial, exact}, domain.RecallOpts{Query: "deploy-runbook"})
	require.Equal(t, exact.ID, rows[0].ID, "exact key tier first")
	require.Equal(t, partial.ID, rows[1].ID, "containing key tier second")
	require.Equal(t, lowB.ID, rows[2].ID, "within the score band higher importance wins")

	dense := func(key string, cos float64) domain.ScoredMemory {
		row := mk(key, 0.01, 0.5)
		row.DenseScore = &cos
		return row
	}
	lo, hi := dense("x1", 0.2), dense("x2", 0.9)
	rows = finalRerank([]domain.ScoredMemory{lo, hi}, domain.RecallOpts{Query: "unrelated"})
	require.Equal(t, hi.ID, rows[0].ID, "dense rows rerank by raw cosine inside a tier")

	inProject := mk("p", 0.1, 0.5)
	inProject.ProjectID = &project
	otherProj := uuid.New()
	foreignProject := mk("q", 0.1, 0.5)
	foreignProject.ProjectID = &otherProj
	archived := mk("r", 0.1, 0.5)
	archived.Archived = true
	expired := mk("s", 0.1, 0.5)
	past := now.Add(-time.Minute)
	expired.ExpiresAt = &past
	otherWS := mk("t", 0.1, 0.5)
	otherWS.WorkspaceID = uuid.New()
	nilID := mk("u", 0.1, 0.5)
	nilID.ID = uuid.Nil
	unscoped := mk("v", 0.1, 0.5)
	pinned := mk("w", 0.1, 0.5)
	pinned.Tags = pq.StringArray{domain.KindPinned}
	opts := domain.RecallOpts{WorkspaceID: ws, ProjectID: project}
	got := finalEligible([]domain.ScoredMemory{inProject, foreignProject, archived, expired, otherWS, nilID, unscoped, pinned, inProject}, opts, now)
	ids := []uuid.UUID{}
	for _, row := range got {
		ids = append(ids, row.ID)
	}
	require.Equal(t, []uuid.UUID{inProject.ID, pinned.ID}, ids)

	all := finalEligible([]domain.ScoredMemory{archived, expired}, domain.RecallOpts{WorkspaceID: ws, IncludeArchived: true, IncludeExpired: true}, now)
	require.Len(t, all, 2)
}

func TestFinalQueryOverlap(t *testing.T) {
	row := domain.ScoredMemory{Memory: domain.Memory{Key: "cobalt-checkout", Content: "base answer", Tags: pq.StringArray{"x"}}}
	require.True(t, finalQueryOverlap("cobalt", []domain.ScoredMemory{row}))
	require.True(t, finalQueryOverlap("ab", []domain.ScoredMemory{row}), "no long tokens means no evidence to require")
	require.False(t, finalQueryOverlap("zebra", []domain.ScoredMemory{row}))
	require.False(t, finalQueryOverlap("zebra", nil))
}

// Known-bad control for TestFinalRecall_FeedbackOnlyForReturnedPage: the
// premature-feedback variant (feedback on at the candidate stage, before the
// output limit) leaves more rows touched than the five returned. The
// regression assertion `touched == page` must therefore FAIL for it; if this
// control ever stops seeing the extra rows, the regression test above has
// gone blind.
func TestFinalRecall_PrematureFeedbackVariantViolatesInvariant(t *testing.T) {
	env := newFinalRecallSvcEnv(t)
	svc, ok := env.svc.(*memoryService)
	require.True(t, ok, "control needs the concrete service to turn feedback on early")
	opts := env.opts(5, 0, false).RecallOpts
	opts.Limit = 10 // candidate budget, wider than the output page
	rows, _, err := svc.recallCandidates(context.Background(), opts, true)
	require.NoError(t, err)
	page := rows
	if len(page) > 5 {
		page = page[:5]
	}
	pageIDs := make([]uuid.UUID, 0, len(page))
	for _, row := range page {
		pageIDs = append(pageIDs, row.ID)
	}
	require.NotEmpty(t, pageIDs)
	require.Greater(t, len(env.touched(t)), len(pageIDs),
		"premature feedback must touch rows outside the final page, i.e. violate touched == page")
	require.NotElementsMatch(t, pageIDs, env.touched(t))
}
