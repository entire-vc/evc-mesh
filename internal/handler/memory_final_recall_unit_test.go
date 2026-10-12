package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type failingFinalRecallService struct {
	*MockMemoryService
	err error
}

func (s failingFinalRecallService) FinalRecall(context.Context, domain.FinalRecallOpts) (*domain.FinalRecallResult, error) {
	return nil, s.err
}

func finalRecallUnitRequest(t *testing.T, h *MemoryHandler) *httptest.ResponseRecorder {
	t.Helper()
	ws := uuid.New()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/recall-final/v1?q=cobalt&limit=5", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), ws)
	require.NoError(t, h.FinalRecall(c))
	return rec
}

// A service without the capability must fail closed with 503, never fall back to legacy search.
func TestFinalRecall_ServiceWithoutCapabilityFailsClosed(t *testing.T) {
	h := NewMemoryHandler(&MockMemoryService{}, &mockWorkspaceMemberRepo{})
	require.Equal(t, http.StatusServiceUnavailable, finalRecallUnitRequest(t, h).Code)
}

func TestFinalRecall_ServiceErrorIsMapped(t *testing.T) {
	h := NewMemoryHandler(failingFinalRecallService{&MockMemoryService{}, apierror.BadRequest("bad window")}, &mockWorkspaceMemberRepo{})
	require.Equal(t, http.StatusBadRequest, finalRecallUnitRequest(t, h).Code)

	h = NewMemoryHandler(failingFinalRecallService{&MockMemoryService{}, errors.New("boom")}, &mockWorkspaceMemberRepo{})
	require.Equal(t, http.StatusInternalServerError, finalRecallUnitRequest(t, h).Code)
}

type okFinalRecallService struct {
	*MockMemoryService
	got domain.FinalRecallOpts
}

func (s *okFinalRecallService) FinalRecall(_ context.Context, o domain.FinalRecallOpts) (*domain.FinalRecallResult, error) {
	s.got = o
	return &domain.FinalRecallResult{Items: []domain.FinalRecallItem{}, CandidateLimit: 10, GraphBoostCount: 2}, nil
}

func finalRecallQuery(t *testing.T, svc service.MemoryService, query string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/memories/recall-final/v1?q=cobalt&"+query, http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, uuid.New(), uuid.New())
	require.NoError(t, NewMemoryHandler(svc, &mockWorkspaceMemberRepo{}).FinalRecall(c))
	return rec
}

// Every malformed parameter is rejected before the service is reached.
func TestFinalRecall_RejectsMalformedParams(t *testing.T) {
	svc := &okFinalRecallService{MockMemoryService: &MockMemoryService{}}
	for _, q := range []string{
		"limit=0", "limit=51", "limit=x", "offset=-1", "offset=151", "offset=x",
		"scope=galaxy", "graph=maybe", "include_archived=maybe", "include_expired=maybe",
		"min_importance=2", "min_importance=-0.1", "min_importance=NaN", "relevance_min=Inf",
	} {
		rec := finalRecallQuery(t, svc, q)
		require.Equal(t, http.StatusBadRequest, rec.Code, q)
	}
}

func TestFinalRecall_PassesParsedOptionsAndEnvelope(t *testing.T) {
	svc := &okFinalRecallService{MockMemoryService: &MockMemoryService{}}
	rec := finalRecallQuery(t, svc, "limit=7&offset=3&graph=true&scope=workspace&include_archived=true&include_expired=true&exclude_superseded=true&apply_recency_decay=true")
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, svc.got.Graph)
	require.Equal(t, 7, svc.got.Limit)
	require.Equal(t, 3, svc.got.Offset)
	require.True(t, svc.got.IncludeArchived)
	require.True(t, svc.got.IncludeExpired)
	require.True(t, svc.got.ExcludeSuperseded)
	require.True(t, svc.got.ApplyDecay)
	require.Contains(t, rec.Body.String(), `"final_recall_version":1`)
	require.Contains(t, rec.Body.String(), `"graph_boost_count":2`)
}
