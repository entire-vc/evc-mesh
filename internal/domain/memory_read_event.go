package domain

import (
	"time"

	"github.com/google/uuid"
)

// Caller kinds of a memory read or tool call (memory_read_events.caller_kind,
// agent_tool_calls_daily.caller_kind). The MCP server names itself with the
// X-Mesh-Caller header; a browser session of a user is ui; anything else is
// unknown, and route + user_agent identify it.
const (
	CallerKindAgentTool = "agent_tool"
	CallerKindRunner    = "runner"
	CallerKindScript    = "script"
	CallerKindUI        = "ui"
	CallerKindUnknown   = "unknown"
)

// ValidCallerKind reports whether v is one of the caller kinds above.
func ValidCallerKind(v string) bool {
	switch v {
	case CallerKindAgentTool, CallerKindRunner, CallerKindScript, CallerKindUI, CallerKindUnknown:
		return true
	}
	return false
}

// MemoryReadResult is one served row of a memory read, in rank order.
type MemoryReadResult struct {
	ID         uuid.UUID `json:"id"`
	Key        string    `json:"key"`
	Source     string    `json:"source"`
	Rank       int       `json:"rank"`
	Score      *float64  `json:"score"`
	Superseded bool      `json:"superseded"`
}

// MemoryReadEvent is one row of memory_read_events.
type MemoryReadEvent struct {
	TS              time.Time
	WorkspaceID     *uuid.UUID
	ProjectID       *uuid.UUID
	AgentID         *uuid.UUID
	UserID          *uuid.UUID
	SessionID       *string
	TaskID          *uuid.UUID
	CallerKind      string
	Route           string
	UserAgent       string
	Op              string
	StatusCode      int
	QueryHash       *string
	QueryText       *string
	SearchMode      *string
	ResultCount     int
	PinnedCount     int
	SupersededCount int
	TopScore        *float64
	Results         []MemoryReadResult
	ResponseChars   int
	DurationMS      int
	Error           *string
}

// ToolCallsDailyDelta is one increment of agent_tool_calls_daily. TaskID is
// not a column: when ProjectID is nil the writer resolves the project from it.
type ToolCallsDailyDelta struct {
	Day         time.Time
	WorkspaceID uuid.UUID
	ProjectID   *uuid.UUID
	TaskID      *uuid.UUID
	AgentID     uuid.UUID
	CallerKind  string
	Tool        string
	Calls       int64
}
