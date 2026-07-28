package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The search envelope must carry the per-arm row counts, not just the mode.
//
// `search_mode: hybrid` means the dense arm RAN — it does not mean the arm
// matched anything. When the chunked-embed write path left every new memory with
// a NULL embedding, VectorSearch matched zero rows corpus-wide and this endpoint
// answered `search_mode: hybrid`, `degraded: false` on a recall served entirely
// by BM25. The CI recall gate read that as its healthiest run ever.
//
// `dense_rows` is what makes the two distinguishable over the wire, which is the
// only place the gate can see them from.

func searchEnvelope(t *testing.T, stats domain.RecallStats) map[string]any {
	t.Helper()

	ms := &MockMemoryService{
		RecallStatsFunc: func(_ context.Context, _ domain.RecallOpts) ([]domain.ScoredMemory, domain.RecallStats, error) {
			return []domain.ScoredMemory{}, stats, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/search?q=probe", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), uuid.New())

	require.NoError(t, h.Search(c))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var env map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	return env
}

// THE CASE THIS EXISTS FOR: a healthy-looking envelope over a dead dense arm.
func TestSearch_Envelope_HybridWithZeroDenseRows(t *testing.T) {
	env := searchEnvelope(t, domain.RecallStats{
		Mode:       domain.SearchModeHybrid,
		DenseRows:  0,
		SparseRows: 12,
	})

	// Everything the old envelope said still reads healthy…
	assert.Equal(t, "hybrid", env["search_mode"])
	assert.Equal(t, false, env["degraded"])
	// …and the new field is the only thing that contradicts it.
	require.Contains(t, env, "dense_rows")
	assert.Equal(t, float64(0), env["dense_rows"])
	assert.Equal(t, float64(12), env["sparse_rows"])
}

// The positive control: a working dense arm reports a non-zero count. A
// dense_rows hard-wired to 0 would pass the test above and be worse than useless.
func TestSearch_Envelope_HybridWithDenseRows(t *testing.T) {
	env := searchEnvelope(t, domain.RecallStats{
		Mode:       domain.SearchModeHybrid,
		DenseRows:  30,
		SparseRows: 12,
	})

	assert.Equal(t, "hybrid", env["search_mode"])
	assert.Equal(t, float64(30), env["dense_rows"])
}

// bm25-only: same zero count, different mode. The gate rule is (hybrid AND 0),
// because a deployment with no embedder configured is legitimate, not an alert.
func TestSearch_Envelope_BM25OnlyReportsZeroDenseRows(t *testing.T) {
	env := searchEnvelope(t, domain.RecallStats{
		Mode:       domain.SearchModeBM25Only,
		DenseRows:  0,
		SparseRows: 12,
	})

	assert.Equal(t, "bm25-only", env["search_mode"])
	assert.Equal(t, true, env["degraded"])
	assert.Equal(t, float64(0), env["dense_rows"])
}

// Additive, in the same sense search_mode/degraded were: the pre-existing keys
// keep their exact names, types and meaning, so no client breaks by upgrading.
func TestSearch_Envelope_NewFieldsAreAdditive(t *testing.T) {
	env := searchEnvelope(t, domain.RecallStats{Mode: domain.SearchModeHybrid, DenseRows: 7, SparseRows: 3})

	for _, k := range []string{"items", "total", "limit", "offset", "search_mode", "degraded"} {
		assert.Contains(t, env, k, "pre-existing envelope key %q must survive", k)
	}
	assert.Equal(t, float64(0), env["total"])
	assert.Equal(t, float64(0), env["offset"])
}
