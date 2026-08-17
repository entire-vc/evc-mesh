package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// activityMockRepo is a minimal AgentRepository for activity tracker tests.
type activityMockRepo struct {
	mu      sync.Mutex
	touched [][]uuid.UUID
}

func (m *activityMockRepo) TouchLastSeenBatch(_ context.Context, ids []uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]uuid.UUID, len(ids))
	copy(cp, ids)
	m.touched = append(m.touched, cp)
	return nil
}

// -- All other AgentRepository methods are no-ops for these tests. --

func (m *activityMockRepo) Create(_ context.Context, _ *domain.Agent) error { return nil }
func (m *activityMockRepo) GetByID(_ context.Context, _ uuid.UUID) (*domain.Agent, error) {
	return nil, nil
}
func (m *activityMockRepo) GetByAPIKeyPrefix(_ context.Context, _ uuid.UUID, _ string) (*domain.Agent, error) {
	return nil, nil
}
func (m *activityMockRepo) SetAPIKeySHA256(_ context.Context, _ uuid.UUID, _, _ string) error {
	return nil
}
func (m *activityMockRepo) Update(_ context.Context, _ *domain.Agent) error { return nil }
func (m *activityMockRepo) Delete(_ context.Context, _ uuid.UUID) error     { return nil }
func (m *activityMockRepo) UpdateHeartbeat(_ context.Context, _ uuid.UUID, _ *repository.UpdateHeartbeatParams) error {
	return nil
}
func (m *activityMockRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ domain.AgentStatus) error {
	return nil
}
func (m *activityMockRepo) List(_ context.Context, _ uuid.UUID, _ repository.AgentFilter, _ pagination.Params) (*pagination.Page[domain.Agent], error) {
	return &pagination.Page[domain.Agent]{}, nil
}
func (m *activityMockRepo) GetSubAgentTree(_ context.Context, _ uuid.UUID) ([]domain.Agent, error) {
	return nil, nil
}
func (m *activityMockRepo) ListWithProjects(_ context.Context, _ uuid.UUID) ([]repository.AgentWithProjects, error) {
	return nil, nil
}
func (m *activityMockRepo) GetAgentActivityLog(_ context.Context, _ uuid.UUID) ([]json.RawMessage, error) {
	return nil, nil
}
func (m *activityMockRepo) GetAgentByID(_ context.Context, _ uuid.UUID) (*domain.Agent, error) {
	return nil, nil
}
func (m *activityMockRepo) GetBySlug(_ context.Context, _ uuid.UUID, _ string) (*domain.Agent, error) {
	return nil, nil
}
func (m *activityMockRepo) SearchByPrefix(_ context.Context, _ uuid.UUID, _ string, _ int) ([]domain.Agent, error) {
	return nil, nil
}

// Ensure compile-time interface compliance.
var _ repository.AgentRepository = (*activityMockRepo)(nil)

func newTestEchoAgent(e *echo.Echo, agentID uuid.UUID) echo.Context {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)
	return c
}

func TestActivityTracker_AgentRequestMarkedDirty(t *testing.T) {
	mock := &activityMockRepo{}
	tracker := NewActivityTracker(mock)
	mwFunc := tracker.Middleware()

	e := echo.New()
	agentID := uuid.New()

	called := false
	handler := mwFunc(func(c echo.Context) error {
		called = true
		return nil
	})
	require.NoError(t, handler(newTestEchoAgent(e, agentID)))
	assert.True(t, called)

	tracker.Flush(context.Background())

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Len(t, mock.touched, 1)
	assert.Equal(t, []uuid.UUID{agentID}, mock.touched[0])
}

func TestActivityTracker_NonAgentRequestIgnored(t *testing.T) {
	mock := &activityMockRepo{}
	tracker := NewActivityTracker(mock)
	mwFunc := tracker.Middleware()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyAuthType, AuthTypeUser)

	_ = mwFunc(func(c echo.Context) error { return nil })(c)

	tracker.Flush(context.Background())

	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Empty(t, mock.touched)
}

func TestActivityTracker_DeduplicatesWithinWindow(t *testing.T) {
	mock := &activityMockRepo{}
	tracker := NewActivityTracker(mock)
	mwFunc := tracker.Middleware()

	e := echo.New()
	agentID := uuid.New()

	for range 5 {
		_ = mwFunc(func(c echo.Context) error { return nil })(newTestEchoAgent(e, agentID))
	}

	tracker.Flush(context.Background())

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Len(t, mock.touched, 1)
	assert.Len(t, mock.touched[0], 1)
	assert.Equal(t, agentID, mock.touched[0][0])
}

func TestActivityTracker_FlushEmptySet(t *testing.T) {
	mock := &activityMockRepo{}
	tracker := NewActivityTracker(mock)
	// Flush with nothing dirty — no repo call.
	tracker.Flush(context.Background())
	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Empty(t, mock.touched)
}

// unused — prevents import error for time
var _ = time.Second
