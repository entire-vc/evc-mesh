package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type mockSSEEventsRepo struct {
	lookupCalled  int
	LookupFunc    func(ctx context.Context, id uuid.UUID) (*domain.AgentEvent, error)
	ListAfterFunc func(ctx context.Context, agentID, lastID uuid.UUID, limit int) ([]domain.AgentEvent, error)
}

func (m *mockSSEEventsRepo) Create(_ context.Context, _ *domain.AgentEvent) error { return nil }
func (m *mockSSEEventsRepo) Lookup(ctx context.Context, id uuid.UUID) (*domain.AgentEvent, error) {
	m.lookupCalled++
	if m.LookupFunc != nil {
		return m.LookupFunc(ctx, id)
	}
	return nil, nil
}
func (m *mockSSEEventsRepo) ListAfter(ctx context.Context, a, b uuid.UUID, limit int) ([]domain.AgentEvent, error) {
	if m.ListAfterFunc != nil {
		return m.ListAfterFunc(ctx, a, b, limit)
	}
	return nil, nil
}
func (m *mockSSEEventsRepo) DeleteExpired(_ context.Context) (int64, error) { return 0, nil }

var _ repository.AgentEventsRepository = (*mockSSEEventsRepo)(nil)

func newSSEMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func newSSETestHandler(rdb *redis.Client, evRepo repository.AgentEventsRepository) *AgentHandler {
	return NewAgentHandlerWithEvents(&MockAgentService{}, nil, nil, rdb, evRepo)
}

func sseEchoCtx(t *testing.T, e *echo.Echo, agentID uuid.UUID, headers map[string]string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/agents/me/events/stream", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("agent_id", agentID)
	return c, rec
}

func sseEchoCtxTimeout(t *testing.T, e *echo.Echo, agentID uuid.UUID, headers map[string]string, d time.Duration) (echo.Context, *httptest.ResponseRecorder, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	req := httptest.NewRequest(http.MethodGet, "/agents/me/events/stream", nil).WithContext(ctx)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("agent_id", agentID)
	return c, rec, cancel
}

func TestEventStream_NoRDB_Returns501(t *testing.T) {
	e := echo.New()
	h := newSSETestHandler(nil, nil)
	c, rec := sseEchoCtx(t, e, uuid.New(), nil)
	require.NoError(t, h.EventStream(c))
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
}

func TestEventStream_InvalidCursor_Returns400(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	evRepo := &mockSSEEventsRepo{}
	e := echo.New()
	h := newSSETestHandler(rdb, evRepo)
	c, rec := sseEchoCtx(t, e, uuid.New(), map[string]string{"Last-Event-ID": "not-a-uuid"})
	require.NoError(t, h.EventStream(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Zero(t, evRepo.lookupCalled)
}

func TestEventStream_StaleCursor_Returns410(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	evRepo := &mockSSEEventsRepo{
		LookupFunc: func(_ context.Context, _ uuid.UUID) (*domain.AgentEvent, error) {
			return nil, nil
		},
	}
	e := echo.New()
	h := newSSETestHandler(rdb, evRepo)

	cursor, _ := uuid.NewV7()
	c, rec := sseEchoCtx(t, e, uuid.New(), map[string]string{"Last-Event-ID": cursor.String()})
	require.NoError(t, h.EventStream(c))
	assert.Equal(t, http.StatusGone, rec.Code)
	assert.NotContains(t, rec.Header().Get("Content-Type"), "text/event-stream")

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "cursor_expired", body["error"])
}

func TestEventStream_BackwardCompat_NoCursorLookup(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	evRepo := &mockSSEEventsRepo{}
	e := echo.New()
	h := newSSETestHandler(rdb, evRepo)

	agentID := uuid.New()
	c, rec, cancel := sseEchoCtxTimeout(t, e, agentID, nil, 60*time.Millisecond)
	defer cancel()

	_ = h.EventStream(c)

	assert.Zero(t, evRepo.lookupCalled, "Lookup must not be called when no Last-Event-ID")
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
}

func TestEventStream_ReplayEvents(t *testing.T) {
	_, rdb := newSSEMiniredis(t)

	cursor, _ := uuid.NewV7()
	time.Sleep(2 * time.Millisecond)
	missedID, _ := uuid.NewV7()
	agentID := uuid.New()

	payload, _ := json.Marshal(map[string]any{
		"event_id":   missedID.String(),
		"event_type": "task.assigned",
	})

	evRepo := &mockSSEEventsRepo{
		LookupFunc: func(_ context.Context, id uuid.UUID) (*domain.AgentEvent, error) {
			if id == cursor {
				return &domain.AgentEvent{EventID: cursor}, nil
			}
			return nil, nil
		},
		ListAfterFunc: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ int) ([]domain.AgentEvent, error) {
			return []domain.AgentEvent{
				{EventID: missedID, EventType: "task.assigned", Payload: payload},
			}, nil
		},
	}
	e := echo.New()
	h := newSSETestHandler(rdb, evRepo)

	c, rec, cancel := sseEchoCtxTimeout(t, e, agentID,
		map[string]string{"Last-Event-ID": cursor.String()}, 200*time.Millisecond)
	defer cancel()
	_ = h.EventStream(c)

	body := rec.Body.String()
	assert.Contains(t, body, "id: "+missedID.String())
	assert.Contains(t, body, "event: task.assigned")
}

func TestEventStream_Dedup_LiveAlreadyReplayed(t *testing.T) {
	_, rdb := newSSEMiniredis(t)

	cursor, _ := uuid.NewV7()
	time.Sleep(2 * time.Millisecond)
	replayID, _ := uuid.NewV7()
	agentID := uuid.New()

	payload, _ := json.Marshal(map[string]any{
		"event_id":   replayID.String(),
		"event_type": "task.assigned",
	})

	evRepo := &mockSSEEventsRepo{
		LookupFunc: func(_ context.Context, id uuid.UUID) (*domain.AgentEvent, error) {
			if id == cursor {
				return &domain.AgentEvent{EventID: cursor}, nil
			}
			return nil, nil
		},
		// ListAfter blocks briefly to let the goroutine publish to Redis first,
		// simulating the race where an event arrives in the Redis buffer during replay.
		ListAfterFunc: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ int) ([]domain.AgentEvent, error) {
			time.Sleep(20 * time.Millisecond)
			return []domain.AgentEvent{
				{EventID: replayID, EventType: "task.assigned", Payload: payload},
			}, nil
		},
	}
	e := echo.New()
	h := newSSETestHandler(rdb, evRepo)

	// Publish the same event to Redis while the handler is doing the DB replay.
	go func() {
		time.Sleep(5 * time.Millisecond) // let Subscribe() establish before publish
		_ = rdb.Publish(context.Background(), agentNotifyChannelPrefix+agentID.String(), string(payload))
	}()

	c, rec, cancel := sseEchoCtxTimeout(t, e, agentID,
		map[string]string{"Last-Event-ID": cursor.String()}, 400*time.Millisecond)
	defer cancel()
	_ = h.EventStream(c)

	// Each SSE frame contains the event_id twice: once in "id:" and once in data JSON.
	// Count only the "id:" prefix occurrences to detect duplicate frames.
	body := rec.Body.String()
	idLineCount := strings.Count(body, "id: "+replayID.String())
	assert.Equal(t, 1, idLineCount, "deduped event must produce exactly one id: line (not sent twice)")
}

func TestEventStream_E2E_MissedEventOnReconnect(t *testing.T) {
	_, rdb := newSSEMiniredis(t)

	agentID := uuid.New()
	cursorID, _ := uuid.NewV7()
	time.Sleep(2 * time.Millisecond)
	missedID, _ := uuid.NewV7()

	missedPayload, _ := json.Marshal(map[string]any{
		"event_id":   missedID.String(),
		"event_type": "task.assigned",
	})

	evRepo := &mockSSEEventsRepo{
		LookupFunc: func(_ context.Context, id uuid.UUID) (*domain.AgentEvent, error) {
			if id == cursorID {
				return &domain.AgentEvent{EventID: cursorID}, nil
			}
			return nil, nil
		},
		ListAfterFunc: func(_ context.Context, _ uuid.UUID, lastID uuid.UUID, _ int) ([]domain.AgentEvent, error) {
			if lastID == cursorID {
				return []domain.AgentEvent{
					{EventID: missedID, EventType: "task.assigned", Payload: missedPayload},
				}, nil
			}
			return nil, nil
		},
	}
	e := echo.New()
	h := newSSETestHandler(rdb, evRepo)

	c, rec, cancel := sseEchoCtxTimeout(t, e, agentID,
		map[string]string{"Last-Event-ID": cursorID.String()}, 400*time.Millisecond)
	defer cancel()
	_ = h.EventStream(c)

	body := rec.Body.String()
	scanner := bufio.NewScanner(strings.NewReader(body))
	var hasID, hasType bool
	for scanner.Scan() {
		line := scanner.Text()
		if line == "id: "+missedID.String() {
			hasID = true
		}
		if line == "event: task.assigned" {
			hasType = true
		}
	}
	assert.True(t, hasID, "missed event ID must appear after reconnect")
	assert.True(t, hasType, "missed event type must appear after reconnect")
}
