package mcp

import (
	"sync"
	"time"
)

// SessionTracker tracks MCP tool usage per agent session (in-memory).
// It is thread-safe and designed for use across concurrent tool calls.
type SessionTracker struct {
	mu              sync.Mutex
	startedAt       time.Time
	toolCalls       int
	toolBreakdown   map[string]int
	tasksSet        map[string]bool // unique task IDs touched
	memoriesCreated int
	eventsPublished int
}

// NewSessionTracker creates a new SessionTracker with the current time as start.
func NewSessionTracker() *SessionTracker {
	return &SessionTracker{
		startedAt:     time.Now(),
		toolBreakdown: make(map[string]int),
		tasksSet:      make(map[string]bool),
	}
}

// RecordToolCall increments the call counter for the named tool.
// Special tools (remember, publish_event, publish_summary) also update
// dedicated counters used in compliance scoring.
func (t *SessionTracker) RecordToolCall(toolName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.toolCalls++
	t.toolBreakdown[toolName]++

	switch toolName {
	case "remember":
		t.memoriesCreated++
	case "publish_event", "publish_summary":
		t.eventsPublished++
	}
}

// RecordTaskTouch records that the given task ID was touched this session.
func (t *SessionTracker) RecordTaskTouch(taskID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tasksSet[taskID] = true
}

// Stats returns a snapshot of session metrics as a map suitable for JSON serialisation.
func (t *SessionTracker) Stats() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	tasks := make([]string, 0, len(t.tasksSet))
	for id := range t.tasksSet {
		tasks = append(tasks, id)
	}
	// Copy breakdown to avoid sharing the internal map.
	breakdown := make(map[string]int, len(t.toolBreakdown))
	for k, v := range t.toolBreakdown {
		breakdown[k] = v
	}
	return map[string]any{
		"started_at":       t.startedAt.Format(time.RFC3339),
		"tool_calls":       t.toolCalls,
		"tool_breakdown":   breakdown,
		"tasks_touched":    tasks,
		"memories_created": t.memoriesCreated,
		"events_published": t.eventsPublished,
	}
}

// ComplianceScore computes a 0–1 score reflecting ACP (Agent Collaboration Protocol)
// adherence for the current session. Returns the score and a per-check detail map.
func (t *SessionTracker) ComplianceScore() (score float64, checks map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	detail := map[string]bool{
		"called_get_me":                t.toolBreakdown["get_me"] > 0 || t.toolBreakdown["heartbeat"] > 0,
		"called_get_project_knowledge": t.toolBreakdown["get_project_knowledge"] > 0 || t.toolBreakdown["recall"] > 0,
		"called_get_my_rules":          t.toolBreakdown["get_my_rules"] > 0,
		"called_get_context":           t.toolBreakdown["get_context"] > 0,
		"called_get_my_tasks":          t.toolBreakdown["get_my_tasks"] > 0 || t.toolBreakdown["poll_tasks"] > 0,
		"published_summary":            t.toolBreakdown["publish_event"] > 0 || t.toolBreakdown["publish_summary"] > 0,
		"created_memory":               t.memoriesCreated > 0,
	}

	score = 0.0
	if detail["called_get_me"] {
		score += 0.10
	}
	if detail["called_get_project_knowledge"] {
		score += 0.25
	}
	if detail["called_get_my_rules"] {
		score += 0.10
	}
	if detail["called_get_context"] {
		score += 0.25
	}
	if detail["called_get_my_tasks"] {
		score += 0.05
	}
	if detail["published_summary"] {
		score += 0.15
	}
	if detail["created_memory"] {
		score += 0.10
	}

	return score, detail
}
