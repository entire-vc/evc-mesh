package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// keySlugRegex matches valid memory keys: lowercase alphanumeric with hyphens,
// starting and ending with an alphanumeric character, at least two characters long.
var keySlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// defaultMinImportance is the threshold applied to recall queries when the caller
// does not supply an explicit min_importance. Entries below this score are noise.
const defaultMinImportance float32 = 0.4

// entityKeywords are canonical domain terms that boost importance_score when found
// in memory content, signalling higher decision-relevance.
var entityKeywords = []string{"icp", "architecture", "license", "security", "money"}

// shortIDRegex matches lowercase hex strings of 6–12 characters (word-bounded) that may
// reference another memory by its short UUID prefix in an incident memory body.
var shortIDRegex = regexp.MustCompile(`\b([0-9a-f]{6,12})\b`)

// computeImportanceScore derives an importance_score for a memory based on its tags
// and content. The score is in [0, 1]. Scoring rules (additive, capped at 1.0):
//
//	Base by kind: tag:
//	  kind:incident          → 0.85
//	  kind:decision          → 0.80
//	  kind:learning          → 0.70
//	  kind:fact              → 0.60
//	  kind:session-checkpoint → 0.30
//	  (no kind: tag)         → 0.50
//
//	+0.10 boost if content mentions a canonical entity keyword (icp, architecture, etc.)
//	+0.10 boost if any tag matches relevance:0.8+ (explicit agent override)
func computeImportanceScore(tags []string, content string) float32 {
	base := float32(0.5)
	isSessionCheckpoint := false
	for _, tag := range tags {
		switch tag {
		case "kind:incident":
			if base < 0.85 {
				base = 0.85
			}
		case "kind:decision":
			if base < 0.80 {
				base = 0.80
			}
		case "kind:learning":
			if base < 0.70 {
				base = 0.70
			}
		case "kind:fact":
			if base < 0.60 {
				base = 0.60
			}
		case "kind:session-checkpoint":
			isSessionCheckpoint = true
		}
	}
	// session-checkpoint is a downgrade: overrides positive kind: tags.
	// An entry tagged session-checkpoint is low-value regardless of other kind: tags.
	if isSessionCheckpoint {
		base = 0.30
	}

	score := base
	lower := strings.ToLower(content)
	for _, kw := range entityKeywords {
		if strings.Contains(lower, kw) {
			score += 0.10
			break
		}
	}

	for _, tag := range tags {
		if strings.HasPrefix(tag, "relevance:") {
			val := strings.TrimPrefix(tag, "relevance:")
			var f float64
			if _, err := fmt.Sscanf(val, "%f", &f); err == nil && f >= 0.8 {
				score += 0.10
				break
			}
		}
	}

	if score > 1.0 {
		score = 1.0
	}
	return score
}

// tagOverlapRatio returns the fraction of newTags found in existingTags.
// Used for reinforcement scoring: when ≥80% of tags match, the memory is
// considered a re-assertion of the same knowledge and gets a score boost.
func tagOverlapRatio(existingTags, newTags []string) float64 {
	if len(newTags) == 0 {
		return 0
	}
	existSet := make(map[string]struct{}, len(existingTags))
	for _, t := range existingTags {
		existSet[t] = struct{}{}
	}
	matches := 0
	for _, t := range newTags {
		if _, ok := existSet[t]; ok {
			matches++
		}
	}
	return float64(matches) / float64(len(newTags))
}

// extractShortIDs scans content for short UUID references (6–12 lowercase hex chars, word-bounded).
// Returns deduplicated matches in order of first appearance.
func extractShortIDs(content string) []string {
	matches := shortIDRegex.FindAllStringSubmatch(content, -1)
	seen := make(map[string]struct{}, len(matches))
	result := make([]string, 0, len(matches))
	for _, m := range matches {
		id := m[1]
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

// rrfK is the Reciprocal Rank Fusion constant. 60 is the standard value.
const rrfK = 60

// rrfVectorWeight and rrfTextWeight control the relative contribution of vector vs keyword results.
const (
	rrfVectorWeight = 0.7
	rrfTextWeight   = 0.3
)

// temporalHalfLifeDays is the half-life for the temporal decay applied to agent-scope memories.
// Project- and workspace-scoped memories are exempt (they represent persistent knowledge).
const temporalHalfLifeDays = 30.0

// candidateMultiplier controls how many extra candidates are fetched for re-ranking.
// FullTextSearch and VectorSearch each fetch limit * candidateMultiplier results.
const candidateMultiplier = 3

type memoryService struct {
	memRepo  repository.MemoryRepository
	edgeRepo repository.MemoryEdgeRepository
	embedder embedding.Embedder
}

// NewMemoryService returns a new MemoryService.
// embedder may be embedding.NewNoopEmbedder() when vector search is not configured;
// all vector operations are skipped gracefully in that case.
func NewMemoryService(memRepo repository.MemoryRepository, edgeRepo repository.MemoryEdgeRepository, embedder embedding.Embedder) MemoryService {
	if embedder == nil {
		embedder = embedding.NewNoopEmbedder()
	}
	return &memoryService{memRepo: memRepo, edgeRepo: edgeRepo, embedder: embedder}
}

// Remember upserts a memory entry. It returns "created" if the key did not exist before,
// or "updated" if an existing entry was overwritten.
// After a successful upsert, it asynchronously embeds the content when an embedder is configured.
func (s *memoryService) Remember(ctx context.Context, mem *domain.Memory) (string, error) {
	if mem.Key == "" {
		return "", apierror.ValidationError(map[string]string{
			"key": "key is required",
		})
	}
	if !keySlugRegex.MatchString(mem.Key) {
		return "", apierror.ValidationError(map[string]string{
			"key": "key must match pattern ^[a-z0-9][a-z0-9-]*[a-z0-9]$ (lowercase alphanumeric with hyphens)",
		})
	}
	if mem.Content == "" {
		return "", apierror.ValidationError(map[string]string{
			"content": "content is required",
		})
	}
	if mem.WorkspaceID == uuid.Nil {
		return "", apierror.ValidationError(map[string]string{
			"workspace_id": "workspace_id is required",
		})
	}

	// Validate relevance ∈ [0, 1].
	if mem.Relevance < 0 || mem.Relevance > 1 {
		return "", apierror.ValidationError(map[string]string{
			"relevance": "relevance must be between 0 and 1",
		})
	}
	// Default relevance for new memories.
	if mem.Relevance == 0 {
		mem.Relevance = 1.0
	}

	// Validate tags.
	if len(mem.Tags) > 20 {
		return "", apierror.ValidationError(map[string]string{
			"tags": "maximum 20 tags allowed",
		})
	}
	for _, tag := range mem.Tags {
		if len(tag) > 64 {
			return "", apierror.ValidationError(map[string]string{
				"tags": "each tag must be 64 characters or fewer",
			})
		}
	}

	// Validate expires_at: if provided it must be in the future.
	if mem.ExpiresAt != nil && !mem.ExpiresAt.After(time.Now()) {
		return "", apierror.ValidationError(map[string]string{
			"expires_at": "expires_at must be in the future",
		})
	}

	// Apply default expires_at policy when not explicitly provided.
	if mem.ExpiresAt == nil {
		mem.ExpiresAt = defaultExpiresAt(mem.Scope, mem.Tags)
	}

	// Determine whether this is a create or update by checking for an existing entry.
	existing, err := s.memRepo.GetByKey(ctx, mem.WorkspaceID, mem.ProjectID, mem.AgentID, mem.Key, mem.Scope)
	if err != nil {
		return "", fmt.Errorf("memory remember: lookup existing: %w", err)
	}

	outcome := "created"
	if existing != nil {
		outcome = "updated"
		// Preserve the original ID so the upsert constraint matches.
		mem.ID = existing.ID
	}

	// Compute importance_score when not explicitly set by the caller.
	if mem.ImportanceScore == 0 {
		mem.ImportanceScore = computeImportanceScore(mem.Tags, mem.Content)
	}
	// Reinforcement: if re-asserting a known memory (≥80% tag overlap), boost the score.
	if existing != nil && tagOverlapRatio(existing.Tags, mem.Tags) >= 0.8 {
		mem.ImportanceScore += 0.1
		if mem.ImportanceScore > 1.0 {
			mem.ImportanceScore = 1.0
		}
	}

	if err := s.memRepo.Upsert(ctx, mem); err != nil {
		return "", fmt.Errorf("memory remember: upsert: %w", err)
	}

	// Async embedding — fire and forget, non-fatal.
	if !embedding.IsNoop(s.embedder) {
		memID := mem.ID
		content := mem.Key + " " + mem.Content + " " + strings.Join(mem.Tags, " ")
		go s.embedAndStore(memID, content)
	}

	// ── Hook 1: relates_to edge on tag overlap ≥60% ───────────────────────────
	if s.edgeRepo != nil && len(mem.Tags) > 0 {
		if candidates, scanErr := s.memRepo.FindByScope(ctx, mem.WorkspaceID, mem.ProjectID, string(mem.Scope), 200); scanErr == nil {
			for _, candidate := range candidates {
				if candidate.ID == mem.ID {
					continue
				}
				if tagOverlapRatio([]string(candidate.Tags), mem.Tags) >= 0.6 {
					edge := &domain.MemoryEdge{
						MemoryFromID:     mem.ID,
						MemoryToID:       candidate.ID,
						RelationshipType: domain.EdgeRelatesTo,
						Weight:           0.5,
						WorkspaceID:      mem.WorkspaceID,
					}
					_ = s.edgeRepo.UpsertEdge(ctx, edge)
				}
			}
		}
	}

	// ── Hook 3: derived_from edge when kind:incident memory references another memory ──
	if s.edgeRepo != nil {
		hasIncidentTag := false
		for _, t := range mem.Tags {
			if t == "kind:incident" || t == "incident" {
				hasIncidentTag = true
				break
			}
		}
		if hasIncidentTag {
			for _, ref := range extractShortIDs(mem.Content) {
				target, findErr := s.memRepo.FindByShortID(ctx, mem.WorkspaceID, ref)
				if findErr != nil || target == nil || target.ID == mem.ID {
					continue
				}
				edge := &domain.MemoryEdge{
					MemoryFromID:     mem.ID,
					MemoryToID:       target.ID,
					RelationshipType: domain.EdgeDerivedFrom,
					Weight:           1.0,
					WorkspaceID:      mem.WorkspaceID,
				}
				_ = s.edgeRepo.UpsertEdge(ctx, edge)
			}
		}
	}

	return outcome, nil
}

// defaultExpiresAt applies the server-side TTL policy when the caller does not supply expires_at.
//
//	if 'session-checkpoint' in tags → now + 7d
//	if scope == 'agent' → now + 180d
//	if scope == 'project' → now + 180d (unless 'permanent' in tags → nil)
//	if scope == 'workspace' → nil (never expires)
func defaultExpiresAt(scope domain.MemoryScope, tags []string) *time.Time {
	hasTag := func(needle string) bool {
		for _, t := range tags {
			if t == needle {
				return true
			}
		}
		return false
	}

	if hasTag("session-checkpoint") {
		t := time.Now().Add(7 * 24 * time.Hour)
		return &t
	}
	switch scope {
	case domain.ScopeAgent:
		t := time.Now().Add(180 * 24 * time.Hour)
		return &t
	case domain.ScopeProject:
		if hasTag("permanent") {
			return nil
		}
		t := time.Now().Add(180 * 24 * time.Hour)
		return &t
	default: // workspace — never expires
		return nil
	}
}

// embedAndStore embeds text and persists the resulting vector for the given memory ID.
// Called asynchronously from Remember; errors are logged but never surfaced to callers.
func (s *memoryService) embedAndStore(id uuid.UUID, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vec, err := s.embedder.Embed(ctx, text)
	if err != nil {
		log.Printf("memory embed: id=%s error=%v", id, err)
		return
	}
	if len(vec) == 0 {
		return
	}
	if err := s.memRepo.UpdateEmbedding(ctx, id, vec, s.embedder.Model(), s.embedder.Dimensions()); err != nil {
		log.Printf("memory embed store: id=%s error=%v", id, err)
	}
}

// RecallResult holds the paginated recall response with metadata.
type RecallResult struct {
	Items        []domain.ScoredMemory
	Total        int
	Limit        int
	Offset       int
	DecayApplied bool
}

// Recall performs a hybrid search (keyword + optional vector) and returns ranked results.
//
// Algorithm:
//  1. Always: full-text keyword search via tsvector (ts_rank_cd).
//  2. If embedder configured: embed query → vector similarity search.
//  3. Merge both result sets using Reciprocal Rank Fusion (RRF).
//  4. Apply temporal decay (half-life 30d) — agent-scope memories only.
//  5. Boost relevance of returned memories as positive feedback (non-fatal).
//
// When extended filter params are present (TagsAny, CreatedBy, Since, Until, etc.),
// the repository List method is used instead of FullTextSearch for precise SQL filtering.
func (s *memoryService) Recall(ctx context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, error) {
	if opts.Query == "" {
		return nil, apierror.ValidationError(map[string]string{
			"q": "search query is required",
		})
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	var projID *uuid.UUID
	if opts.ProjectID != uuid.Nil {
		projID = &opts.ProjectID
	}

	poolSize := opts.Limit * candidateMultiplier

	// ── Step 1: Keyword search (always available) ──────────────────────────────
	kwResults, err := s.memRepo.FullTextSearch(ctx, opts.Query, opts.WorkspaceID, projID, string(opts.Scope), opts.Tags, poolSize)
	if err != nil {
		return nil, fmt.Errorf("memory recall: full text search: %w", err)
	}

	// ── Step 2: Vector search (only when embedder is configured) ───────────────
	var vecResults []domain.ScoredMemory
	if !embedding.IsNoop(s.embedder) {
		queryVec, embedErr := s.embedder.Embed(ctx, opts.Query)
		if embedErr != nil {
			// Graceful degradation: log and fall back to keyword-only.
			log.Printf("memory recall: embedding failed, using keyword-only: %v", embedErr)
		} else if len(queryVec) > 0 {
			vecResults, err = s.memRepo.VectorSearch(ctx, queryVec, opts.WorkspaceID, projID, string(opts.Scope), opts.Tags, poolSize)
			if err != nil {
				// Graceful degradation: log and continue with keyword results only.
				log.Printf("memory recall: vector search failed, using keyword-only: %v", err)
				vecResults = nil
			}
		}
	}

	// ── Step 3: RRF merge ─────────────────────────────────────────────────────
	merged := reciprocalRankFusion(kwResults, vecResults)

	// ── Step 4: Apply extended filters (TagsAny, CreatedBy, Since, Until, etc.) ─
	// Apply the default importance_score threshold when the caller hasn't overridden it.
	if opts.MinImportance == nil {
		def := defaultMinImportance
		opts.MinImportance = &def
	}
	if opts.CreatedBy != nil || opts.SourceType != "" || len(opts.TagsAny) > 0 || opts.Since != nil || opts.Until != nil || opts.RelevanceMin != nil || opts.MinImportance != nil {
		merged = applyExtendedFilters(merged, opts)
	}

	// ── Step 5: Temporal decay ────────────────────────────────────────────────
	now := time.Now()
	lambda := math.Log(2) / temporalHalfLifeDays

	for i := range merged {
		m := &merged[i]
		// Project- and workspace-scoped memories represent persistent knowledge
		// (analogous to MEMORY.md in OpenClaw) and are exempt from temporal decay.
		if m.Scope == domain.ScopeProject || m.Scope == domain.ScopeWorkspace {
			continue
		}
		ageDays := now.Sub(m.UpdatedAt).Hours() / 24
		decay := math.Exp(-lambda * ageDays)
		m.Score *= decay
	}

	// Re-sort after decay adjustment.
	slices.SortFunc(merged, func(a, b domain.ScoredMemory) int {
		return cmp.Compare(b.Score, a.Score)
	})

	// ── Step 6: Trim to requested limit ───────────────────────────────────────
	if len(merged) > opts.Limit {
		merged = merged[:opts.Limit]
	}

	// ── Boost relevance as positive feedback (non-fatal) ─────────────────────
	if len(merged) > 0 {
		ids := make([]uuid.UUID, len(merged))
		for i, r := range merged {
			ids[i] = r.ID
		}
		_ = s.memRepo.BoostRelevance(ctx, ids)
	}

	return merged, nil
}

// applyExtendedFilters filters a slice of ScoredMemory using the extended RecallOpts fields
// that are not handled by FullTextSearch (TagsAny, CreatedBy, Since, Until, RelevanceMin).
func applyExtendedFilters(items []domain.ScoredMemory, opts domain.RecallOpts) []domain.ScoredMemory {
	out := items[:0]
	for _, m := range items {
		if opts.CreatedBy != nil {
			if m.AgentID == nil || *m.AgentID != *opts.CreatedBy {
				continue
			}
		}
		if opts.SourceType != "" && m.SourceType != opts.SourceType {
			continue
		}
		if opts.Since != nil && m.CreatedAt.Before(*opts.Since) {
			continue
		}
		if opts.Until != nil && m.CreatedAt.After(*opts.Until) {
			continue
		}
		if opts.RelevanceMin != nil && m.Relevance < *opts.RelevanceMin {
			continue
		}
		if opts.MinImportance != nil && m.ImportanceScore < *opts.MinImportance {
			continue
		}
		if len(opts.TagsAny) > 0 {
			found := false
			for _, required := range opts.TagsAny {
				for _, tag := range m.Tags {
					if tag == required {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// ListMemories returns a richly-filtered, paginated list of memories using the repository
// List method. Unlike Recall, this path does not perform hybrid search — it delegates all
// filtering and ordering to the database.
func (s *memoryService) ListMemories(ctx context.Context, filter domain.MemoryListFilter) (*RecallResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 20
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}

	result, err := s.memRepo.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("memory list: %w", err)
	}

	return &RecallResult{
		Items:        result.Items,
		Total:        result.Total,
		Limit:        filter.Limit,
		Offset:       filter.Offset,
		DecayApplied: result.DecayApplied,
	}, nil
}

// reciprocalRankFusion merges keyword and vector result lists using RRF scoring.
// The formula is: score(d) = kwW/(k+rank_kw) + vecW/(k+rank_vec)
// where k=60 is the standard RRF constant.
func reciprocalRankFusion(kw, vec []domain.ScoredMemory) []domain.ScoredMemory {
	type entry struct {
		mem   domain.Memory
		score float64
	}
	scores := make(map[uuid.UUID]*entry)

	for rank, m := range kw {
		id := m.ID
		if _, ok := scores[id]; !ok {
			mc := m.Memory
			scores[id] = &entry{mem: mc}
		}
		scores[id].score += rrfTextWeight * (1.0 / (float64(rrfK) + float64(rank+1)))
	}

	for rank, m := range vec {
		id := m.ID
		if _, ok := scores[id]; !ok {
			mc := m.Memory
			scores[id] = &entry{mem: mc}
		}
		scores[id].score += rrfVectorWeight * (1.0 / (float64(rrfK) + float64(rank+1)))
	}

	result := make([]domain.ScoredMemory, 0, len(scores))
	for _, e := range scores {
		result = append(result, domain.ScoredMemory{
			Memory: e.mem,
			Score:  e.score,
		})
	}
	slices.SortFunc(result, func(a, b domain.ScoredMemory) int {
		return cmp.Compare(b.Score, a.Score)
	})
	return result
}

// GetProjectKnowledge returns all non-expired memories for a workspace (and optional project).
func (s *memoryService) GetProjectKnowledge(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]domain.Memory, error) {
	return s.memRepo.ListByWorkspaceProject(ctx, workspaceID, projectID)
}

// SetProjectKnowledge upserts a project-scoped knowledge entry by key.
// Category is stored as a "category:{value}" tag. Returns the upserted memory and outcome.
func (s *memoryService) SetProjectKnowledge(ctx context.Context, input SetProjectKnowledgeInput) (*domain.Memory, string, error) {
	if input.Key == "" {
		return nil, "", apierror.ValidationError(map[string]string{
			"key": "key is required",
		})
	}
	if len(input.Key) > 80 {
		return nil, "", apierror.ValidationError(map[string]string{
			"key": "key must be 80 characters or fewer",
		})
	}
	if !keySlugRegex.MatchString(input.Key) {
		return nil, "", apierror.ValidationError(map[string]string{
			"key": "key must match pattern ^[a-z0-9][a-z0-9-]*[a-z0-9]$ (lowercase alphanumeric with hyphens)",
		})
	}
	if input.Value == "" {
		return nil, "", apierror.ValidationError(map[string]string{
			"value": "value is required",
		})
	}
	if len(input.Value) > 4000 {
		return nil, "", apierror.ValidationError(map[string]string{
			"value": "value must be 4000 characters or fewer",
		})
	}

	// Build tags: caller-supplied + optional category tag.
	tags := make([]string, 0, len(input.Tags)+1)
	tags = append(tags, input.Tags...)
	if input.Category != "" {
		tags = append(tags, "category:"+input.Category)
	}

	sourceType := domain.MemorySourceType(input.SourceType)
	if sourceType == "" {
		sourceType = domain.SourceAgent
	}

	projID := &input.ProjectID
	mem := &domain.Memory{
		WorkspaceID: input.WorkspaceID,
		ProjectID:   projID,
		AgentID:     input.AgentID,
		Key:         input.Key,
		Content:     input.Value,
		Scope:       domain.ScopeProject,
		Tags:        tags,
		SourceType:  sourceType,
		SourceURL:   input.SourceURL,
		Relevance:   1.0,
	}

	outcome, err := s.Remember(ctx, mem)
	if err != nil {
		return nil, "", fmt.Errorf("set_project_knowledge: %w", err)
	}

	return mem, outcome, nil
}

// Forget deletes a memory by ID. Agents may only delete their own agent-scope memories.
// Admins (isAdmin=true) may delete any memory.
func (s *memoryService) Forget(ctx context.Context, id uuid.UUID, actorAgentID *uuid.UUID, isAdmin bool) error {
	mem, err := s.memRepo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("memory forget: get by id: %w", err)
	}
	if mem == nil {
		return apierror.NotFound("Memory")
	}

	if !isAdmin {
		// Non-admin agents may only delete memories they created (matching agent_id).
		if actorAgentID == nil {
			return apierror.Forbidden("only admins can delete memories created by other actors")
		}
		if mem.AgentID == nil || *mem.AgentID != *actorAgentID {
			return apierror.Forbidden("agents may only delete their own memories")
		}
	}

	if err := s.memRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("memory forget: delete: %w", err)
	}
	return nil
}

// GetByID returns a single memory by primary key.
func (s *memoryService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Memory, error) {
	mem, err := s.memRepo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("memory get by id: %w", err)
	}
	if mem == nil {
		return nil, apierror.NotFound("Memory")
	}
	return mem, nil
}

// ExtractFromEvent inspects an event and its optional hint to decide whether to
// persist a memory. When hint.Persist is true, memory fields come from the hint.
// For auto-extract: context_update events whose payload contains context_type of
// "decision", "instruction", or "reference" are automatically stored.
func (s *memoryService) ExtractFromEvent(ctx context.Context, event *domain.EventBusMessage, hint *domain.MemoryHint) error {
	if event == nil {
		return nil
	}

	var mem *domain.Memory

	if hint != nil && hint.Persist {
		// Explicit persist request from hint.
		expiresAt, _ := parseDuration(hint.ExpiresIn)

		mem = &domain.Memory{
			WorkspaceID:   event.WorkspaceID,
			ProjectID:     &event.ProjectID,
			AgentID:       event.AgentID,
			Key:           hint.Key,
			Content:       event.Subject,
			Scope:         hint.Scope,
			Tags:          hint.Tags,
			SourceType:    domain.SourceAgent,
			SourceEventID: &event.ID,
			Relevance:     0.5,
			ExpiresAt:     expiresAt,
		}

		// Auto-generate key from subject if not provided.
		if mem.Key == "" {
			mem.Key = memorySlugify(event.Subject)
		}
	} else if string(event.EventType) == "context_update" {
		// Auto-extract from context_update events.
		mem = s.autoExtractFromContextUpdate(event)
	}

	if mem == nil {
		return nil
	}

	if mem.Key == "" {
		mem.Key = memorySlugify(event.Subject)
	}
	if mem.Key == "" || !keySlugRegex.MatchString(mem.Key) {
		// Cannot produce a valid key — skip silently.
		return nil
	}

	if err := s.memRepo.Upsert(ctx, mem); err != nil {
		return fmt.Errorf("memory extract from event: upsert: %w", err)
	}

	// Async embedding for auto-extracted memories.
	if mem.ID != uuid.Nil && !embedding.IsNoop(s.embedder) {
		memID := mem.ID
		content := mem.Key + " " + mem.Content + " " + strings.Join(mem.Tags, " ")
		go s.embedAndStore(memID, content)
	}

	return nil
}

// autoExtractFromContextUpdate auto-creates a memory when a context_update event
// has a payload with context_type = decision | instruction | reference.
func (s *memoryService) autoExtractFromContextUpdate(event *domain.EventBusMessage) *domain.Memory {
	if len(event.Payload) == 0 {
		return nil
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil
	}

	contextType, _ := payload["context_type"].(string)
	switch contextType {
	case "decision", "instruction", "reference":
		// Valid auto-extract type.
	default:
		return nil
	}

	key := memorySlugify(event.Subject)
	if key == "" || !keySlugRegex.MatchString(key) {
		return nil
	}

	return &domain.Memory{
		WorkspaceID:   event.WorkspaceID,
		ProjectID:     &event.ProjectID,
		AgentID:       event.AgentID,
		Key:           key,
		Content:       event.Subject,
		Scope:         domain.ScopeProject,
		SourceType:    domain.SourceSystem,
		SourceEventID: &event.ID,
		Relevance:     0.3,
	}
}

// memorySlugify converts an arbitrary string into a valid memory key slug.
// It lowercases the input, replaces non-alphanumeric runs with a single hyphen,
// trims leading/trailing hyphens, and truncates to 100 characters.
// This is distinct from the agent-service slugify which does not collapse runs.
func memorySlugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}

	result := strings.Trim(b.String(), "-")
	if len(result) > 100 {
		result = result[:100]
		result = strings.TrimRight(result, "-")
	}
	return result
}

// parseDuration parses a Go duration string (e.g. "72h") and returns an expiry time pointer.
// Returns nil when input is empty. Returns an error for invalid formats.
func parseDuration(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, err
	}
	t := time.Now().Add(d)
	return &t, nil
}

// memoryExportItem is the YAML representation of a single memory for export/import.
type memoryExportItem struct {
	Key        string   `yaml:"key"`
	Content    string   `yaml:"content"`
	Scope      string   `yaml:"scope"`
	ProjectID  string   `yaml:"project_id,omitempty"`
	Tags       []string `yaml:"tags,omitempty"`
	SourceType string   `yaml:"source_type"`
}

// memoryExportDoc is the top-level YAML document structure.
type memoryExportDoc struct {
	Version  string             `yaml:"version"`
	Memories []memoryExportItem `yaml:"memories"`
}

// ExportMemories serialises all non-expired memories for the workspace (optionally
// filtered to a single project) as a YAML document.
func (s *memoryService) ExportMemories(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]byte, error) {
	memories, err := s.memRepo.ListByWorkspaceProject(ctx, workspaceID, projectID)
	if err != nil {
		return nil, fmt.Errorf("memory export: list: %w", err)
	}

	doc := memoryExportDoc{
		Version:  "1",
		Memories: make([]memoryExportItem, 0, len(memories)),
	}
	for _, m := range memories {
		item := memoryExportItem{
			Key:        m.Key,
			Content:    m.Content,
			Scope:      string(m.Scope),
			SourceType: string(m.SourceType),
			Tags:       m.Tags,
		}
		if m.ProjectID != nil {
			item.ProjectID = m.ProjectID.String()
		}
		doc.Memories = append(doc.Memories, item)
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("memory export: marshal yaml: %w", err)
	}
	return data, nil
}

// ImportMemories parses a YAML export document and upserts each memory entry.
// Returns the count of successfully imported memories.
func (s *memoryService) ImportMemories(ctx context.Context, workspaceID uuid.UUID, data []byte) (int, error) {
	var doc memoryExportDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return 0, apierror.BadRequest("invalid YAML: " + err.Error())
	}

	count := 0
	for _, item := range doc.Memories {
		if item.Key == "" || item.Content == "" {
			continue
		}
		if !keySlugRegex.MatchString(item.Key) {
			continue
		}

		scope := domain.MemoryScope(item.Scope)
		if scope == "" {
			scope = domain.ScopeWorkspace
		}

		var projID *uuid.UUID
		if item.ProjectID != "" {
			pid, err := uuid.Parse(item.ProjectID)
			if err == nil {
				projID = &pid
			}
		}

		sourceType := domain.MemorySourceType(item.SourceType)
		if sourceType == "" {
			sourceType = domain.SourceHuman
		}

		mem := &domain.Memory{
			WorkspaceID: workspaceID,
			ProjectID:   projID,
			Key:         item.Key,
			Content:     item.Content,
			Scope:       scope,
			Tags:        item.Tags,
			SourceType:  sourceType,
			Relevance:   0.5,
		}

		if err := s.memRepo.Upsert(ctx, mem); err != nil {
			log.Printf("memory import: upsert key=%s: %v", item.Key, err)
			continue
		}
		count++
	}

	return count, nil
}

// BatchEmbed finds all memories without an embedding vector and embeds them
// using the configured embedder. It is a no-op when the embedder is the noop variant.
// Returns the count of memories successfully embedded.
func (s *memoryService) BatchEmbed(ctx context.Context, workspaceID uuid.UUID) (int, error) {
	if embedding.IsNoop(s.embedder) {
		return 0, nil
	}

	const batchSize = 100
	memories, err := s.memRepo.ListWithNullEmbedding(ctx, workspaceID, batchSize)
	if err != nil {
		return 0, fmt.Errorf("memory batch embed: list: %w", err)
	}

	count := 0
	for _, m := range memories {
		text := m.Key + " " + m.Content + " " + strings.Join(m.Tags, " ")
		vec, embedErr := s.embedder.Embed(ctx, text)
		if embedErr != nil {
			log.Printf("memory batch embed: embed id=%s: %v", m.ID, embedErr)
			continue
		}
		if len(vec) == 0 {
			continue
		}
		if storeErr := s.memRepo.UpdateEmbedding(ctx, m.ID, vec, s.embedder.Model(), s.embedder.Dimensions()); storeErr != nil {
			log.Printf("memory batch embed: store id=%s: %v", m.ID, storeErr)
			continue
		}
		count++
	}

	return count, nil
}

// FindRelated returns memories related to the given memory by performing a full-text
// search using the memory's key and tags as the query. The source memory itself is excluded.
func (s *memoryService) FindRelated(ctx context.Context, memoryID uuid.UUID, limit int) ([]domain.ScoredMemory, error) {
	if limit <= 0 {
		limit = 5
	}

	mem, err := s.memRepo.GetByID(ctx, memoryID)
	if err != nil {
		return nil, fmt.Errorf("memory find related: get by id: %w", err)
	}
	if mem == nil {
		return nil, apierror.NotFound("Memory")
	}

	// Build query from key and tags.
	parts := []string{mem.Key}
	parts = append(parts, mem.Tags...)
	query := strings.Join(parts, " ")

	var projID *uuid.UUID
	if mem.ProjectID != nil {
		projID = mem.ProjectID
	}

	// Fetch slightly more than limit to account for excluding the source memory.
	results, err := s.memRepo.FullTextSearch(ctx, query, mem.WorkspaceID, projID, "", nil, limit+1)
	if err != nil {
		return nil, fmt.Errorf("memory find related: search: %w", err)
	}

	// Exclude the source memory.
	filtered := make([]domain.ScoredMemory, 0, len(results))
	for _, r := range results {
		if r.ID == memoryID {
			continue
		}
		filtered = append(filtered, r)
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	return filtered, nil
}

// Supersede creates a 'supersedes' edge from newID → oldID and marks oldID as archived.
// Hook 2 of the F1-s2 automatic edge-creation spec.
func (s *memoryService) Supersede(ctx context.Context, oldID, newID uuid.UUID) error {
	old, err := s.memRepo.GetByID(ctx, oldID)
	if err != nil {
		return fmt.Errorf("memory supersede: get old: %w", err)
	}
	if old == nil {
		return apierror.NotFound("Memory")
	}
	newMem, err := s.memRepo.GetByID(ctx, newID)
	if err != nil {
		return fmt.Errorf("memory supersede: get new: %w", err)
	}
	if newMem == nil {
		return apierror.NotFound("Memory")
	}

	edge := &domain.MemoryEdge{
		MemoryFromID:     newID,
		MemoryToID:       oldID,
		RelationshipType: domain.EdgeSupersedes,
		Weight:           1.0,
		WorkspaceID:      old.WorkspaceID,
	}
	if err := s.edgeRepo.UpsertEdge(ctx, edge); err != nil {
		return fmt.Errorf("memory supersede: create edge: %w", err)
	}

	if err := s.memRepo.SetArchived(ctx, oldID, true); err != nil {
		return fmt.Errorf("memory supersede: archive old: %w", err)
	}

	return nil
}

// ── RecallGraph ───────────────────────────────────────────────────────────────

// recallGraphCacheTTL is the in-process TTL for RecallGraph results.
const recallGraphCacheTTL = 5 * time.Minute

// recallGraphCacheEntry holds a cached RecallGraph result with its expiry time.
type recallGraphCacheEntry struct {
	results   []domain.RecallGraphResult
	expiresAt time.Time
}

// recallGraphCache is a package-level in-process cache for RecallGraph results.
// Key: "<taskID>:<queryHash>" — see recallGraphCacheKey.
var recallGraphCache sync.Map //nolint:gochecknoglobals // intentional package-level cache

// recallGraphCacheKey builds the cache key from the optional taskID and the SHA-256
// of the serialised RecallGraphOpts fields that affect query results.
func recallGraphCacheKey(opts domain.RecallGraphOpts) string {
	taskPart := "notask"
	if opts.TaskID != nil {
		taskPart = opts.TaskID.String()
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%d|%f",
		opts.Query,
		opts.WorkspaceID.String(),
		func() string {
			if opts.ProjectID != nil {
				return opts.ProjectID.String()
			}
			return ""
		}(),
		opts.Hops,
		opts.WeightThreshold,
	)
	return taskPart + ":" + hex.EncodeToString(h.Sum(nil))
}

// graphMinImportance is the minimum importance_score required for graph-expanded (non-seed) memories.
const graphMinImportance float32 = 0.4

// recallGraphSeedLimit is the number of seeds fetched from hybrid recall.
const recallGraphSeedLimit = 10

// RecallGraph performs a multi-hop knowledge-graph traversal.
//
// Algorithm:
//  1. Seed: hybrid recall (keyword + optional vector) → top recallGraphSeedLimit memories.
//  2. BFS: expand along memory_edges bidirectionally for up to opts.Hops levels,
//     only following edges with weight >= opts.WeightThreshold.
//  3. Deduplicate by memory ID (seeds take priority).
//  4. Rank by composite score: seed_score × Π(edge_weight along the chain from seed).
//  5. Drop graph-expanded memories (provenance="via:graph") with importance_score < 0.4.
//  6. Cache results for recallGraphCacheTTL keyed by (taskID, queryHash).
func (s *memoryService) RecallGraph(ctx context.Context, opts domain.RecallGraphOpts) ([]domain.RecallGraphResult, error) {
	if opts.Query == "" {
		return nil, apierror.ValidationError(map[string]string{
			"query": "query is required",
		})
	}
	if opts.Hops <= 0 {
		opts.Hops = 2
	}
	if opts.Hops > 5 {
		opts.Hops = 5 // safety cap
	}
	if opts.WeightThreshold <= 0 {
		opts.WeightThreshold = 0.1
	}

	cacheKey := recallGraphCacheKey(opts)
	now := time.Now()

	// Check cache.
	if raw, ok := recallGraphCache.Load(cacheKey); ok {
		entry := raw.(recallGraphCacheEntry)
		if now.Before(entry.expiresAt) {
			return entry.results, nil
		}
		recallGraphCache.Delete(cacheKey)
	}

	// ── Step 1: Seed via hybrid recall ─────────────────────────────────────────
	var projID *uuid.UUID
	if opts.ProjectID != nil && *opts.ProjectID != uuid.Nil {
		projID = opts.ProjectID
	}

	seedOpts := domain.RecallOpts{
		Query:       opts.Query,
		WorkspaceID: opts.WorkspaceID,
		Limit:       recallGraphSeedLimit,
	}
	if projID != nil {
		seedOpts.ProjectID = *projID
	}

	seedResults, err := s.Recall(ctx, seedOpts)
	if err != nil {
		return nil, fmt.Errorf("recall graph: seed recall: %w", err)
	}

	if len(seedResults) == 0 {
		return nil, nil
	}

	// Build result map keyed by memory ID. Seeds are "via:recall" at hop 0.
	type nodeInfo struct {
		result         domain.RecallGraphResult
		compositeScore float64
	}
	nodes := make(map[uuid.UUID]*nodeInfo, len(seedResults)*4)

	for _, sm := range seedResults {
		nodes[sm.ID] = &nodeInfo{
			result: domain.RecallGraphResult{
				ID:              sm.ID,
				Content:         sm.Content,
				ImportanceScore: sm.ImportanceScore,
				CompositeScore:  sm.Score,
				Provenance:      domain.ProvenanceRecall,
				HopDistance:     0,
			},
			compositeScore: sm.Score,
		}
	}

	// ── Step 2: BFS expansion ──────────────────────────────────────────────────
	// frontier holds the IDs processed in the current BFS level.
	frontier := make([]uuid.UUID, 0, len(seedResults))
	for id := range nodes {
		frontier = append(frontier, id)
	}

	for hop := 1; hop <= opts.Hops && len(frontier) > 0; hop++ {
		edges, edgeErr := s.edgeRepo.GetNeighbors(ctx, frontier, opts.WeightThreshold)
		if edgeErr != nil {
			return nil, fmt.Errorf("recall graph: get neighbors hop %d: %w", hop, edgeErr)
		}

		nextFrontier := make([]uuid.UUID, 0)

		for _, edge := range edges {
			// Determine which end is the known node and which is the new neighbor.
			var knownID, neighborID uuid.UUID
			for _, fid := range frontier {
				if fid == edge.MemoryFromID {
					knownID = fid
					neighborID = edge.MemoryToID
					break
				}
				if fid == edge.MemoryToID {
					knownID = fid
					neighborID = edge.MemoryFromID
					break
				}
			}
			if knownID == uuid.Nil {
				continue
			}

			known, ok := nodes[knownID]
			if !ok {
				continue
			}

			// Composite score = parent composite × this edge weight.
			newScore := known.compositeScore * float64(edge.Weight)

			if existing, seen := nodes[neighborID]; seen {
				// Already reached: update composite score if this path is better.
				if newScore > existing.compositeScore {
					existing.compositeScore = newScore
					existing.result.CompositeScore = newScore
					existing.result.HopDistance = hop
				}
				continue
			}

			// New node: fetch the memory to get content and importance_score.
			mem, fetchErr := s.memRepo.GetByID(ctx, neighborID)
			if fetchErr != nil || mem == nil {
				continue // skip unreachable or deleted memories
			}

			// Drop low-importance graph-expanded memories.
			if mem.ImportanceScore < graphMinImportance {
				continue
			}

			nodes[neighborID] = &nodeInfo{
				result: domain.RecallGraphResult{
					ID:              mem.ID,
					Content:         mem.Content,
					ImportanceScore: mem.ImportanceScore,
					CompositeScore:  newScore,
					Provenance:      domain.ProvenanceGraph,
					HopDistance:     hop,
				},
				compositeScore: newScore,
			}
			nextFrontier = append(nextFrontier, neighborID)
		}

		frontier = nextFrontier
	}

	// ── Step 3: Collect and sort ───────────────────────────────────────────────
	results := make([]domain.RecallGraphResult, 0, len(nodes))
	for _, n := range nodes {
		n.result.CompositeScore = n.compositeScore
		results = append(results, n.result)
	}

	slices.SortFunc(results, func(a, b domain.RecallGraphResult) int {
		return cmp.Compare(b.CompositeScore, a.CompositeScore)
	})

	// ── Step 4: Cache and return ───────────────────────────────────────────────
	recallGraphCache.Store(cacheKey, recallGraphCacheEntry{
		results:   results,
		expiresAt: now.Add(recallGraphCacheTTL),
	})

	return results, nil
}
