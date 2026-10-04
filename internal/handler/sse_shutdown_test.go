package handler

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// TestEventStream_ServerShutdown_NotBlockedByOpenSSE reproduces the deploy
// downtime: http.Server.Shutdown waits for in-flight handlers, and an open SSE
// stream never returns on its own. Closing the shutdown signal must end the
// stream so Shutdown completes in well under its deadline.
func TestEventStream_ServerShutdown_NotBlockedByOpenSSE(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	h := newSSETestHandler(rdb, &mockSSEEventsRepo{})
	shutdownCh := make(chan struct{})
	h.SetShutdownSignal(shutdownCh)

	e := echo.New()
	e.GET("/stream", func(c echo.Context) error {
		c.Set("agent_id", uuid.New())
		return h.EventStream(c)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	e.Listener = ln
	go func() { _ = e.Start("") }()
	t.Cleanup(func() { _ = e.Close() })

	resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Same order as cmd/api/main.go: signal first, then e.Shutdown.
	start := time.Now()
	close(shutdownCh)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, e.Shutdown(ctx), "Shutdown must not hit its deadline with an SSE stream open")
	require.Less(t, time.Since(start), 2*time.Second)
}

// A parked long-poll (up to 120s) must also return when shutdown starts.
func TestPollTasks_ServerShutdown_NotBlockedByParkedPoll(t *testing.T) {
	_, rdb := newSSEMiniredis(t)
	h := NewAgentHandlerFull(nil, &MockTaskService{
		GetMyTasksFunc: func(_ context.Context, _, _ uuid.UUID, _ domain.AssigneeType,
			_ repository.AssigneeTaskFilter) ([]domain.Task, int, error) {
			return nil, 0, nil
		},
	}, nil, rdb)
	shutdownCh := make(chan struct{})
	h.SetShutdownSignal(shutdownCh)

	e := echo.New()
	e.GET("/poll", func(c echo.Context) error {
		c.Set(mw.ContextKeyAgentID, uuid.New())
		c.Set(mw.ContextKeyWorkspaceID, uuid.New())
		return h.PollTasks(c)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	e.Listener = ln
	go func() { _ = e.Start("") }()
	t.Cleanup(func() { _ = e.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.Get("http://" + ln.Addr().String() + "/poll?timeout=120"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond) // let the request park

	close(shutdownCh)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, e.Shutdown(ctx), "Shutdown must not wait for a parked long-poll")
	<-done
}
