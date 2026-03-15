package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// MemoryScope defines where a memory entry is visible.
type MemoryScope string

const (
	// ScopeWorkspace makes a memory visible to all agents in the workspace.
	ScopeWorkspace MemoryScope = "workspace"
	// ScopeProject makes a memory visible within a specific project.
	ScopeProject MemoryScope = "project"
	// ScopeAgent makes a memory private to the creating agent.
	ScopeAgent MemoryScope = "agent"
)

// MemorySourceType identifies what created a memory entry.
type MemorySourceType string

const (
	// SourceAgent indicates the memory was created by an AI agent.
	SourceAgent MemorySourceType = "agent"
	// SourceHuman indicates the memory was created by a human user.
	SourceHuman MemorySourceType = "human"
	// SourceSystem indicates the memory was created automatically by the system.
	SourceSystem MemorySourceType = "system"
)

// Memory is a persistent, searchable knowledge entry stored by an agent or the system.
// Memories survive across agent sessions and can be recalled via full-text search.
// When an embedding provider is configured, the Embedding field holds a dense vector
// representation used for semantic (hybrid) recall. It is nil when vector search is disabled.
type Memory struct {
	ID            uuid.UUID        `json:"id" db:"id"`
	WorkspaceID   uuid.UUID        `json:"workspace_id" db:"workspace_id"`
	ProjectID     *uuid.UUID       `json:"project_id,omitempty" db:"project_id"`
	AgentID       *uuid.UUID       `json:"agent_id,omitempty" db:"agent_id"`
	Key           string           `json:"key" db:"key"`
	Content       string           `json:"content" db:"content"`
	Scope         MemoryScope      `json:"scope" db:"scope"`
	Tags          pq.StringArray   `json:"tags" db:"tags"`
	SourceType    MemorySourceType `json:"source_type" db:"source_type"`
	SourceEventID *uuid.UUID       `json:"source_event_id,omitempty" db:"source_event_id"`
	Relevance     float32          `json:"relevance" db:"relevance"`
	CreatedAt     time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at" db:"updated_at"`
	ExpiresAt     *time.Time       `json:"expires_at,omitempty" db:"expires_at"`

	// Embedding fields — populated only when an embedding provider is configured.
	// Embedding is the raw float32 vector; it is not serialised to JSON for API responses.
	Embedding      []float32 `json:"-" db:"-"`
	EmbeddingModel string    `json:"-" db:"embedding_model"`
	EmbeddingDim   int       `json:"-" db:"embedding_dim"`
}

// ScoredMemory wraps a Memory with a full-text search rank score.
type ScoredMemory struct {
	Memory
	Score float64 `json:"score"`
}

// RecallOpts specifies parameters for a memory recall (full-text search) operation.
type RecallOpts struct {
	Query       string
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	Scope       MemoryScope
	Tags        []string
	Limit       int
}

// MemoryHint is embedded in an event bus message payload to signal that the event
// should be persisted as a memory entry. Agents include this when publishing events
// that contain knowledge worth storing for future recall.
type MemoryHint struct {
	// Persist instructs the event pipeline to create a memory entry from this event.
	Persist bool `json:"persist"`
	// Key is the unique memory key within the given scope.
	Key string `json:"key"`
	// Scope controls visibility of the resulting memory.
	Scope MemoryScope `json:"scope"`
	// Tags are indexed for filtering and relevance boosting.
	Tags []string `json:"tags,omitempty"`
	// ExpiresIn is a Go duration string (e.g. "72h") controlling memory TTL.
	// Empty means the memory never expires.
	ExpiresIn string `json:"expires_in,omitempty"`
}
