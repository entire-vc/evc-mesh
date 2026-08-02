package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The response carries BOTH numbers. `remaining` is what an operator's convergence loop
// reads; if the handler ever collapsed them into one field, a repair that selected nothing
// would be indistinguishable from one that fixed everything.
func TestRechunkStale_ReturnsProcessedAndRemainingSeparately(t *testing.T) {
	ownWS := uuid.New()
	var gotWS uuid.UUID
	var gotLimit int
	ms := &MockMemoryService{
		RechunkStaleFunc: func(_ context.Context, workspaceID uuid.UUID, limit int) (int, int, error) {
			gotWS = workspaceID
			gotLimit = limit
			return 100, 3160, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/rechunk-stale?limit=100", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.RechunkStale(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, ownWS, gotWS)
	assert.Equal(t, 100, gotLimit)
	assert.JSONEq(t, `{"rechunked":100,"remaining":3160}`, rec.Body.String())
}

func TestRechunkStale_NonNumericLimitFallsBackToZero(t *testing.T) {
	ownWS := uuid.New()
	var gotLimit int
	ms := &MockMemoryService{
		RechunkStaleFunc: func(_ context.Context, _ uuid.UUID, limit int) (int, int, error) {
			gotLimit = limit
			return 0, 0, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/rechunk-stale?limit=not-a-number", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.RechunkStale(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, gotLimit, "an unparseable limit must be dropped, not passed through as garbage")
}

// This endpoint re-embeds and rewrites chunk rows, so a workspace_id the caller does not own
// must be refused BEFORE the service is reached — memories carry no RLS policy, so
// requireWorkspaceID is the only tenant boundary in front of them.
func TestRechunkStale_ForeignWorkspaceRejectedBeforeAnyWrite(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()
	called := false
	ms := &MockMemoryService{
		RechunkStaleFunc: func(context.Context, uuid.UUID, int) (int, int, error) {
			called = true
			return 0, 0, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/rechunk-stale?workspace_id="+foreignWS.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.RechunkStale(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "a foreign workspace must never reach a path that rewrites embeddings")
}

// A failed repair must not answer 200 with a body an operator's loop would read. The danger
// shape is specifically `{"remaining":0}` on error — the one value that means "converged,
// stop calling me".
func TestRechunkStale_ServiceErrorDoesNotRenderRemainingZero(t *testing.T) {
	ownWS := uuid.New()
	ms := &MockMemoryService{
		RechunkStaleFunc: func(context.Context, uuid.UUID, int) (int, int, error) {
			return 0, 0, errors.New("simulated db failure")
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/rechunk-stale", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.RechunkStale(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"remaining":0`,
		"an error must not be rendered as a converged-looking body")
}
