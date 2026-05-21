package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

type notifyTestAgentSvc struct {
	agent *domain.Agent
	err   error
}

func (s *notifyTestAgentSvc) GetByID(_ context.Context, _ uuid.UUID) (*domain.Agent, error) {
	return s.agent, s.err
}
func (s *notifyTestAgentSvc) Register(_ context.Context, _ RegisterAgentInput) (*RegisterAgentOutput, error) {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) Update(_ context.Context, _ *domain.Agent) error { panic("not implemented") }
func (s *notifyTestAgentSvc) Delete(_ context.Context, _ uuid.UUID) error      { panic("not implemented") }
func (s *notifyTestAgentSvc) List(_ context.Context, _ uuid.UUID, _ repository.AgentFilter, _ pagination.Params) (*pagination.Page[domain.Agent], error) {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) Heartbeat(_ context.Context, _ uuid.UUID, _ *HeartbeatInput) error {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) Authenticate(_ context.Context, _, _ string) (*domain.Agent, error) {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) RotateAPIKey(_ context.Context, _ uuid.UUID) (string, error) {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) ListSubAgents(_ context.Context, _ uuid.UUID, _ bool) ([]domain.Agent, error) {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) CreateActivityLog(_ context.Context, _ *domain.AgentActivityLog) error {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) ListActivityLog(_ context.Context, _ uuid.UUID, _ repository.AgentActivityLogFilter, _ pagination.Params) (*pagination.Page[domain.AgentActivityLog], error) {
	panic("not implemented")
}
func (s *notifyTestAgentSvc) TouchLastSeen(_ context.Context, _ uuid.UUID) error { return nil }
func (s *notifyTestAgentSvc) GetBySlug(_ context.Context, _ uuid.UUID, _ string) (*domain.Agent, error) {
	panic("not implemented")
}

var _ AgentService = (*notifyTestAgentSvc)(nil)

type trackingEventsRepo struct {
	mu      sync.Mutex
	created []*domain.AgentEvent
	notify  chan struct{}
}

func newTrackingEventsRepo() *trackingEventsRepo {
	return &trackingEventsRepo{notify: make(chan struct{}, 1)}
}

func (r *trackingEventsRepo) Create(_ context.Context, ev *domain.AgentEvent) error {
	r.mu.Lock()
	r.created = append(r.created, ev)
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
	return nil
}
func (r *trackingEventsRepo) Lookup(_ context.Context, _ uuid.UUID) (*domain.AgentEvent, error) {
	return nil, nil
}
func (r *trackingEventsRepo) ListAfter(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ int) ([]domain.AgentEvent, error) {
	return nil, nil
}
func (r *trackingEventsRepo) DeleteExpired(_ context.Context) (int64, error) { return 0, nil }

var _ repository.AgentEventsRepository = (*trackingEventsRepo)(nil)

func TestSSEEventTTL_Critical(t *testing.T) {
	for _, et := range []string{"task.assigned", "task.created", "task.mentioned"} {
		t.Run(et, func(t *testing.T) {
			assert.Equal(t, 7*24*time.Hour, sseEventTTL(et))
		})
	}
}

func TestSSEEventTTL_NonCritical(t *testing.T) {
	for _, et := range []string{"task.status_changed", "task.commented", "task.updated", ""} {
		t.Run(et, func(t *testing.T) {
			assert.Equal(t, 24*time.Hour, sseEventTTL(et))
		})
	}
}

func newSvcMiniredis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func waitNotify(t *testing.T, repo *trackingEventsRepo) *domain.AgentEvent {
	t.Helper()
	select {
	case <-repo.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: agentEventsRepo.Create not called")
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.created[len(repo.created)-1]
}

func TestAgentNotify_CreateCalledOnce(t *testing.T) {
	rdb := newSvcMiniredis(t)
	agentID := uuid.New()
	evRepo := newTrackingEventsRepo()
	svc := NewAgentNotifyService(&notifyTestAgentSvc{agent: &domain.Agent{ID: agentID}}, rdb, evRepo)

	svc.NotifyAgent(context.Background(), agentID, AgentNotification{
		EventType:   "task.assigned",
		WorkspaceID: uuid.New(),
	})
	waitNotify(t, evRepo)
	evRepo.mu.Lock()
	n := len(evRepo.created)
	evRepo.mu.Unlock()
	assert.Equal(t, 1, n)
}

func TestAgentNotify_CriticalEvent_7dTTL(t *testing.T) {
	rdb := newSvcMiniredis(t)
	agentID := uuid.New()
	evRepo := newTrackingEventsRepo()
	svc := NewAgentNotifyService(&notifyTestAgentSvc{agent: &domain.Agent{ID: agentID}}, rdb, evRepo)

	svc.NotifyAgent(context.Background(), agentID, AgentNotification{
		EventType:   "task.assigned",
		WorkspaceID: uuid.New(),
	})
	ev := waitNotify(t, evRepo)
	got := ev.ExpiresAt.Sub(ev.EmittedAt)
	assert.InDelta(t, (7*24*time.Hour).Seconds(), got.Seconds(), 5)
}

func TestAgentNotify_NonCriticalEvent_24hTTL(t *testing.T) {
	rdb := newSvcMiniredis(t)
	agentID := uuid.New()
	evRepo := newTrackingEventsRepo()
	svc := NewAgentNotifyService(&notifyTestAgentSvc{agent: &domain.Agent{ID: agentID}}, rdb, evRepo)

	svc.NotifyAgent(context.Background(), agentID, AgentNotification{
		EventType:   "task.commented",
		WorkspaceID: uuid.New(),
	})
	ev := waitNotify(t, evRepo)
	got := ev.ExpiresAt.Sub(ev.EmittedAt)
	assert.InDelta(t, (24*time.Hour).Seconds(), got.Seconds(), 5)
}

func TestAgentNotify_NilRepo_NoPanic(t *testing.T) {
	rdb := newSvcMiniredis(t)
	agentID := uuid.New()
	svc := NewAgentNotifyService(&notifyTestAgentSvc{agent: &domain.Agent{ID: agentID}}, rdb, nil)

	done := make(chan struct{})
	go func() {
		svc.NotifyAgent(context.Background(), agentID, AgentNotification{
			EventType:   "task.assigned",
			WorkspaceID: uuid.New(),
		})
		time.Sleep(200 * time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}
}
