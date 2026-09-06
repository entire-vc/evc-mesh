package middleware

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/repository"
)

const toolBreakdownFlushInterval = 15 * time.Second

// toolBreakdownBucketKey groups counts accumulated between flushes by the
// agent that made the calls and, when the route carried one, the task and
// workspace it was scoped to. TaskID is uuid.Nil for calls with no
// :task_id route param — that's an agent-wide bucket, mirroring the
// agent-wide fallback ReportSession already uses when it has no task_id to
// scope by (see agent_handler.go ReportSession / repository.
// AgentSessionRepository.IncrementToolBreakdown's doc comment).
type toolBreakdownBucketKey struct {
	AgentID     uuid.UUID
	WorkspaceID uuid.UUID
	TaskID      uuid.UUID
}

// ToolBreakdownTracker is a non-blocking middleware that counts MCP tool
// calls in memory — one per authenticated-agent HTTP request, keyed by
// (agent, workspace, task) and the resolved tool name — and flushes the
// counts to agent_sessions.tool_breakdown every 15s via a background
// goroutine.
//
// This batches for the same reason ActivityTracker (agent_activity.go)
// batches last_heartbeat updates: recall/remember/get_task/... are the
// single hottest path in the system (every tool call any agent makes goes
// through here), and a synchronous DB write on every one of them would put a
// write on that hot path for what is purely an observability counter — one
// that can afford to lose up to one flush interval's worth of counts on an
// ungraceful crash, the same tradeoff already accepted for last_heartbeat.
type ToolBreakdownTracker struct {
	mu          sync.Mutex
	dirty       map[toolBreakdownBucketKey]map[string]int64
	sessionRepo repository.AgentSessionRepository
}

// NewToolBreakdownTracker creates a new ToolBreakdownTracker.
func NewToolBreakdownTracker(sessionRepo repository.AgentSessionRepository) *ToolBreakdownTracker {
	return &ToolBreakdownTracker{
		dirty:       make(map[toolBreakdownBucketKey]map[string]int64),
		sessionRepo: sessionRepo,
	}
}

// Middleware returns an Echo middleware that records one tool call for the
// requesting agent against the MCP tool name resolved from the route. Must
// be registered after DualAuth (agent_id/workspace_id in context) — same
// placement requirement as ActivityTracker.Middleware(). Non-agent (human,
// JWT-authenticated) requests are not counted: only agents call Mesh through
// MCP tools, so IsAgent(c) is exactly the filter this needs.
func (t *ToolBreakdownTracker) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if IsAgent(c) {
				agentID, aerr := GetAgentID(c)
				workspaceID, werr := GetWorkspaceID(c)
				if aerr == nil && werr == nil {
					var taskID uuid.UUID
					if ts := c.Param("task_id"); ts != "" {
						if parsed, perr := uuid.Parse(ts); perr == nil {
							taskID = parsed
						}
					}
					tool := resolveMCPToolName(c.Request().Method, c.Path())
					t.record(agentID, workspaceID, taskID, tool)
				}
			}
			return next(c)
		}
	}
}

func (t *ToolBreakdownTracker) record(agentID, workspaceID, taskID uuid.UUID, tool string) {
	if tool == "" {
		return
	}
	key := toolBreakdownBucketKey{AgentID: agentID, WorkspaceID: workspaceID, TaskID: taskID}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.dirty[key]
	if !ok {
		m = make(map[string]int64)
		t.dirty[key] = m
	}
	m[tool]++
}

// Run starts the background flush loop. It blocks until ctx is cancelled.
// Call it in a goroutine from main.
func (t *ToolBreakdownTracker) Run(ctx context.Context) {
	ticker := time.NewTicker(toolBreakdownFlushInterval)
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

// Flush drains accumulated counts immediately. Call during graceful shutdown
// so the last (up to) 15s of tool calls aren't lost.
func (t *ToolBreakdownTracker) Flush(ctx context.Context) {
	t.flush(ctx)
}

func (t *ToolBreakdownTracker) flush(ctx context.Context) {
	t.mu.Lock()
	batch := t.dirty
	t.dirty = make(map[toolBreakdownBucketKey]map[string]int64)
	t.mu.Unlock()

	for key, counts := range batch {
		if len(counts) == 0 {
			continue
		}
		var taskID *uuid.UUID
		if key.TaskID != uuid.Nil {
			id := key.TaskID
			taskID = &id
		}
		if err := t.sessionRepo.IncrementToolBreakdown(ctx, key.AgentID, key.WorkspaceID, taskID, counts); err != nil {
			log.Printf("[tool-breakdown] flush failed for agent %s: %v", key.AgentID, err)
		}
	}
}
