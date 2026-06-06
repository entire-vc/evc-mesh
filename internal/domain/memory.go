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

// IsValid reports whether the source type is one of the recognised values.
func (s MemorySourceType) IsValid() bool {
	switch s {
	case SourceAgent, SourceHuman, SourceSystem:
		return true
	default:
		return false
	}
}

// Memory is a persistent, searchable knowledge entry stored by an agent or the system.
// Memories survive across agent sessions and can be recalled via full-text search.
// When an embedding provider is configured, the Embedding field holds a dense vector
// representation used for semantic (hybrid) recall. It is nil when vector search is disabled.
type Memory struct {
	ID              uuid.UUID        `json:"id" db:"id"`
	WorkspaceID     uuid.UUID        `json:"workspace_id" db:"workspace_id"`
	ProjectID       *uuid.UUID       `json:"project_id,omitempty" db:"project_id"`
	AgentID         *uuid.UUID       `json:"agent_id,omitempty" db:"agent_id"`
	Key             string           `json:"key" db:"key"`
	Content         string           `json:"content" db:"content"`
	Scope           MemoryScope      `json:"scope" db:"scope"`
	Tags            pq.StringArray   `json:"tags" db:"tags"`
	SourceType      MemorySourceType `json:"source_type" db:"source_type"`
	SourceEventID   *uuid.UUID       `json:"source_event_id,omitempty" db:"source_event_id"`
	SourceURL       *string          `json:"source_url,omitempty" db:"source_url"`
	Relevance       float32          `json:"relevance" db:"relevance"`
	ImportanceScore float32          `json:"importance_score" db:"importance_score"`
	CreatedAt       time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at" db:"updated_at"`
	ExpiresAt       *time.Time       `json:"expires_at,omitempty" db:"expires_at"`
	LastAccessedAt  *time.Time       `json:"last_accessed_at,omitempty" db:"last_accessed_at"`
	Archived        bool             `json:"archived" db:"archived"`

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

	// Extended filter params (Phase 2 — memory API extensions).
	TagsAny        []string         // OR filter: memory must contain at least one of these tags
	CreatedBy      *uuid.UUID       // agent_id filter
	SourceType     MemorySourceType // "agent" | "human" | "system"; empty means no filter
	Since          *time.Time       // created_at >=
	Until          *time.Time       // created_at <=
	RelevanceMin   *float32         // relevance >=
	MinImportance  *float32         // importance_score >= (default 0.4 applied at service layer)
	IncludeExpired bool             // if false, filters expires_at > now() OR expires_at IS NULL
	OrderBy        string           // "created_at:desc", "relevance:desc", "decayed_relevance:desc"
	ApplyDecay     bool             // if true, sort by relevance * pow(0.95, days_since_created)
	Offset         int
}

// MemoryListFilter is the structured filter passed to the repository List method.
type MemoryListFilter struct {
	WorkspaceID     uuid.UUID
	ProjectID       *uuid.UUID
	Scope           string
	Query           string           // full-text search (optional)
	Tags            []string         // AND filter
	TagsAny         []string         // OR filter
	CreatedBy       *uuid.UUID       // agent_id filter
	SourceType      MemorySourceType // "agent" | "human" | "system"; empty means no filter
	Since           *time.Time       // created_at >=
	Until           *time.Time       // created_at <=
	RelevanceMin    *float32         // relevance >=
	MinImportance   *float32         // importance_score >= (default 0.4 applied at service layer)
	IncludeExpired  bool
	IncludeArchived bool   // if false (default), only archived=false rows are returned
	OrderBy         string // "created_at:desc", "relevance:desc", "decayed_relevance:desc"
	ApplyDecay      bool   // if true, score = relevance * pow(0.95, days_since)
	Limit           int
	Offset          int
}

// MemoryListResult is the structured response from the repository List method.
type MemoryListResult struct {
	Items        []ScoredMemory
	Total        int
	DecayApplied bool
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

// MemoryEdgeRelationshipType defines the semantic relationship for a directed KG edge.
type MemoryEdgeRelationshipType string

const (
	EdgeRelatesTo   MemoryEdgeRelationshipType = "relates_to"
	EdgeSupersedes  MemoryEdgeRelationshipType = "supersedes"
	EdgeDependsOn   MemoryEdgeRelationshipType = "depends_on"
	EdgeContradicts MemoryEdgeRelationshipType = "contradicts"
	EdgeDerivedFrom MemoryEdgeRelationshipType = "derived_from"
)

// MemoryEdge is a directed, typed, weighted link in the memory Knowledge Graph.
// From-to direction: memory_from_id → memory_to_id, with RelationshipType semantics.
type MemoryEdge struct {
	ID               uuid.UUID                  `json:"id" db:"id"`
	MemoryFromID     uuid.UUID                  `json:"memory_from_id" db:"memory_from_id"`
	MemoryToID       uuid.UUID                  `json:"memory_to_id" db:"memory_to_id"`
	RelationshipType MemoryEdgeRelationshipType `json:"relationship_type" db:"relationship_type"`
	Weight           float32                    `json:"weight" db:"weight"`
	WorkspaceID      uuid.UUID                  `json:"workspace_id" db:"workspace_id"`
	CreatedAt        time.Time                  `json:"created_at" db:"created_at"`
	LastTraversedAt  *time.Time                 `json:"last_traversed_at,omitempty" db:"last_traversed_at"`
}
