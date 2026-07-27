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

func TestBackfillChunks_OwnWorkspaceReturnsCount(t *testing.T) {
	ownWS := uuid.New()
	var gotWS uuid.UUID
	var gotLimit int
	ms := &MockMemoryService{
		BackfillChunksFunc: func(_ context.Context, workspaceID uuid.UUID, limit int) (int, error) {
			gotWS = workspaceID
			gotLimit = limit
			return 7, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/backfill-chunks?limit=50", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.BackfillChunks(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, ownWS, gotWS)
	assert.Equal(t, 50, gotLimit)
	assert.JSONEq(t, `{"chunked":7}`, rec.Body.String())
}

func TestBackfillChunks_NonNumericLimitFallsBackToZero(t *testing.T) {
	ownWS := uuid.New()
	var gotLimit int
	ms := &MockMemoryService{
		BackfillChunksFunc: func(_ context.Context, _ uuid.UUID, limit int) (int, error) {
			gotLimit = limit
			return 0, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/backfill-chunks?limit=not-a-number", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.BackfillChunks(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, gotLimit, "an unparseable limit must be dropped, not passed through as garbage")
}

func TestBackfillChunks_ForeignWorkspaceRejected(t *testing.T) {
	ownWS := uuid.New()
	foreignWS := uuid.New()
	called := false
	ms := &MockMemoryService{
		BackfillChunksFunc: func(context.Context, uuid.UUID, int) (int, error) {
			called = true
			return 0, nil
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/backfill-chunks?workspace_id="+foreignWS.String(), http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.BackfillChunks(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called, "the service must never be reached for a workspace the caller doesn't own")
}

func TestBackfillChunks_ServiceErrorPropagates(t *testing.T) {
	ownWS := uuid.New()
	ms := &MockMemoryService{
		BackfillChunksFunc: func(context.Context, uuid.UUID, int) (int, error) {
			return 0, errors.New("simulated db failure")
		},
	}
	h := NewMemoryHandler(ms, &mockWorkspaceMemberRepo{})

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories/backfill-chunks", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ownWS)

	require.NoError(t, h.BackfillChunks(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
