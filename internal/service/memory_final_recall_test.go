package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// candidateReaderRepo adds the optional side-effect-free reader to the shared
// memory-repo fake, the way the postgres repo does in production.
type candidateReaderRepo struct {
	*mockMemoryRepo
	byID map[uuid.UUID]domain.Memory
	err  error
}

func (r *candidateReaderRepo) RecallCandidatesByIDs(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]domain.Memory, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := []domain.Memory{}
	for _, id := range ids {
		if m, ok := r.byID[id]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func finalRow(ws, id uuid.UUID, key, content string, score float64) domain.ScoredMemory {
	return domain.ScoredMemory{
		Memory: domain.Memory{
			ID: id, WorkspaceID: ws, Key: key, Content: content,
			ImportanceScore: 0.9, Scope: domain.ScopeWorkspace,
		},
		Score: score,
	}
}

func finalOpts(ws uuid.UUID, query string, limit int, graph bool) domain.FinalRecallOpts {
	return domain.FinalRecallOpts{
		RecallOpts: domain.RecallOpts{WorkspaceID: ws, Query: query, Limit: limit},
		Graph:      graph,
	}
}

func TestFinalRecall_RejectsOutOfRangePage(t *testing.T) {
	svc := NewMemoryService(&mockMemoryRepo{}, nil, nil).(FinalRecallService)
	ws := uuid.New()
	for name, o := range map[string]domain.FinalRecallOpts{
		"limit zero":      finalOpts(ws, "q", 0, false),
		"limit too large": finalOpts(ws, "q", 51, false),
		"offset negative": {RecallOpts: domain.RecallOpts{WorkspaceID: ws, Query: "q", Limit: 5, Offset: -1}},
		"offset too far":  {RecallOpts: domain.RecallOpts{WorkspaceID: ws, Query: "q", Limit: 5, Offset: 151}},
	} {
		_, err := svc.FinalRecall(context.Background(), o)
		require.Error(t, err, name)
		var ae *apierror.Error
		require.True(t, errors.As(err, &ae), name)
		assert.Equal(t, http.StatusBadRequest, ae.Code, name)
	}
}

func TestFinalRecall_EmptyResultIsNonNilSlice(t *testing.T) {
	repo := &mockMemoryRepo{fullTextSearchRankedFn: func(context.Context, uuid.UUID, *uuid.UUID, string, domain.MemorySearchFilter, int) ([]domain.ScoredMemory, error) {
		return nil, nil
	}}
	svc := NewMemoryService(repo, nil, nil).(FinalRecallService)
	res, err := svc.FinalRecall(context.Background(), finalOpts(uuid.New(), "nothing here", 5, false))
	require.NoError(t, err)
	require.NotNil(t, res.Items)
	assert.Empty(t, res.Items)
	assert.Equal(t, 10, res.CandidateLimit, "candidate budget is max(10, limit+offset), independent of page size")
}

func TestFinalRecall_ReturnsPageAndRecordsReadFeedback(t *testing.T) {
	ws := uuid.New()
	a, b := uuid.New(), uuid.New()
	var boosted, touched []uuid.UUID
	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(context.Context, uuid.UUID, *uuid.UUID, string, domain.MemorySearchFilter, int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{
				finalRow(ws, a, "deploy-notes", "deploy checklist for prometheus", 0.5),
				finalRow(ws, b, "other", "prometheus scrape config", 0.4),
			}, nil
		},
		boostRelevanceFn: func(_ context.Context, ids []uuid.UUID) error { boosted = ids; return nil },
		touchAccessedFn:  func(_ context.Context, ids []uuid.UUID) error { touched = ids; return nil },
	}
	svc := NewMemoryService(repo, nil, nil).(FinalRecallService)
	res, err := svc.FinalRecall(context.Background(), finalOpts(ws, "prometheus", 1, false))
	require.NoError(t, err)
	require.Len(t, res.Items, 1, "page is cut to limit")
	assert.Equal(t, domain.ProvenanceRecall, res.Items[0].Provenance)
	assert.Equal(t, []uuid.UUID{res.Items[0].ID}, boosted)
	assert.Equal(t, boosted, touched)
	assert.Zero(t, res.GraphBoostCount)
}

func TestFinalRecall_GraphAdmitsNeighbourWithQueryEvidence(t *testing.T) {
	ws := uuid.New()
	seed, withEvidence, noEvidence := uuid.New(), uuid.New(), uuid.New()
	repo := &candidateReaderRepo{
		mockMemoryRepo: &mockMemoryRepo{fullTextSearchRankedFn: func(context.Context, uuid.UUID, *uuid.UUID, string, domain.MemorySearchFilter, int) ([]domain.ScoredMemory, error) {
			return []domain.ScoredMemory{finalRow(ws, seed, "seed", "prometheus alert rules", 0.5)}, nil
		}},
		byID: map[uuid.UUID]domain.Memory{
			withEvidence: finalRow(ws, withEvidence, "n1", "prometheus retention", 0).Memory,
			noEvidence:   finalRow(ws, noEvidence, "n2", "unrelated gardening", 0).Memory,
		},
	}
	edges := &testEdgeRepo{getNeighborsFn: func(_ context.Context, _ []uuid.UUID, _ float64, _ int) ([]domain.MemoryEdge, error) {
		return []domain.MemoryEdge{graphEdge(seed, withEvidence, 0.8), graphEdge(noEvidence, seed, 0.9)}, nil
	}}
	svc := NewMemoryService(repo, edges, nil).(FinalRecallService)
	res, err := svc.FinalRecall(context.Background(), finalOpts(ws, "prometheus", 4, true))
	require.NoError(t, err)
	ids := []uuid.UUID{}
	for _, it := range res.Items {
		ids = append(ids, it.ID)
	}
	assert.Contains(t, ids, seed)
	assert.Contains(t, ids, withEvidence, "neighbour with its own query evidence is admitted")
	assert.NotContains(t, ids, noEvidence, "an edge alone must not admit a neighbour")
	assert.Equal(t, 1, res.GraphBoostCount)
	for _, it := range res.Items {
		if it.ID == withEvidence {
			assert.Equal(t, domain.ProvenanceGraph, it.Provenance)
			assert.Equal(t, 1, it.HopDistance)
			assert.Greater(t, it.CompositeScore, 0.0)
		}
	}
}

func TestFinalRecall_GraphWithoutCandidateReaderIs503(t *testing.T) {
	ws := uuid.New()
	repo := &mockMemoryRepo{fullTextSearchRankedFn: func(context.Context, uuid.UUID, *uuid.UUID, string, domain.MemorySearchFilter, int) ([]domain.ScoredMemory, error) {
		return []domain.ScoredMemory{finalRow(ws, uuid.New(), "k", "prometheus", 0.5)}, nil
	}}
	svc := NewMemoryService(repo, &testEdgeRepo{}, nil).(FinalRecallService)
	_, err := svc.FinalRecall(context.Background(), finalOpts(ws, "prometheus", 4, true))
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae))
	assert.Equal(t, http.StatusServiceUnavailable, ae.Code)
}

func TestFinalRecall_GraphErrorsPropagate(t *testing.T) {
	ws := uuid.New()
	seed := uuid.New()
	ranked := func(context.Context, uuid.UUID, *uuid.UUID, string, domain.MemorySearchFilter, int) ([]domain.ScoredMemory, error) {
		return []domain.ScoredMemory{finalRow(ws, seed, "k", "prometheus", 0.5)}, nil
	}
	boom := errors.New("boom")

	edgeErr := &testEdgeRepo{getNeighborsFn: func(context.Context, []uuid.UUID, float64, int) ([]domain.MemoryEdge, error) { return nil, boom }}
	repo := &candidateReaderRepo{mockMemoryRepo: &mockMemoryRepo{fullTextSearchRankedFn: ranked}}
	_, err := NewMemoryService(repo, edgeErr, nil).(FinalRecallService).FinalRecall(context.Background(), finalOpts(ws, "prometheus", 4, true))
	require.ErrorIs(t, err, boom)

	n := uuid.New()
	edgeOK := &testEdgeRepo{getNeighborsFn: func(context.Context, []uuid.UUID, float64, int) ([]domain.MemoryEdge, error) {
		return []domain.MemoryEdge{graphEdge(seed, n, 0.9)}, nil
	}}
	repo = &candidateReaderRepo{mockMemoryRepo: &mockMemoryRepo{fullTextSearchRankedFn: ranked}, err: boom}
	_, err = NewMemoryService(repo, edgeOK, nil).(FinalRecallService).FinalRecall(context.Background(), finalOpts(ws, "prometheus", 4, true))
	require.ErrorIs(t, err, boom)
}

func TestFinalEligible_Filters(t *testing.T) {
	ws, proj, otherProj := uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	mk := func(mut func(*domain.ScoredMemory)) domain.ScoredMemory {
		r := finalRow(ws, uuid.New(), "k", "c", 1)
		mut(&r)
		return r
	}
	dup := finalRow(ws, uuid.New(), "dup", "c", 1)
	rows := []domain.ScoredMemory{
		mk(func(r *domain.ScoredMemory) { r.ProjectID = &proj }),                             // keep
		mk(func(r *domain.ScoredMemory) { r.ProjectID = &otherProj }),                        // other project
		mk(func(r *domain.ScoredMemory) {}),                                                  // workspace row, not pinned
		mk(func(r *domain.ScoredMemory) { r.Tags = []string{domain.KindPinned} }),            // pinned workspace row: keep
		mk(func(r *domain.ScoredMemory) { r.WorkspaceID = uuid.New(); r.ProjectID = &proj }), // other workspace
		mk(func(r *domain.ScoredMemory) { r.ID = uuid.Nil; r.ProjectID = &proj }),            // nil id
		mk(func(r *domain.ScoredMemory) { r.Archived = true; r.ProjectID = &proj }),          // archived
		mk(func(r *domain.ScoredMemory) { r.ExpiresAt = &past; r.ProjectID = &proj }),        // expired
		mk(func(r *domain.ScoredMemory) { r.ExpiresAt = &future; r.ProjectID = &proj }),      // not yet expired: keep
		dup, dup,
	}
	dupWithProj := dup
	dupWithProj.ProjectID = &proj
	rows[len(rows)-2], rows[len(rows)-1] = dupWithProj, dupWithProj

	got := finalEligible(rows, domain.RecallOpts{WorkspaceID: ws, ProjectID: proj}, now)
	assert.Len(t, got, 4, "kept: project row, pinned workspace row, unexpired row, one of the duplicates")

	all := finalEligible([]domain.ScoredMemory{mk(func(r *domain.ScoredMemory) { r.Archived = true; r.ExpiresAt = &past })},
		domain.RecallOpts{WorkspaceID: ws, IncludeArchived: true, IncludeExpired: true}, now)
	assert.Len(t, all, 1, "include flags keep archived and expired rows")
}

func TestFinalRerank_KeyTiersThenScoreBandThenDense(t *testing.T) {
	ws := uuid.New()
	exact := finalRow(ws, uuid.New(), "prometheus", "x", 0.1)
	contains := finalRow(ws, uuid.New(), "prometheus-notes", "x", 0.2)
	shared := finalRow(ws, uuid.New(), "alpha_bravo_charlie_delta", "x", 0.3)
	plain := finalRow(ws, uuid.New(), "zzz", "x", 0.9)

	rows := finalRerank([]domain.ScoredMemory{plain, shared, contains, exact}, domain.RecallOpts{Query: "prometheus"})
	assert.Equal(t, exact.ID, rows[0].ID, "exact key match first")
	assert.Equal(t, contains.ID, rows[1].ID, "substring key second")
	assert.Equal(t, plain.ID, rows[2].ID, "untiered rows follow, ordered by score")

	// inside one score band, higher importance wins
	lo := finalRow(ws, uuid.New(), "qq-a", "x", 0.5000)
	hi := finalRow(ws, uuid.New(), "qq-b", "x", 0.4995)
	hi.ImportanceScore = 1
	lo.ImportanceScore = 0.2
	out := finalRerank([]domain.ScoredMemory{lo, hi}, domain.RecallOpts{Query: "nomatch"})
	assert.Equal(t, hi.ID, out[0].ID)

	// dense cosine reorders sparse-ranked rows of the same tier
	d1, d2 := finalRow(ws, uuid.New(), "qq-c", "x", 0.9), finalRow(ws, uuid.New(), "qq-d", "x", 0.1)
	v1, v2 := 0.2, 0.8
	d1.DenseScore, d2.DenseScore = &v1, &v2
	dense := finalRerank([]domain.ScoredMemory{d1, d2}, domain.RecallOpts{Query: "nomatch"})
	assert.Equal(t, d2.ID, dense[0].ID)

	// project and scope each add a tier point
	proj := uuid.New()
	p := finalRow(ws, uuid.New(), "qq-e", "x", 0.1)
	p.ProjectID = &proj
	q := finalRow(ws, uuid.New(), "qq-f", "x", 0.9)
	scoped := finalRerank([]domain.ScoredMemory{q, p}, domain.RecallOpts{Query: "nomatch", ProjectID: proj, Scope: domain.ScopeWorkspace})
	assert.Equal(t, p.ID, scoped[0].ID)
	_ = shared
}

func TestFinalRerank_ThreeSharedKeyTokensEarnATier(t *testing.T) {
	ws := uuid.New()
	shared := finalRow(ws, uuid.New(), "alpha-bravo-charlie-zzz", "x", 0.1)
	plain := finalRow(ws, uuid.New(), "zzz", "x", 0.9)
	out := finalRerank([]domain.ScoredMemory{plain, shared}, domain.RecallOpts{Query: "alpha bravo charlie question"})
	assert.Equal(t, shared.ID, out[0].ID)
}

func TestFinalQueryOverlap_TokenRules(t *testing.T) {
	ws := uuid.New()
	row := finalRow(ws, uuid.New(), "deploy-notes", "Prometheus scrape", 1)
	row.Tags = []string{"observability"}
	assert.True(t, finalQueryOverlap("a b", nil), "no token of 4+ letters means nothing to disprove")
	assert.True(t, finalQueryOverlap("prometheus?", []domain.ScoredMemory{row}))
	assert.True(t, finalQueryOverlap("observability", []domain.ScoredMemory{row}), "tags count as evidence")
	assert.False(t, finalQueryOverlap("kubernetes", []domain.ScoredMemory{row}))
	assert.False(t, finalQueryOverlap("kubernetes", nil))
}
