package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Regression cover for #655c6d12: RecallWithStats armed time decay from
// `opts.OrderBy == "decayed_relevance"` — the bare key — while every producer in
// the tree emits the suffixed "decayed_relevance:desc". The branch could not fire,
// and nothing said so: the one caller that asks for this ordering (the MCP temporal
// profile) also sets ApplyDecay=true, so the intended behaviour occurred for the
// wrong reason and the dead branch stayed masked.
//
// The observable is ScoredMemory.RecencyScore, which the service writes from the
// same `applyTimeDecay` decision: exp(-Δt·ln2/half_life) when armed, exactly 1.0
// when not. Asserting on it rather than on the unexported local keeps the test
// coupled to the behaviour instead of the implementation.

// recallOneAged runs a recall over a single memory of the given age and returns
// the RecencyScore the service stamped on it.
func recallOneAged(t *testing.T, ageDays float64, opts domain.RecallOpts) float64 {
	t.Helper()
	aged := []domain.ScoredMemory{{
		Memory: domain.Memory{
			ID:      uuid.New(),
			Key:     "aged-memory",
			Content: "decision recorded a while ago",
			// Above the service's default MinImportance floor (0.4) — below it the
			// row is filtered out before the decay step and every assertion here
			// becomes vacuous. The require.Len in the caller is what surfaces that.
			ImportanceScore: 0.8,
			CreatedAt:       time.Now().Add(-time.Duration(ageDays*24) * time.Hour),
		},
		Score: 1.0,
	}}
	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return aged, nil
		},
	}
	opts.Query = "decision"
	opts.WorkspaceID = uuid.New()
	opts.Limit = 10

	scored, _, err := newMemoryService(repo).Recall(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, scored, 1, "fixture must survive filtering, or the assertion below is vacuous")
	return scored[0].RecencyScore
}

// TestRecall_OrderByDecayedRelevanceArmsDecay is THE case the dead branch was
// supposed to cover: order_by alone, ApplyDecay explicitly false. On the pre-fix
// code every suffixed sub-case returns 1.0.
func TestRecall_OrderByDecayedRelevanceArmsDecay(t *testing.T) {
	const ageDays = 90

	for _, orderBy := range []string{
		domain.OrderByDecayedRelevanceDesc, // the form every producer actually emits
		"decayed_relevance",                // the bare key the old compare expected — must keep working
	} {
		t.Run(orderBy, func(t *testing.T) {
			got := recallOneAged(t, ageDays, domain.RecallOpts{
				OrderBy:    orderBy,
				ApplyDecay: false, // load-bearing: decay must come from OrderBy alone
			})
			assert.Less(t, got, 1.0,
				"order_by=%q must arm time decay on its own; RecencyScore=1.0 means the branch did not fire", orderBy)
		})
	}
}

// TestRecall_NonDecayOrderByLeavesScoresUntouched is the discriminating control.
// Without it the test above passes on a service that decays unconditionally — which
// would be a worse bug than the one being fixed, and invisible to a one-armed test.
func TestRecall_NonDecayOrderByLeavesScoresUntouched(t *testing.T) {
	const ageDays = 90

	for _, orderBy := range []string{
		domain.OrderByRelevanceDesc,
		domain.OrderByCreatedAtDesc,
		domain.OrderByCreatedAtAsc,
		"",                                 // no ordering requested at all
		"decayed_relevance_something_else", // near-miss: must NOT be treated as the real thing
	} {
		t.Run("orderBy="+orderBy, func(t *testing.T) {
			got := recallOneAged(t, ageDays, domain.RecallOpts{
				OrderBy:    orderBy,
				ApplyDecay: false,
			})
			assert.Equal(t, 1.0, got,
				"order_by=%q must not arm time decay", orderBy)
		})
	}
}

// TestRecall_ApplyDecayStillArmsDecayWithoutOrderBy pins the other half of the OR.
// Today this is the ONLY arm that fires in production, so a fix that accidentally
// made decay depend on OrderBy would silently disable it fleet-wide.
func TestRecall_ApplyDecayStillArmsDecayWithoutOrderBy(t *testing.T) {
	got := recallOneAged(t, 90, domain.RecallOpts{
		OrderBy:    "",
		ApplyDecay: true,
	})
	assert.Less(t, got, 1.0, "ApplyDecay=true must arm decay regardless of OrderBy")
}
