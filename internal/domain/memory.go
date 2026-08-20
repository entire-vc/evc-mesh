package domain

import (
	"fmt"
	"slices"
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
	// Used for periodic near-duplicate detection by the reconciler. Nil when not yet computed.
	ContentSimhash *int64 `json:"content_simhash,omitempty" db:"content_simhash"`

	// Version is bumped on every write to this memory and is what a conditional
	// write is conditional on. Starts at 1. Rows written before migration
	// 20260820109 carry the default 1 without a matching revision row — they
	// have no recorded history, which is the honest state rather than a
	// fabricated one.
	Version int `json:"version" db:"version"`

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

// RecallStats reports what each retrieval arm actually CONTRIBUTED to a recall,
// alongside the mode it was served in.
//
// Mode alone is not enough, and the gap is not theoretical. SearchModeHybrid is
// set when the dense arm completed end-to-end — embed OK, non-empty query
// vector, VectorSearch returned without error — which is a statement about the
// EMBEDDER being alive, not about the vector arm having found anything. A
// VectorSearch that matches zero rows across the entire corpus (every
// memories.embedding IS NULL, say, because a write-path change stopped
// populating it) returns (nil, nil): the dense arm "ran", the mode says
// "hybrid", `degraded` says false, and the recall is served by BM25 alone with
// nothing anywhere saying so.
//
// That exact state shipped: after the chunked-embed write path landed, every
// newly written memory had a NULL embedding, and the CI recall gate scored its
// best-ever run — `hybrid`, `degraded: false` — measuring a corpus its dense arm
// could not see. DenseRows is the number that distinguishes the two: a hybrid
// recall with DenseRows == 0 is a dead arm wearing a healthy label.
type RecallStats struct {
	// Mode is the mode the recall was actually SERVED in.
	Mode SearchMode
	// DenseRows is how many candidates the vector arm returned, counted BEFORE
	// the RRF merge and before any post-filtering — it measures the arm, not
	// what survived downstream. Always 0 when Mode is not SearchModeHybrid,
	// since the arm did not run at all.
	DenseRows int
	// SparseRows is the same count for the BM25 arm. It is 0 both when the FTS
	// query genuinely matched nothing and when it errored (Recall drops those
	// results and degrades to vector-only).
	SparseRows int
}

// Canonical `order_by` values. These were bare string literals repeated across
// four files (domain doc comments, the REST handler, the SQL ORDER BY switch, and
// the recall service's decay predicate) until 2026-08-09 (#655c6d12) — and they had
// already drifted: the service compared against "decayed_relevance" while every
// producer in the codebase emits "decayed_relevance:desc", so that comparison was
// dead code from the day it was written. Nothing failed; the branch simply never
// ran, which is the quietest way for a feature to not exist.
const (
	OrderByCreatedAtDesc        = "created_at:desc"
	OrderByCreatedAtAsc         = "created_at:asc"
	OrderByRelevanceDesc        = "relevance:desc"
	OrderByDecayedRelevanceDesc = "decayed_relevance:desc"
)

// CanonicalOrderBy maps an order_by value onto its canonical form, so the same
// intent expressed two ways resolves to one string. A bare sort key without a
// direction suffix means descending — that is the direction every one of these
// keys is useful in, and it is what a caller writing `order_by=decayed_relevance`
// plainly means.
//
// Normalising HERE, once, is the point: the alternative is every consumer deciding
// for itself which spellings count, which is exactly how the decay predicate and
// the SQL switch came to disagree about a value they both name `decayed_relevance`.
// An unrecognised value is returned unchanged — callers keep their own default
// behaviour for it rather than having one invented here.
func CanonicalOrderBy(orderBy string) string {
	switch orderBy {
	case "created_at":
		return OrderByCreatedAtDesc
	case "relevance":
		return OrderByRelevanceDesc
	case "decayed_relevance":
		return OrderByDecayedRelevanceDesc
	default:
		return orderBy
	}
}

// IsDecayedRelevanceOrder reports whether orderBy asks for decay-weighted ordering,
// in either the canonical suffixed form or the bare key.
func IsDecayedRelevanceOrder(orderBy string) bool {
	return CanonicalOrderBy(orderBy) == OrderByDecayedRelevanceDesc
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

// MemorySearchFilter carries the eligibility predicates that BOTH retrieval arms of
// Recall must enforce as SQL, before either arm truncates to the candidate pool.
//
// These are not ranking hints — they define which rows are *allowed* to be returned
// at all. A post-filter cannot substitute for them: by the time it runs, each arm has
// already cut the corpus down to `limit × candidateMultiplier` rows chosen without
// regard to scope or tags, so an eligible row that ranked below that cut is gone and
// no amount of downstream filtering brings it back. Pushing them into both arms makes
// the candidate pool the *eligible* set rather than "the first N of the workspace".
//
// Scope in particular is an isolation contract, not an optimisation: it was previously
// passed to the vector arm only, which left it unenforced on every BM25-arm row and
// entirely unenforced in bm25-only mode (the fail-open path taken whenever the embedder
// is down). See task #2c087b2a.
type MemorySearchFilter struct {
	// Scope restricts rows to a single memory scope ("workspace", "project", "agent").
	// Empty means no scope restriction.
	Scope string
	// Tags is an AND filter: a row must carry ALL of these tags (SQL `tags @> …`).
	Tags []string
	// TagsAny is an OR filter: a row must carry AT LEAST ONE of these (SQL `tags && …`).
	TagsAny []string
}

// SearchFilter extracts the arm-level eligibility predicates from a RecallOpts.
func (o RecallOpts) SearchFilter() MemorySearchFilter {
	return MemorySearchFilter{
		Scope:   string(o.Scope),
		Tags:    o.Tags,
		TagsAny: o.TagsAny,
	}
}

// IsZero reports whether the filter constrains nothing.
func (f MemorySearchFilter) IsZero() bool {
	return f.Scope == "" && len(f.Tags) == 0 && len(f.TagsAny) == 0
}

// Allows reports whether a memory satisfies the filter. It is the in-memory twin of the
// SQL predicates the repository builds from the same struct, used as defence-in-depth on
// rows that reach the client through a path that did not pre-filter (pinned injection,
// graph expansion). The two must agree; if you change one, change the other.
func (f MemorySearchFilter) Allows(m Memory) bool {
	if f.Scope != "" && string(m.Scope) != f.Scope {
		return false
	}
	if len(f.Tags) > 0 {
		for _, required := range f.Tags {
			if !slices.Contains(m.Tags, required) {
				return false
			}
		}
	}
	if len(f.TagsAny) > 0 {
		found := false
		for _, want := range f.TagsAny {
			if slices.Contains(m.Tags, want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
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
//
// Key/Scope/Tags exist so the CALLER can verify a scope/tag filter for itself,
// not just trust that the server applied one. Task #37e9344c found that a
// graph-boosted row carrying no metadata at all is indistinguishable from a
// row that legitimately passed a filter — the missing fields hid the fact
// that filters weren't reaching this arm in the first place.
type RecallGraphResult struct {
	ID              uuid.UUID             `json:"id"`
	Key             string                `json:"key"`
	Scope           MemoryScope           `json:"scope"`
	Tags            []string              `json:"tags"`
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

	// Scope/Tags/TagsAny restrict BOTH the seed recall and the BFS-expanded neighbours.
	// Graph expansion walks memory_edges, which carry no notion of scope, so without an
	// explicit check here an out-of-scope memory adjacent to an in-scope seed would be
	// returned — a filter bypass through the side door. See task #2c087b2a.
	Scope   string
	Tags    []string
	TagsAny []string
}

// SearchFilter extracts the eligibility predicates applied to both seeds and neighbours.
func (o RecallGraphOpts) SearchFilter() MemorySearchFilter {
	return MemorySearchFilter{Scope: o.Scope, Tags: o.Tags, TagsAny: o.TagsAny}
}

// MemoryChunk is one embedded slice of a longer memory (ADR-0002). The prod
// embedder silently truncates input past ~512 tokens, so any memory longer
// than that needs more than one vector to be fully searchable — see #e8063a65.
//
// ChunkStart/ChunkEnd are BYTE offsets (half-open [start, end)) into the
// parent Memory's Content. Embedding is base64(little-endian float32 bytes),
// not a JSON array — see the migration comment for why.
type MemoryChunk struct {
	ID             uuid.UUID `json:"id" db:"id"`
	MemoryID       uuid.UUID `json:"memory_id" db:"memory_id"`
	ChunkIdx       int       `json:"chunk_idx" db:"chunk_idx"`
	ChunkStart     int       `json:"chunk_start" db:"chunk_start"`
	ChunkEnd       int       `json:"chunk_end" db:"chunk_end"`
	Embedding      string    `json:"embedding" db:"embedding"`
	EmbeddingModel string    `json:"embedding_model" db:"embedding_model"`
	EmbeddingDim   int       `json:"embedding_dim" db:"embedding_dim"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
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

// ── Versioning and revision history ─────────────────────────────────────────

// Memory revision actions.
const (
	// MemoryActionCreated is the first version of a key.
	MemoryActionCreated = "created"
	// MemoryActionUpdated is any later write to an existing key.
	MemoryActionUpdated = "updated"
	// MemoryActionForgotten records the content a forget removed, so that
	// "what did this say before someone deleted it" stays answerable.
	MemoryActionForgotten = "forgotten"
)

// MemoryRevision is one historical version of a memory: what it asserted at
// that version, and why the author says it came to assert it.
type MemoryRevision struct {
	ID       uuid.UUID      `json:"id" db:"id"`
	MemoryID uuid.UUID      `json:"memory_id" db:"memory_id"`
	Version  int            `json:"version" db:"version"`
	Content  string         `json:"content" db:"content"`
	Tags     pq.StringArray `json:"tags" db:"tags"`
	Action   string         `json:"action" db:"action"`
	// Reason is nil for writes made before the reason requirement was switched
	// on. Absent and blank are different states and are kept different: blank is
	// rejected by a table CHECK, absent means "this predates the requirement".
	Reason       *string    `json:"reason,omitempty" db:"reason"`
	ActorAgentID *uuid.UUID `json:"actor_agent_id,omitempty" db:"actor_agent_id"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
}

// MemoryWriteIntent carries the caller's intent for a single memory write:
// why it is happening, and what state it believes it is overwriting.
//
// It is a required argument of MemoryRepository.Upsert rather than a set of
// optional fields on Memory, so that adding a fourth write path into the
// memories table cannot silently produce an unversioned, unexplained write.
// The compiler asks the question instead of a reviewer having to notice.
type MemoryWriteIntent struct {
	// Action is one of the MemoryAction* constants. Empty means "derive it":
	// created when the row is new, updated otherwise.
	Action string

	// Reason is the author's statement of why this version exists. May be empty
	// while MESH_MEMORY_REQUIRE_REASON is off; the service layer, not the
	// repository, owns that policy.
	Reason string

	// ExpectedVersion, when non-nil, makes the write conditional: it succeeds
	// only if the stored version still equals this value. Nil means
	// last-write-wins, which is the pre-existing behaviour and stays the
	// default so that callers who have not opted in are unaffected.
	ExpectedVersion *int

	// ActorAgentID is who is writing. Nil for system paths with no agent
	// identity (reconcilers, imports).
	ActorAgentID *uuid.UUID
}

// MemoryVersionConflictError is returned when a conditional write loses a race:
// the stored version no longer matches what the caller expected.
//
// It names both numbers because "conflict" alone leaves the caller unable to
// decide what to do next, and re-reading to find out is another round trip that
// can itself be raced.
type MemoryVersionConflictError struct {
	Key      string
	Expected int
	Actual   int
}

func (e *MemoryVersionConflictError) Error() string {
	return fmt.Sprintf(
		"memory %q was modified by someone else: you expected version %d, stored version is %d; re-read it and re-apply your change",
		e.Key, e.Expected, e.Actual,
	)
}
