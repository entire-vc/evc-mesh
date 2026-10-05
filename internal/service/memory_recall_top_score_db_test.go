package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// No //go:build integration tag, deliberately — same reasoning as
// memory_embedding_pending_db_test.go: CI's untagged `go test ./...` runs this
// against a migrated DATABASE_URL and skips when no database is reachable.
//
// The live stand for #2c44a087, kept as a ratchet. On a real Postgres with a
// real (stub-embedded) dense arm, a seeded record recalled by a MATCHING query
// and by an UNMATCHABLE query must produce different mesh_memory_recall_top_
// score observations on the same hybrid series — the prod-measured separation
// (both arms 1/61 ≈ 0.0164 vs dense-only 0.7/61 ≈ 0.0115). The unmatchable
// query still returns a NON-EMPTY page because the dense arm always has
// neighbours: that is Hugh's #4395a289 finding, the blindness the metric
// exists to remove — mesh_memory_recall_empty_total stays flat through both
// recalls while top_score tells them apart.

func TestRecall_TopScoreStand_SeededVsUnmatchable(t *testing.T) {
	db := embeddingPendingTestDB(t)
	ctx := context.Background()

	memRepo := postgres.NewMemoryRepo(db)
	edgeRepo := postgres.NewMemoryEdgesRepo(db)
	// Fixed-vector embedder: every text maps to the same vector, so the dense
	// arm ranks the seeded row first (cosine 1.0) for ANY query — including the
	// unmatchable one. What separates the two recalls is therefore the BM25 arm
	// alone, which is exactly the axis the metric measures: on hybrid, "dense
	// alone decided the page" vs "both arms agreed on the winner".
	svc := NewMemoryService(memRepo, edgeRepo, &stubEmbedder{vec: []float32{0.4, 0.5, 0.6, 0.7}})

	wsRepo := postgres.NewWorkspaceRepo(db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "top-score-stand-ws", Slug: "top-score-stand-" + uuid.New().String()[:8], OwnerID: uuid.New()}
	require.NoError(t, wsRepo.Create(ctx, ws))
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM memories WHERE workspace_id = $1", ws.ID)
		_, _ = db.ExecContext(context.Background(), "DELETE FROM workspaces WHERE id = $1", ws.ID)
	})

	// Globally unique tokens: nothing else in the shared CI database can match.
	token := "zorblat" + uuid.New().String()[:6]
	mem := &domain.Memory{
		WorkspaceID: ws.ID,
		Key:         "top-score-stand-" + token,
		Content:     token + " fenestrator quib seeded stand row for the top score metric",
		Scope:       domain.ScopeWorkspace,
		SourceType:  domain.SourceAgent,
		Tags:        []string{"kind:learning"},
	}
	_, err := svc.Remember(ctx, mem, domain.MemoryWriteIntent{})
	require.NoError(t, err)

	// Remember embeds asynchronously; wait until the row is dense-findable so
	// the stand is deterministic (same eventual-consistency dance as
	// TestRemember_EmbeddingPending_SignaledWhileGoroutineInFlight).
	queryVec := []float32{0.4, 0.5, 0.6, 0.7}
	require.Eventually(t, func() bool {
		rows, vecErr := memRepo.VectorSearch(ctx, queryVec, ws.ID, nil, domain.MemorySearchFilter{}, 20)
		return vecErr == nil && containsMemoryID(rows, mem.ID)
	}, 5*time.Second, 25*time.Millisecond, "seeded row never became dense-findable")

	opts := domain.RecallOpts{Query: token + " fenestrator quib", WorkspaceID: ws.ID, Limit: 10}

	hybridLe0012Before := recallTopScoreBelow(t, domain.SearchModeHybrid, 0.012)
	hybridLe0017Before := recallTopScoreBelow(t, domain.SearchModeHybrid, 0.017)

	// 1) Matching query: BM25 AND-matches the seeded row at rank 1 (unique
	//    tokens) and the dense arm ranks it first → top score 1/61 ≈ 0.0164.
	results, mode, err := svc.Recall(ctx, opts)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, domain.SearchModeHybrid, mode)
	assert.Equal(t, hybridLe0017Before+1, recallTopScoreBelow(t, domain.SearchModeHybrid, 0.017),
		"a both-arms rank-1 hit must land inside le=0.017 (1/61 ≈ 0.0164)")
	assert.Equal(t, hybridLe0012Before, recallTopScoreBelow(t, domain.SearchModeHybrid, 0.012),
		"a both-arms hit must NOT land in the dense-only bucket le=0.012")

	// 2) Unmatchable query: the tokens exist nowhere, BM25 returns nothing, but
	//    the dense arm still returns the seeded neighbour → a NON-EMPTY page at
	//    0.7/61. Non-empty yet useless — invisible to the empty counter.
	unmatchable := domain.RecallOpts{Query: "qqxxz wumpoid flurbopector", WorkspaceID: ws.ID, Limit: 10}
	results, mode, err = svc.Recall(ctx, unmatchable)
	require.NoError(t, err)
	require.NotEmpty(t, results, "the dense arm must still return a neighbour — this page is NOT empty")
	require.Equal(t, domain.SearchModeHybrid, mode)
	assert.Equal(t, hybridLe0012Before+1, recallTopScoreBelow(t, domain.SearchModeHybrid, 0.012),
		"a dense-arm-only page must land inside le=0.012 (0.7/61 ≈ 0.0115)")

	// 3) bm25-only series: the same matching query served by BM25 alone tops at
	//    0.3/61 ≈ 0.0049 — a disjoint scale, which is why the metric carries
	//    search_mode as a label.
	bm25Svc := NewMemoryService(memRepo, edgeRepo, embedding.NewNoopEmbedder())
	bm25Le0005Before := recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.005)
	results, mode, err = bm25Svc.Recall(ctx, opts)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, domain.SearchModeBM25Only, mode)
	assert.Equal(t, bm25Le0005Before+1, recallTopScoreBelow(t, domain.SearchModeBM25Only, 0.005),
		"a bm25-only rank-1 hit tops at 0.3/61 ≈ 0.0049 — inside le=0.005")

	// 4) Exportability: /metrics (the same default registry the API serves)
	//    must carry both modes' series with the decision buckets present.
	srv := httptest.NewServer(promhttp.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	// Presence only, deliberately: the registry is process-global, so exact
	// counts depend on which package tests ran first — the exact deltas are
	// asserted via dto in steps 1-3 above. Label order is the vec's declaration
	// order (search_mode) with the histogram's le appended.
	for _, want := range []string{
		`mesh_memory_recall_top_score_bucket{search_mode="hybrid",le="0.012"}`,
		`mesh_memory_recall_top_score_bucket{search_mode="hybrid",le="0.017"}`,
		`mesh_memory_recall_top_score_bucket{search_mode="bm25-only",le="0.005"}`,
		`mesh_memory_recall_top_score_count{search_mode="hybrid"}`,
		`mesh_memory_recall_top_score_count{search_mode="bm25-only"}`,
	} {
		require.True(t, strings.Contains(string(body), want), "/metrics must expose %s", want)
	}
	t.Log("── /metrics series for mesh_memory_recall_top_score ──")
	for _, ln := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(ln, "mesh_memory_recall_top_score") {
			t.Log(ln)
		}
	}
}
