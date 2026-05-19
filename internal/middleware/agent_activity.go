package middleware

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

const activityFlushInterval = 15 * time.Second

// ActivityTracker is a non-blocking middleware that batches last_heartbeat
// updates for authenticated agents. It records which agents made API calls
// and flushes them to the DB every 15 s via a background goroutine.
type ActivityTracker struct {
	dirtySet  sync.Map
	agentRepo repository.AgentRepository
}

// NewActivityTracker creates a new ActivityTracker.
func NewActivityTracker(agentRepo repository.AgentRepository) *ActivityTracker {
	return &ActivityTracker{agentRepo: agentRepo}
}

// Middleware returns an Echo middleware that marks the requesting agent dirty.
// Must be registered after DualAuth so agent_id is available in context.
func (t *ActivityTracker) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if IsAgent(c) {
				if id, err := GetAgentID(c); err == nil {
					t.dirtySet.Store(id, struct{}{})
				}
			}
			return next(c)
		}
	}
}

// Run starts the background flush loop. It blocks until ctx is cancelled.
// Call it in a goroutine from main.
func (t *ActivityTracker) Run(ctx context.Context) {
	ticker := time.NewTicker(activityFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.flush(ctx)
		}
	}
}

// Flush drains the dirty set immediately. Call during graceful shutdown.
func (t *ActivityTracker) Flush(ctx context.Context) {
	t.flush(ctx)
}

func (t *ActivityTracker) flush(ctx context.Context) {
	var ids []uuid.UUID
	t.dirtySet.Range(func(k, _ any) bool {
		ids = append(ids, k.(uuid.UUID))
		t.dirtySet.Delete(k)
		return true
	})
	if len(ids) > 0 {
		_ = t.agentRepo.TouchLastSeenBatch(ctx, ids)
	}
}
