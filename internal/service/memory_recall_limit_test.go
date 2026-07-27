package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ---------------------------------------------------------------------------
// `limit` is a bound, not a hint (task #4c65d3e2)
//
// The defect: pinned rows were prepended AFTER the trim, so Recall returned
// limit+len(eligible pinned) rows while the response still echoed the requested
// limit. The caller had no way to notice — it asked for 10, got 12, and nothing
// in the payload said so.
//
// The invariant asserted here is deliberately not a specific count: the corpus is
// live and the number of pinned rows moves. It is len(results) <= opts.Limit, for
// every limit, with and without pinned rows in play.
// ---------------------------------------------------------------------------

// pinnedMem builds a kind:pinned memory that is eligible for an unfiltered recall.
func pinnedMem(wsID uuid.UUID) domain.Memory {
	return memWithScope(wsID, domain.ScopeWorkspace, domain.KindPinned)
}

// retrievalRows builds n distinct rows for an arm to return, descending in score
// so the ranking is deterministic.
func retrievalRows(wsID uuid.UUID, n int) []domain.ScoredMemory {
	rows := make([]domain.ScoredMemory, 0, n)
	for i := 0; i < n; i++ {
		m := memWithScope(wsID, domain.ScopeWorkspace)
		m.Key = fmt.Sprintf("retrieved-%02d", i)
		rows = append(rows, domain.ScoredMemory{Memory: m, Score: 1.0 - float64(i)/1000.0})
	}
	return rows
}

// TestRecall_PinnedInjection_RespectsLimit is the regression test for the defect.
// Pre-fix this fails at every limit: the arm returns a full page, then every
// eligible pinned row is appended on top of it.
func TestRecall_PinnedInjection_RespectsLimit(t *testing.T) {
	for _, limit := range []int{1, 6, 10, 40} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			wsID := uuid.New()

			// The arm returns a full page on its own, so there is no slack for
			// pinned rows to occupy without displacing something.
			armRows := retrievalRows(wsID, limit)

			// More pinned rows than the limit, to prove the trim covers them too.
			pinned := make([]domain.Memory, 0, limit+3)
			for i := 0; i < limit+3; i++ {
				pinned = append(pinned, pinnedMem(wsID))
			}

			repo := &mockMemoryRepo{
				fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
					return armRows, nil
				},
				findPinnedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
					return pinned, nil
				},
			}
			svc := NewMemoryService(repo, nil, nil)

			results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
				Query:       "probe",
				WorkspaceID: wsID,
				Limit:       limit,
			})
			require.NoError(t, err)

			assert.LessOrEqual(t, len(results), limit,
				"recall(limit=%d) returned %d rows — pinned injection escaped the trim",
				limit, len(results))
		})
	}
}

// TestRecall_PinnedRowsDisplaceWeakestHit pins down WHICH rows survive, not just
// how many. Pinned outranks retrieval (that is what pinned means), so a pinned row
// must take the slot of the weakest hit rather than be dropped by the trim — a fix
// that simply truncated the tail would satisfy the count assertion above while
// silently making pinned rows unreachable on a full page.
func TestRecall_PinnedRowsDisplaceWeakestHit(t *testing.T) {
	wsID := uuid.New()
	const limit = 5

	armRows := retrievalRows(wsID, limit)
	weakestHitID := armRows[len(armRows)-1].ID
	strongestHitID := armRows[0].ID

	pin := pinnedMem(wsID)

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return armRows, nil
		},
		findPinnedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
			return []domain.Memory{pin}, nil
		},
	}
	svc := NewMemoryService(repo, nil, nil)

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "probe",
		WorkspaceID: wsID,
		Limit:       limit,
	})
	require.NoError(t, err)
	require.Len(t, results, limit)

	ids := make(map[uuid.UUID]bool, len(results))
	for _, r := range results {
		ids[r.ID] = true
	}

	assert.True(t, ids[pin.ID], "the pinned row was trimmed away instead of displacing a hit")
	assert.True(t, ids[strongestHitID], "the best retrieval hit was dropped")
	assert.False(t, ids[weakestHitID], "the weakest hit should have been displaced by the pinned row")
}

// TestRecall_NoPinned_StillFillsLimit guards the other direction: the reordering
// must not cost a caller rows when there is nothing pinned to inject.
func TestRecall_NoPinned_StillFillsLimit(t *testing.T) {
	wsID := uuid.New()
	const limit = 8

	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return retrievalRows(wsID, limit*2), nil
		},
		findPinnedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
			return nil, nil
		},
	}
	svc := NewMemoryService(repo, nil, nil)

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "probe",
		WorkspaceID: wsID,
		Limit:       limit,
	})
	require.NoError(t, err)
	assert.Len(t, results, limit, "a full page should still be served when nothing is pinned")
}

// TestRecall_BoostRelevance_OnlyCoversReturnedRows checks the side effect that moved
// with the trim. BoostRelevance is positive feedback for rows the caller actually
// saw; boosting the pre-trim set would promote rows that were never returned.
func TestRecall_BoostRelevance_OnlyCoversReturnedRows(t *testing.T) {
	wsID := uuid.New()
	const limit = 3

	var boosted []uuid.UUID
	repo := &mockMemoryRepo{
		fullTextSearchRankedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ string, _ domain.MemorySearchFilter, _ int) ([]domain.ScoredMemory, error) {
			return retrievalRows(wsID, limit*4), nil
		},
		findPinnedFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]domain.Memory, error) {
			return nil, nil
		},
		boostRelevanceFn: func(_ context.Context, ids []uuid.UUID) error {
			boosted = append([]uuid.UUID(nil), ids...)
			return nil
		},
	}
	svc := NewMemoryService(repo, nil, nil)

	results, _, err := svc.Recall(context.Background(), domain.RecallOpts{
		Query:       "probe",
		WorkspaceID: wsID,
		Limit:       limit,
	})
	require.NoError(t, err)
	require.Len(t, results, limit)

	assert.Len(t, boosted, limit,
		"BoostRelevance saw %d rows for a %d-row page — it is being fed the pre-trim set",
		len(boosted), limit)

	returned := make(map[uuid.UUID]bool, len(results))
	for _, r := range results {
		returned[r.ID] = true
	}
	for _, id := range boosted {
		assert.True(t, returned[id], "boosted a row that was never returned to the caller")
	}
}
