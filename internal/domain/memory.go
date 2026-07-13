package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Memory kind tag constants — well-known values for the kind: tag prefix.
// These map to importance_score ranges and special recall behaviours.
const (
	// KindPinned marks a memory as always-injected into recall results regardless of
	// relevance score or min_importance threshold. Suitable for must-not-vanish facts.
	KindPinned = "kind:pinned"
	// KindPreference marks a user/agent preference memory. Maps to importance_score 0.80.
	KindPreference = "kind:preference"
)

// MemoryStatus represents the lifecycle health state of a memory entry.
// The status drives freshness scoring and recall filtering.
type MemoryStatus string

const (
	// MemoryStatusActive is the default state — the memory is fresh and reliable.
	MemoryStatusActive MemoryStatus = "active"
	// MemoryStatusStale indicates the memory may be outdated; still recalled but with reduced freshness_score.
	MemoryStatusStale MemoryStatus = "stale"
	// MemoryStatusSuperseded means a newer memory has replaced this one.
	// The superseded_by field points to the replacement.
	MemoryStatusSuperseded MemoryStatus = "superseded"
	// MemoryStatusArchived is a soft-delete state; excluded from recall by default.
	MemoryStatusArchived MemoryStatus = "archived"
	// MemoryStatusConflicted means two memories contradict each other; requires human review.
	MemoryStatusConflicted MemoryStatus = "conflicted"
	// MemoryStatusReviewNeeded flags the memory for human or agent review before acting on it.
	MemoryStatusReviewNeeded MemoryStatus = "review_needed"
)

// StatusFreshnessScore returns the canonical freshness_score floor for a given status.
// The actual freshness_score may be higher (set explicitly) but never lower than this floor.
func (s MemoryStatus) StatusFreshnessScore() float32 {
	switch s {
	case MemoryStatusActive:
		return 1.0
	case MemoryStatusStale:
		return 0.25
	case MemoryStatusSuperseded:
		return 0.1
	case MemoryStatusArchived:
		return 0.0
	default:
		return 0.5
	}
}

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

	// ThreadID is propagated from the source task's thread_id at write time.
	// Enables same-thread edge creation without a JOIN to the tasks table.
	ThreadID *string `json:"thread_id,omitempty" db:"thread_id"`
	// SourceTaskID is the Mesh task UUID that produced this memory.
	// Used by the task-graph bridge (Amendment 3) to create derived_from edges.
	SourceTaskID *uuid.UUID `json:"source_task_id,omitempty" db:"source_task_id"`

	// Health lifecycle fields (migration 080, P1-A).

	// Status represents the current health/freshness lifecycle state.
	// Defaults to MemoryStatusActive on creation.
	Status MemoryStatus `json:"status" db:"status"`
	// FreshnessScore is a [0.0, 1.0] float representing how fresh/reliable the memory is.
	// Starts at 1.0 for active memories; degrades as staleness is detected.
	FreshnessScore float32 `json:"freshness_score" db:"freshness_score"`
	// SupersededBy points to the Memory that replaced this one when Status=superseded.
	SupersededBy *uuid.UUID `json:"superseded_by,omitempty" db:"superseded_by"`
	// ValidFrom marks the start of the time window during which the memory is valid.
	// Nil means "valid from creation".
	ValidFrom *time.Time `json:"valid_from,omitempty" db:"valid_from"`
	// ValidUntil marks the end of the time window during which the memory is valid.
	// Distinct from ExpiresAt: expiry is about retention, validity is about truth.
	ValidUntil *time.Time `json:"valid_until,omitempty" db:"valid_until"`
	// ContentSimhash is a 64-bit simhash of the memory content (3-gram shingles, FNV-64a).
	// Used for write-time and periodic near-duplicate detection. Nil when not yet computed.
	ContentSimhash *int64 `json:"content_simhash,omitempty" db:"content_simhash"`

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
	// RecencyScore is the exponential decay factor applied to this item during recall.
	// Value in [0, 1]: 1.0 = brand-new or decay not applied; approaches 0 for very old items.
	RecencyScore float64 `json:"recency_score,omitempty"`
}

// SearchMode reports which retrieval arms actually served a Recall.
//
// Recall is a hybrid search: a sparse BM25 arm plus a dense/vector arm, merged
// with RRF. The dense arm needs a live embedder, and Recall FAILS OPEN when the
// embedder errors — it silently returns BM25-only results rather than an error.
// That degradation used to be invisible to callers (one log line, no metric, no
// response field), so a dead embedder could serve materially worse recall for
// days without anyone noticing, and a benchmark could not tell "this code is
// worse" apart from "the embedder is down".
//
// SearchMode makes the fail-open visible: it is the mode a given recall was
// actually SERVED in, not the mode it was configured for.
type SearchMode string

const (
	// SearchModeHybrid means the dense/vector arm ran successfully and
	// contributed to the RRF merge.
	SearchModeHybrid SearchMode = "hybrid"
	// SearchModeBM25Only means the dense/vector arm did NOT contribute: the
	// embedder is unconfigured (noop), errored, or the vector search failed.
	// Results came from the sparse BM25 arm alone.
	SearchModeBM25Only SearchMode = "bm25-only"
)

// Degraded reports whether the recall was served without its dense arm.
// Anything that is not a full hybrid search is a degraded search.
func (m SearchMode) Degraded() bool { return m != SearchModeHybrid }

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
	// RecencyWeight blends an exponential recency-decay factor into the full-text
	// ranking. 0.0 (default) = exactly the legacy behavior (rank by FTS score only);
	// 1.0 = rank purely by recency. Values are clamped to [0,1]. See MemoryRepository.FullTextSearch.
	RecencyWeight float64
	// HalfLifeDays overrides the service-level default half-life for exponential decay.
	// 0 means use the service default (env MEMORY_RECALL_HALF_LIFE_DAYS, fallback 30d).
	// Valid range: 1–365. Applied when ApplyDecay=true or OrderBy="decayed_relevance".
	HalfLifeDays float64
	Offset       int

	// Health lifecycle filters (P1-A).
	// StatusFilter restricts results to memories with one of the given statuses.
	// If empty, no status filter is applied (all statuses may be returned).
	StatusFilter []MemoryStatus
	// ExcludeSuperseded excludes memories with status=superseded from results.
	// Applied after StatusFilter. Defaults to true in the service layer.
	ExcludeSuperseded bool
	// IncludeStale includes stale memories in results (default true).
	// When false, only active/review_needed/conflicted memories are returned.
	IncludeStale bool
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
	IncludeArchived bool    // if false (default), only archived=false rows are returned
	OrderBy         string  // "created_at:desc", "relevance:desc", "decayed_relevance:desc"
	ApplyDecay      bool    // if true, score = relevance * pow(0.95, days_since)
	HalfLifeDays    float64 // half-life for decayed_relevance ordering; 0 means 30d default
	Limit           int
	Offset          int

	// Health lifecycle filters (P1-A).
	StatusFilter      []MemoryStatus // restrict to given statuses (empty = no filter)
	ExcludeSuperseded bool           // skip status=superseded memories
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

// RecallGraphProvenance describes how a memory was reached during a RecallGraph traversal.
// "via:recall" means the memory was a direct seed from hybrid recall;
// "via:graph" means it was discovered by BFS expansion along KG edges.
type RecallGraphProvenance string

const (
	ProvenanceRecall RecallGraphProvenance = "via:recall"
	ProvenanceGraph  RecallGraphProvenance = "via:graph"
)

// RecallGraphResult is a single entry in the response of a multi-hop graph traversal.
// CompositeScore = seed_score × Π(edge_weight along the chain from seed to this node).
type RecallGraphResult struct {
	ID              uuid.UUID             `json:"id"`
	Content         string                `json:"content"`
	ImportanceScore float32               `json:"importance_score"`
	CompositeScore  float64               `json:"composite_score"`
	Provenance      RecallGraphProvenance `json:"provenance"`
	HopDistance     int                   `json:"hop_distance"`
}

// RecallGraphOpts holds parameters for a multi-hop graph recall traversal.
type RecallGraphOpts struct {
	Query           string
	WorkspaceID     uuid.UUID
	ProjectID       *uuid.UUID
	Hops            int
	WeightThreshold float64
	TaskID          *uuid.UUID
}

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
