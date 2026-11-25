package middleware

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/entire-vc/evc-mesh/internal/domain"
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

	// Daily counters (agent_tool_calls_daily). Nil dailySink = off
	// (MEMORY_READ_LOG=false): agent_sessions.tool_breakdown keeps working.
	dailySink  ToolCallsDailySink
	dailyDirty map[toolCallsDailyKey]int64
	now        func() time.Time
}

// ToolCallsDailySink persists agent_tool_calls_daily increments.
type ToolCallsDailySink interface {
	IncrementToolCallsDaily(ctx context.Context, deltas []domain.ToolCallsDailyDelta) error
}

// toolCallsDailyKey is one agent_tool_calls_daily bucket before the flush.
// TaskID is kept so the writer can resolve the project of a task route.
type toolCallsDailyKey struct {
	Day         string
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	TaskID      uuid.UUID
	AgentID     uuid.UUID
	CallerKind  string
	Tool        string
}

// ContextKeyResponseCommentCount is set by the handlers that return comments
// (task context, comment list) to the number of comments in the response. The
// daily counters book get_task/get_task_context as "+comments" only when it is
// at least one.
const ContextKeyResponseCommentCount = "mesh_response_comment_count"

// SetResponseCommentCount records how many comments a response carried.
func SetResponseCommentCount(c echo.Context, n int) { c.Set(ContextKeyResponseCommentCount, n) }

func responseCommentCount(c echo.Context) int {
	n, _ := c.Get(ContextKeyResponseCommentCount).(int)
	return n
}

// Tool keys of a task read that returned comments.
const (
	ToolGetTaskWithComments        = "get_task+comments"
	ToolGetTaskContextWithComments = "get_task_context+comments"
)

// EnableDaily turns on the per-day counters. Call before Run.
func (t *ToolBreakdownTracker) EnableDaily(sink ToolCallsDailySink) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dailySink = sink
}

// NewToolBreakdownTracker creates a new ToolBreakdownTracker.
func NewToolBreakdownTracker(sessionRepo repository.AgentSessionRepository) *ToolBreakdownTracker {
	return &ToolBreakdownTracker{
		dirty:       make(map[toolBreakdownBucketKey]map[string]int64),
		sessionRepo: sessionRepo,
		dailyDirty:  make(map[toolCallsDailyKey]int64),
		now:         time.Now,
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
					err := next(c)
					t.recordDaily(c, agentID, workspaceID, taskID, tool)
					return err
				}
			}
			return next(c)
		}
	}
}

// recordDaily books calls after the handler ran. A composite MCP get_task
// marks its primary request X-Mesh-Tool: get_task+comments (no daily entry);
// its comments request carries X-Mesh-Tool: get_task and records the single
// logical call according to the rows actually served, including an empty page.
// This avoids corrections across flushes or UTC day boundaries.
func (t *ToolBreakdownTracker) recordDaily(c echo.Context, agentID, workspaceID, taskID uuid.UUID, tool string) {
	if tool == "" {
		return
	}
	t.mu.Lock()
	enabled := t.dailySink != nil
	t.mu.Unlock()
	if !enabled {
		return
	}
	var projectID uuid.UUID
	if parsed, err := uuid.Parse(c.Param("proj_id")); err == nil {
		projectID = parsed
	} else if parsed, err := uuid.Parse(c.QueryParam("project_id")); err == nil {
		projectID = parsed
	}
	key := toolCallsDailyKey{
		Day:         t.now().UTC().Format("2006-01-02"),
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		TaskID:      taskID,
		AgentID:     agentID,
		CallerKind:  CallerKindOf(c),
	}
	if tool == "get_task" && c.Request().Header.Get(HeaderMeshTool) == ToolGetTaskWithComments {
		return
	}
	switch {
	case tool == "get_task_context" && responseCommentCount(c) > 0:
		tool = ToolGetTaskContextWithComments
	case tool == "get_task" && responseCommentCount(c) > 0:
		tool = ToolGetTaskWithComments
	case tool == "list_comments" && c.Request().Header.Get(HeaderMeshTool) == "get_task":
		tool = "get_task"
		if responseCommentCount(c) > 0 {
			tool = ToolGetTaskWithComments
		}
	}
	key.Tool = tool
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dailyDirty[key]++
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
	daily := t.dailyDirty
	t.dailyDirty = make(map[toolCallsDailyKey]int64)
	dailySink := t.dailySink
	t.mu.Unlock()

	if dailySink != nil && len(daily) > 0 {
		deltas := make([]domain.ToolCallsDailyDelta, 0, len(daily))
		for k, n := range daily {
			if n == 0 {
				continue
			}
			day, _ := time.Parse("2006-01-02", k.Day)
			d := domain.ToolCallsDailyDelta{Day: day, WorkspaceID: k.WorkspaceID, AgentID: k.AgentID, CallerKind: k.CallerKind, Tool: k.Tool, Calls: n}
			if k.ProjectID != uuid.Nil {
				id := k.ProjectID
				d.ProjectID = &id
			}
			if k.TaskID != uuid.Nil {
				id := k.TaskID
				d.TaskID = &id
			}
			deltas = append(deltas, d)
		}
		if err := dailySink.IncrementToolCallsDaily(ctx, deltas); err != nil {
			log.Printf("[tool-breakdown] daily counters flush failed (%d buckets): %v", len(deltas), err)
		}
	}

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
