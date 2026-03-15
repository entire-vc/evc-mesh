package service

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"slices"
	"strings"
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
	embedder embedding.Embedder
}

// NewMemoryService returns a new MemoryService.
// embedder may be embedding.NewNoopEmbedder() when vector search is not configured;
// all vector operations are skipped gracefully in that case.
func NewMemoryService(memRepo repository.MemoryRepository, embedder embedding.Embedder) MemoryService {
	if embedder == nil {
		embedder = embedding.NewNoopEmbedder()
	}
	return &memoryService{memRepo: memRepo, embedder: embedder}
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

	if err := s.memRepo.Upsert(ctx, mem); err != nil {
		return "", fmt.Errorf("memory remember: upsert: %w", err)
	}

	// Async embedding — fire and forget, non-fatal.
	if !embedding.IsNoop(s.embedder) {
		memID := mem.ID
		content := mem.Key + " " + mem.Content + " " + strings.Join(mem.Tags, " ")
		go s.embedAndStore(memID, content)
	}

	return outcome, nil
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

// Recall performs a hybrid search (keyword + optional vector) and returns ranked results.
//
// Algorithm:
//  1. Always: full-text keyword search via tsvector (ts_rank_cd).
//  2. If embedder configured: embed query → vector similarity search.
//  3. Merge both result sets using Reciprocal Rank Fusion (RRF).
//  4. Apply temporal decay (half-life 30d) — agent-scope memories only.
//  5. Boost relevance of returned memories as positive feedback (non-fatal).
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

	// ── Step 4: Temporal decay ────────────────────────────────────────────────
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

	// ── Step 5: Trim to requested limit ───────────────────────────────────────
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
		// Non-admin agents may only delete their own agent-scope memories.
		if actorAgentID == nil {
			return apierror.Forbidden("only admins can delete memories created by other actors")
		}
		if mem.Scope != domain.ScopeAgent || mem.AgentID == nil || *mem.AgentID != *actorAgentID {
			return apierror.Forbidden("agents may only delete their own agent-scope memories")
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
