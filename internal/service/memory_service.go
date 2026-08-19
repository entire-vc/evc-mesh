package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gopkg.in/yaml.v3"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

// keySlugRegex matches valid memory keys: lowercase alphanumeric with hyphens,
// starting and ending with an alphanumeric character, at least two characters long.
var keySlugRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// defaultMinImportance is the threshold applied to recall queries when the caller
// does not supply an explicit min_importance. Entries below this score are noise.
const defaultMinImportance float32 = 0.4

// maxNearDupHamming is the Hamming distance threshold for near-duplicate detection.
// Two memories whose content_simhash values differ by ≤ 10 bits are considered near-dups.
const maxNearDupHamming = 10

// entityKeywords are canonical domain terms that boost importance_score when found
// in memory content, signalling higher decision-relevance.
var entityKeywords = []string{"icp", "architecture", "license", "security", "money"}

// shortIDRegex matches lowercase hex strings of 6–12 characters (word-bounded) that may
// reference another memory by its short UUID prefix in an incident memory body.
var shortIDRegex = regexp.MustCompile(`\b([0-9a-f]{6,12})\b`)

// projectSlugAliases maps every accepted project tag value (including legacy aliases) to
// the canonical project slug used in the DB. When a memory is written with a
// project:<slug> tag but no explicit project_id, Remember() resolves the canonical slug
// and looks up the project in the DB. Only one distinct canonical slug may be present;
// cross-project writes (multiple distinct slugs) stay workspace-scoped (project_id = NULL).
//
// Source: backfill from Lab task #41cde334 — reusing the same alias map.
var projectSlugAliases = map[string]string{
	"argus":             "argus",
	"evc-argus":         "argus",
	"billing":           "billing",
	"evc-billing":       "billing",
	"content-marketing": "content-marketing",
	"contenthub":        "contenthub",
	"lab":               "lab",
	"local-sync":        "local-sync",
	"marketing":         "marketing",
	"mesh-dev":          "mesh-dev",
	"mesh":              "mesh-dev",
	"evc-mesh":          "mesh-dev",
	"spark":             "spark",
	"evc-spark":         "spark",
	"tg-bot":            "tg-bot",
	"tgbot":             "tg-bot",
	"evc-tgbot":         "tg-bot",
	"team-relay":        "team-relay",
	"evc-team-relay":    "team-relay",
}

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
		case "kind:pinned":
			if base < 1.0 {
				base = 1.0
			}
		case "kind:preference":
			if base < 0.80 {
				base = 0.80
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

// genericTagPrefixes are namespace prefixes whose tags are excluded from edge-creation
// overlap checks. They appear on virtually every memory (project:X, owner:Y, kind:Z)
// so including them inflates Jaccard similarity and connects unrelated memories.
var genericTagPrefixes = []string{"kind:", "owner:", "project:", "phase:"}

// genericTagExact are single-word tags too common to carry semantic signal for edges.
var genericTagExact = map[string]struct{}{
	"fleet": {}, "infra": {}, "bug": {}, "feature": {}, "throwaway": {},
	"smoke": {}, "probe": {}, "test": {}, "wip": {},
	"p0": {}, "p1": {}, "p2": {},
}

// isGenericTag returns true for tags that should be excluded from edge-overlap checks.
func isGenericTag(t string) bool {
	if _, ok := genericTagExact[t]; ok {
		return true
	}
	for _, pfx := range genericTagPrefixes {
		if strings.HasPrefix(t, pfx) {
			return true
		}
	}
	return false
}

// filterSemanticTags returns only the tags that carry semantic signal for KG edges,
// dropping generic namespace and utility tags.
func filterSemanticTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if !isGenericTag(t) {
			out = append(out, t)
		}
	}
	return out
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

// defaultRRFVectorWeight and defaultRRFTextWeight control the relative contribution of the
// dense (vector) and sparse (BM25) arms in the RRF merge: score += weight/(rrfK+rank).
//
// ⚠️ These values are NOT validated. They were set while the dense arm was dead — the
// embedder was returning HTTP 402 and recall was in practice serving bm25-only — so the
// tilt was dead code for weeks. It first took effect on 2026-07-15 and immediately cost
// the benchmark two questions: every bm25-only run scores single-session-user 1.000,
// every hybrid run 0.500, split by MODE rather than by date, on an unchanged dataset.
//
// What the arithmetic implies, given score = weight/(rrfK+rank):
//
//	a dense-arm hit at rank r outranks the BM25 arm's rank-1 hit whenever
//	    0.7/(60+r) > 0.3/61   ⟺   r ≤ 82
//
// With poolSize = limit × candidateMultiplier, up to 82 dense-only rows can sit above
// BM25's best hit — i.e. on any query where BM25 is the arm that discriminates, BM25 is
// decorative. This was observed live, twice independently: gold scored exactly 0.3/63
// (BM25 rank 3, no dense term at all) and was displaced by a dense-only row from the
// deep tail (rank 77 in one run, 84 in another).
//
// Overridable at runtime via MEMORY_RECALL_RRF_VECTOR_WEIGHT / MEMORY_RECALL_RRF_TEXT_WEIGHT
// so the weights can be swept against the recall gate WITHOUT a rebuild — the sweep is
// tracked in task #acb84eaa, and re-weighting is deliberately NOT done here: changing
// these numbers without a measurement would repeat the mistake that produced them.
// Defaults therefore stay at the current production values until that sweep runs.
const (
	defaultRRFVectorWeight = 0.7
	defaultRRFTextWeight   = 0.3
)

// defaultHalfLifeDays is the default half-life for the universal exponential decay.
// Override at runtime via MEMORY_RECALL_HALF_LIFE_DAYS env var or the per-call HalfLifeDays opt.
const defaultHalfLifeDays = 30.0

// candidateMultiplier controls how many extra candidates are fetched for re-ranking.
// FullTextSearch and VectorSearch each fetch limit * candidateMultiplier results.
const candidateMultiplier = 3

type memoryService struct {
	memRepo      repository.MemoryRepository
	edgeRepo     repository.MemoryEdgeRepository
	embedder     embedding.Embedder
	projectRepo  repository.ProjectRepository        // optional; nil → slug resolution skipped
	taskRepo     repository.TaskRepository           // optional; nil → Amendments 2 & 3 skipped
	depRepo      repository.TaskDependencyRepository // optional; nil → depends_on bridge skipped
	chunkRepo    repository.MemoryChunkRepository    // optional; nil → legacy single-vector embed path (memories.embedding)
	halfLifeDays float64                             // half-life for exp decay; default defaultHalfLifeDays
	embedSem     chan struct{}                       // optional bound on concurrent embed goroutines; nil = unbounded (default)

	// RRF arm weights, resolved once at construction. Held per-service rather than as
	// package globals so a weight sweep cannot race across concurrently-built services
	// and so tests can set them without mutating process state.
	rrfVectorWeight float64
	rrfTextWeight   float64
}

// MemoryServiceOption configures a MemoryService.
type MemoryServiceOption func(*memoryService)

// MemoryWithProjectRepo enables automatic project-tag resolution. When a memory is written
// with a project:<slug> tag but no explicit project_id, Remember() looks up the project
// by canonical slug and sets project_id. Pass nil to disable (default when omitted).
func MemoryWithProjectRepo(pr repository.ProjectRepository) MemoryServiceOption {
	return func(s *memoryService) {
		s.projectRepo = pr
	}
}

// MemoryWithTaskRepo enables Amendment 2 (thread_id edges) and Amendment 3 (task-graph
// bridge) by allowing the memory service to look up source task metadata.
func MemoryWithTaskRepo(tr repository.TaskRepository) MemoryServiceOption {
	return func(s *memoryService) {
		s.taskRepo = tr
	}
}

// MemoryWithDepRepo enables the depends_on part of Amendment 3. When set, the memory
// service also creates derived_from edges for tasks in the depends_on set of the source task.
func MemoryWithDepRepo(dr repository.TaskDependencyRepository) MemoryServiceOption {
	return func(s *memoryService) {
		s.depRepo = dr
	}
}

// MemoryWithChunkRepo switches the embed write path (embedAndStore, BatchEmbed) from
// the legacy single-vector memories.embedding column to per-chunk storage in
// memory_chunks (ADR-0002). Without this option, a memory longer than the embedder's
// input window (~2000 chars / 512 tokens for the prod TEI endpoint) only ever gets
// embedded from its first ~15% — see #e8063a65. Omit only in tests exercising the
// legacy path directly; production wiring always sets this.
func MemoryWithChunkRepo(cr repository.MemoryChunkRepository) MemoryServiceOption {
	return func(s *memoryService) {
		s.chunkRepo = cr
	}
}

// MemoryWithEmbedConcurrency bounds how many embedAndStore goroutines may call the
// embedder concurrently. A burst of writes against a slow, CPU-bound embedder (e.g. a
// self-hosted TEI server) can otherwise stampede it: every write fires its own goroutine
// with no limit, the embedder's own concurrency semaphore starts rejecting requests,
// survivors queue, and the embed HTTP client's timeout trips before a response arrives.
// n <= 0 leaves embedding unbounded (today's exact behavior, and the default when this
// option is omitted).
func MemoryWithEmbedConcurrency(n int) MemoryServiceOption {
	return func(s *memoryService) {
		if n > 0 {
			s.embedSem = make(chan struct{}, n)
		}
	}
}

// NewMemoryService returns a new MemoryService.
// embedder may be embedding.NewNoopEmbedder() when vector search is not configured;
// all vector operations are skipped gracefully in that case.
// Optional MemoryServiceOption values (e.g. MemoryWithProjectRepo) extend behaviour.
func NewMemoryService(memRepo repository.MemoryRepository, edgeRepo repository.MemoryEdgeRepository, embedder embedding.Embedder, opts ...MemoryServiceOption) MemoryService {
	if embedder == nil {
		embedder = embedding.NewNoopEmbedder()
	}
	halfLife := defaultHalfLifeDays
	if v := os.Getenv("MEMORY_RECALL_HALF_LIFE_DAYS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			halfLife = f
		}
	}
	s := &memoryService{
		memRepo:         memRepo,
		edgeRepo:        edgeRepo,
		embedder:        embedder,
		halfLifeDays:    halfLife,
		rrfVectorWeight: envFloatOrDefault("MEMORY_RECALL_RRF_VECTOR_WEIGHT", defaultRRFVectorWeight),
		rrfTextWeight:   envFloatOrDefault("MEMORY_RECALL_RRF_TEXT_WEIGHT", defaultRRFTextWeight),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// envFloatOrDefault reads a non-negative float from the environment, falling back to def
// when the variable is unset, unparseable, or negative.
//
// Zero IS accepted: `0` is the meaningful "disable this arm entirely" setting, which the
// weight sweep needs in order to measure each arm alone. That is why the guard is `< 0`
// and not `<= 0` — the half-life reader above deliberately rejects zero because a zero
// half-life is nonsense, whereas a zero weight is a legitimate configuration.
func envFloatOrDefault(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		log.Printf("memory recall: ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return f
}

// resolveProjectSlug scans tags for project:<slug> entries, applies the canonical alias
// map, and returns the unique canonical slug when exactly one distinct project is named.
// Returns ("", false) when zero or multiple distinct canonical slugs are found — those
// writes stay workspace-scoped (project_id = NULL).
func resolveProjectSlug(tags []string) (string, bool) {
	seen := make(map[string]struct{}, 2)
	for _, tag := range tags {
		val, ok := strings.CutPrefix(tag, "project:")
		if !ok {
			continue
		}
		if canonical, exists := projectSlugAliases[val]; exists {
			seen[canonical] = struct{}{}
		}
	}
	if len(seen) != 1 {
		return "", false
	}
	for slug := range seen {
		return slug, true
	}
	return "", false
}

// Remember upserts a memory entry. It returns "created" if the key did not exist before,
// or "updated" if an existing entry was overwritten.
// After a successful upsert, it asynchronously embeds the content when an embedder is configured.
func (s *memoryService) Remember(ctx context.Context, mem *domain.Memory) (RememberResult, error) {
	if mem.Key == "" {
		return RememberResult{}, apierror.ValidationError(map[string]string{
			"key": "key is required",
		})
	}
	if !keySlugRegex.MatchString(mem.Key) {
		return RememberResult{}, apierror.ValidationError(map[string]string{
			"key": "key must match pattern ^[a-z0-9][a-z0-9-]*[a-z0-9]$ (lowercase alphanumeric with hyphens)",
		})
	}
	if mem.Content == "" {
		return RememberResult{}, apierror.ValidationError(map[string]string{
			"content": "content is required",
		})
	}
	if mem.WorkspaceID == uuid.Nil {
		return RememberResult{}, apierror.ValidationError(map[string]string{
			"workspace_id": "workspace_id is required",
		})
	}

	// Validate relevance ∈ [0, 1].
	if mem.Relevance < 0 || mem.Relevance > 1 {
		return RememberResult{}, apierror.ValidationError(map[string]string{
			"relevance": "relevance must be between 0 and 1",
		})
	}
	// Default relevance for new memories.
	if mem.Relevance == 0 {
		mem.Relevance = 1.0
	}

	// Validate tags.
	if len(mem.Tags) > 20 {
		return RememberResult{}, apierror.ValidationError(map[string]string{
			"tags": "maximum 20 tags allowed",
		})
	}
	for _, tag := range mem.Tags {
		if len(tag) > 64 {
			return RememberResult{}, apierror.ValidationError(map[string]string{
				"tags": "each tag must be 64 characters or fewer",
			})
		}
	}

	// Validate expires_at: if provided it must be in the future.
	if mem.ExpiresAt != nil && !mem.ExpiresAt.After(time.Now()) {
		return RememberResult{}, apierror.ValidationError(map[string]string{
			"expires_at": "expires_at must be in the future",
		})
	}

	// Apply default expires_at policy when not explicitly provided.
	if mem.ExpiresAt == nil {
		mem.ExpiresAt = defaultExpiresAt(mem.Scope, mem.Tags)
	}

	// A caller-supplied project_id only means something when scope=project:
	// GetByKey's identity for workspace/agent scope never includes it, and the
	// project-scoped recall filter (project_id = $N, no OR scope='workspace')
	// makes a stray project_id on a workspace-scope row silently invisible to
	// any agent recalling from a DIFFERENT project than the one it happened to
	// carry. Both known auto-stamp sources (resolveProjectSlug below, and the
	// MCP client's active-task auto-populate, evc-mesh-mcp#44) were already
	// gated to scope==project — but neither gate stops a caller from passing
	// project_id EXPLICITLY alongside scope=workspace/agent. Measured live
	// 2026-07-30: 4 new such rows landed within ~1h of both auto-stamp fixes
	// being deployed (#4edf3fb5 cleanup regressing again, task #2c0154db/F3).
	// Normalize unconditionally, before identity lookup, so no future caller
	// or code path can reintroduce the drift the cleanup already paid for once.
	if mem.Scope != domain.ScopeProject {
		mem.ProjectID = nil
	}

	// ── Slug resolution: if no project_id given but tags contain exactly one
	// resolvable project:<slug>, look up the project and populate project_id. ──
	// Scoped to scope=project only: identity for workspace/agent scope does not
	// include project_id (see GetByKey), so auto-populating it there would just
	// re-create the "workspace record incoherently carries a project_id" bug
	// this fix removes — 544 of 1186 active workspace-scoped rows had exactly
	// that shape before the scope-identity fix.
	if mem.Scope == domain.ScopeProject && mem.ProjectID == nil && s.projectRepo != nil {
		if slug, ok := resolveProjectSlug(mem.Tags); ok {
			if proj, lookupErr := s.projectRepo.GetBySlug(ctx, mem.WorkspaceID, slug); lookupErr == nil && proj != nil {
				mem.ProjectID = &proj.ID
			}
		}
	}

	// Determine whether this is a create or update by checking for an existing entry.
	existing, err := s.memRepo.GetByKey(ctx, mem.WorkspaceID, mem.ProjectID, mem.AgentID, mem.Key, mem.Scope)
	if err != nil {
		return RememberResult{}, fmt.Errorf("memory remember: lookup existing: %w", err)
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

	// Set health lifecycle defaults for new memories written via Remember().
	if mem.Status == "" {
		mem.Status = domain.MemoryStatusActive
	}
	if mem.FreshnessScore == 0 && mem.Status == domain.MemoryStatusActive {
		mem.FreshnessScore = 1.0
	}

	// ── Simhash: compute 64-bit fingerprint and detect near-duplicates ──────────
	// A near-dup is a different memory (different key) whose content_simhash is
	// within maxNearDupHamming bits. Detected synchronously at write time so
	// callers are notified immediately without waiting for the 6h reconciler.
	hash := ComputeSimhash(mem.Content)
	mem.ContentSimhash = &hash

	var nearDupKey string
	if hash != 0 {
		excludeID := mem.ID // may be uuid.Nil for new creates; FindBySimhashProximity handles that
		if dups, dupErr := s.memRepo.FindBySimhashProximity(ctx, mem.WorkspaceID, hash, maxNearDupHamming, excludeID, 1); dupErr == nil && len(dups) > 0 {
			nearDupKey = dups[0].Key
		}
	}

	// Cache the source-task lookup — used for both thread_id propagation and
	// Amendment 2/3 edge hooks below. One query, two consumers.
	var sourceTask *domain.Task
	if mem.SourceTaskID != nil && s.taskRepo != nil {
		if task, taskErr := s.taskRepo.GetByID(ctx, *mem.SourceTaskID); taskErr == nil && task != nil {
			sourceTask = task
		}
	}
	// Propagate thread_id from the source task when the caller has not set it explicitly.
	if sourceTask != nil && mem.ThreadID == nil {
		mem.ThreadID = sourceTask.ThreadID
	}

	if err := s.memRepo.Upsert(ctx, mem); err != nil {
		return RememberResult{}, fmt.Errorf("memory remember: upsert: %w", err)
	}

	// Async embedding — fire and forget, non-fatal. The row is invisible to the
	// dense recall arm until this goroutine lands (see EmbeddingPending's doc);
	// embeddingPending is reported to the caller below regardless of how long
	// the goroutine actually takes.
	embeddingPending := !embedding.IsNoop(s.embedder)
	if embeddingPending {
		memID := mem.ID
		prefix := mem.Key + " " + strings.Join(mem.Tags, " ") + " "
		go s.embedAndStore(memID, mem.Content, prefix)
	}

	// ── Hook 1: relates_to edge on semantic tag overlap ≥60% ─────────────────
	// Only non-generic tags are compared: kind:*, owner:*, project:*, phase:*
	// prefixes and common utility words are excluded to prevent all memories in
	// the same project from becoming a fully-connected clique.
	if s.edgeRepo != nil && len(mem.Tags) > 0 {
		semanticTags := filterSemanticTags(mem.Tags)
		if len(semanticTags) > 0 {
			if candidates, scanErr := s.memRepo.FindByScope(ctx, mem.WorkspaceID, mem.ProjectID, string(mem.Scope), 200); scanErr == nil {
				for _, candidate := range candidates {
					if candidate.ID == mem.ID {
						continue
					}
					candidateSemantic := filterSemanticTags([]string(candidate.Tags))
					if len(candidateSemantic) == 0 {
						continue
					}
					if tagOverlapRatio(candidateSemantic, semanticTags) >= 0.6 {
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

	// ── Amendment 2: same-thread relates_to edges (weight=1.0) ───────────────────
	// Memories created during the same fiddler thread share a thread_id propagated
	// from the source task. They are strongly related (same work session) so get
	// a higher weight than the tag-overlap Hook 1.
	if s.edgeRepo != nil && mem.ThreadID != nil && *mem.ThreadID != "" {
		if threadMems, tErr := s.memRepo.FindByThreadID(ctx, mem.WorkspaceID, *mem.ThreadID, mem.ID); tErr == nil {
			for _, candidate := range threadMems {
				edge := &domain.MemoryEdge{
					MemoryFromID:     mem.ID,
					MemoryToID:       candidate.ID,
					RelationshipType: domain.EdgeRelatesTo,
					Weight:           1.0,
					WorkspaceID:      mem.WorkspaceID,
				}
				_ = s.edgeRepo.UpsertEdge(ctx, edge)
			}
		}
	}

	// ── Amendment 3: task-graph bridge derived_from edges (weight=0.7) ───────────
	// The Mesh task graph (parent_task / depends_on) is a ready-made skeleton:
	// if memory A was produced while working on task X and task X depends on (or is
	// a subtask of) task Y, then memories from task Y are "ancestors" of memory A.
	if s.edgeRepo != nil && sourceTask != nil {
		var relatedTaskIDs []uuid.UUID
		if sourceTask.ParentTaskID != nil {
			relatedTaskIDs = append(relatedTaskIDs, *sourceTask.ParentTaskID)
		}
		if s.depRepo != nil {
			if deps, depErr := s.depRepo.ListByTask(ctx, *mem.SourceTaskID); depErr == nil {
				for _, dep := range deps {
					relatedTaskIDs = append(relatedTaskIDs, dep.DependsOnTaskID)
				}
			}
		}
		if len(relatedTaskIDs) > 0 {
			if relatedMems, findErr := s.memRepo.FindBySourceTaskIDs(ctx, mem.WorkspaceID, relatedTaskIDs); findErr == nil {
				for _, candidate := range relatedMems {
					if candidate.ID == mem.ID {
						continue
					}
					edge := &domain.MemoryEdge{
						MemoryFromID:     mem.ID,
						MemoryToID:       candidate.ID,
						RelationshipType: domain.EdgeDerivedFrom,
						Weight:           0.7,
						WorkspaceID:      mem.WorkspaceID,
					}
					_ = s.edgeRepo.UpsertEdge(ctx, edge)
				}
			}
		}
	}

	return RememberResult{Outcome: outcome, NearDupKey: nearDupKey, EmbeddingPending: embeddingPending}, nil
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

	if hasTag("session-checkpoint") || hasTag("kind:session-checkpoint") {
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

// embedAndStore embeds text and persists the resulting vector(s) for the given memory ID.
// Called asynchronously from Remember; errors are logged but never surfaced to callers.
// When embedSem is configured (MemoryWithEmbedConcurrency), it caps how many embed calls
// run concurrently; otherwise embedding remains unbounded. embedSem is shared with the
// query-embed call in RecallWithStats and with BatchEmbed (see acquireEmbedSem) — it is
// a bound on the whole memoryService's embedder client, not just this write path (#3d10774e:
// EMBEDDING_CONCURRENCY looked like a global bound but originally only gated this one call
// site, leaving the recall query embed to stampede the embedder unbounded).
//
// When chunkRepo is configured (MemoryWithChunkRepo), text is split into chunks (see
// #e8063a65: the prod embedder silently truncates past ~2000 chars, so a memory longer
// than that needs more than one vector to be fully searchable) and each chunk's vector is
// stored in memory_chunks — memories.embedding is left untouched for that memory. Without
// chunkRepo, the legacy single-vector path applies unchanged.
// embedAndStore embeds a memory's content and persists the resulting vector(s).
// prefix (the memory's key + tags, space-joined) is prepended to every embedded
// unit — the whole content in the single-vector path below, and every chunk in
// embedChunked — but is never itself chunked and never shifts chunk_start/
// chunk_end, which stay byte offsets into content alone (see embedChunked's doc
// and #38bb958c: prefixing only chunk 0 of a composite pre-chunked string left
// ~94% of a multi-chunk memory's chunks searchable without its own key).
// acquireEmbedSem blocks until an embedSem slot is free, or ctx is done — in which
// case it returns ctx.Err() and holds no slot. A nil embedSem (the default,
// EMBEDDING_CONCURRENCY unset) always succeeds immediately, so every call site can
// use this unconditionally regardless of whether a limit is configured.
//
// Pass context.Background() (never the call's own ctx) at a site that establishes
// its OWN embed-budget deadline immediately after acquiring — embedAndStore's
// chunked and single-vector paths both do this via embedBudget() — so time spent
// queued for a slot is never charged against that budget; see embedAndStore's doc
// for why that matters. Pass the caller's own ctx at a site with no such invented
// budget (RecallWithStats, BatchEmbed) — there, honoring the caller's deadline
// while queued is correct: a recall stuck behind a full semaphore should fail open
// to BM25 like any other embed failure, not outlive the request that asked for it.
func (s *memoryService) acquireEmbedSem(ctx context.Context) (release func(), err error) {
	if s.embedSem == nil {
		return func() {}, nil
	}
	select {
	case s.embedSem <- struct{}{}:
		return func() { <-s.embedSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *memoryService) embedAndStore(id uuid.UUID, content, prefix string) {
	release, _ := s.acquireEmbedSem(context.Background()) // never errors on a Background ctx
	defer release()

	if s.chunkRepo != nil {
		// Budget scales with the number of embedder ROUND TRIPS this memory needs, not
		// with the memory itself. A flat 30s per memory was a hidden "no longer than
		// ~12 chunks under load" limit: chunking multiplied the calls by N while the
		// budget stayed constant, so long documents died mid-document (538 deadline
		// failures at chunk 12-15 of 18-24 in one loaded window, #67f4e0d9) — silently,
		// since the goroutine only logs. Batching makes it one round trip for almost
		// every memory; this scaling is the insurance for when it isn't.
		pieces := chunkText(content, defaultChunkSize, defaultChunkOverlap)
		ctx, cancel := context.WithTimeout(context.Background(), embedBudget(len(pieces)))
		defer cancel()
		if err := s.embedChunked(ctx, id, content, prefix); err != nil {
			if isForeignKeyViolation(err) {
				// The memory was deleted while this goroutine was still embedding it
				// (bench fixtures are created and swept within seconds). Not an
				// embedding failure — counting it as one masks the real ones.
				return
			}
			pkgmetrics.MemoryEmbedFailuresTotal.WithLabelValues("store").Inc()
			log.Printf("memory embed (chunked): id=%s chunks=%d error=%v", id, len(pieces), err)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), embedBudget(1))
	defer cancel()

	vec, err := s.embedder.Embed(ctx, prefix+content)
	if err != nil {
		pkgmetrics.MemoryEmbedFailuresTotal.WithLabelValues("store").Inc()
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

// embedChunked splits content into chunks, embeds each one (with prefix prepended)
// individually, and atomically replaces the memory's chunk rows (ReplaceChunks —
// delete+reinsert in one transaction, which is what makes re-embedding idempotent:
// chunkText is deterministic, so re-running this on the same content always produces
// the same rows).
//
// prefix (key + tags, space-joined — see embedAndStore) is prepended to EVERY chunk's
// embedded text, never chunked itself and never part of the stored chunk_start/
// chunk_end. Before this, the composite key+content+tags string was chunked as a
// whole, so the key landed only in chunk 0 and tags only in the last chunk — ~94% of
// a multi-chunk memory's chunks were searchable without the memory's own key
// (#38bb958c). Measured fix (variant B2, not the naive "prefix the existing composite
// chunks" B1, which failed its own single-chunk control): +0.058 MRR on the live query
// distribution, +0.112 on multi-chunk memories specifically, and a genuine ~0 on the
// single-chunk control where the mechanism cannot apply.
//
// embedding_dim is taken from len(vec) — the actual returned vector — never from
// s.embedder.Dimensions() (the configured value). Trusting the config is exactly the bug
// that left embedding_dim=0 on every row before EMBEDDING_DIMENSIONS was set (see #b052cdda
// subtask 4): a misconfigured or zero-value config would propagate through every chunk
// instead of surfacing as a mismatch.
//
// A chunk whose embed call fails aborts the whole memory's replace (returns early without
// calling ReplaceChunks) rather than writing a partial set — a memory with 3 of 4 chunks
// embedded would look chunked but silently drop content, the same class of failure this
// entire fix exists to close.
func (s *memoryService) embedChunked(ctx context.Context, id uuid.UUID, content, prefix string) error {
	return s.embedChunkedStoring(ctx, id, content, prefix, s.memRepo.UpdateEmbedding)
}

// embedChunkedStoring is embedChunked with the memories.embedding write-back injected, so a
// repair path can re-embed an existing memory without the `updated_at = NOW()` bump that
// UpdateEmbedding carries (RechunkStale passes UpdateEmbeddingKeepUpdatedAt). Only the
// write-back differs — chunking, prefixing and ReplaceChunks are shared, so the two paths
// cannot drift into producing different chunks for the same memory.
func (s *memoryService) embedChunkedStoring(
	ctx context.Context,
	id uuid.UUID,
	content, prefix string,
	storeVector func(context.Context, uuid.UUID, []float32, string, int) error,
) error {
	pieces := chunkText(content, defaultChunkSize, defaultChunkOverlap)
	if len(pieces) == 0 {
		return nil
	}
	texts := make([]string, len(pieces))
	for i, p := range pieces {
		texts[i] = prefix + p.Text
	}

	// ONE batched call (per 32 chunks) instead of one call per chunk. Sequential
	// per-chunk calls put ~20x the load on a shared embedder instance and made the
	// failure probability grow with document length — i.e. chunking failed hardest on
	// exactly the long documents it exists to serve (#67f4e0d9).
	vecs, err := embedWithRetry(ctx, s.embedder, texts)
	if err != nil {
		return fmt.Errorf("embed %d chunks: %w", len(pieces), err)
	}
	if len(vecs) != len(pieces) {
		return fmt.Errorf("embed %d chunks: embedder returned %d vectors", len(pieces), len(vecs))
	}

	runes := []rune(content)
	chunks := make([]domain.MemoryChunk, 0, len(pieces))
	var firstVec []float32
	for i, p := range pieces {
		vec := vecs[i]
		if len(vec) == 0 {
			continue
		}
		if firstVec == nil {
			firstVec = vec
		}
		chunks = append(chunks, domain.MemoryChunk{
			ChunkIdx:       i,
			ChunkStart:     runeOffsetToByteOffset(runes, p.Start),
			ChunkEnd:       runeOffsetToByteOffset(runes, p.End),
			Embedding:      domain.EncodeEmbedding(vec),
			EmbeddingModel: s.embedder.Model(),
			EmbeddingDim:   len(vec),
		})
	}
	if len(chunks) == 0 {
		// Every chunk's embed call returned an empty vector (e.g. the embedder
		// is a noop or degraded) — leave any existing rows alone rather than
		// wiping a memory's searchable chunks because of a transient failure.
		return nil
	}
	if err := s.chunkRepo.ReplaceChunks(ctx, id, chunks); err != nil {
		return err
	}
	// STOPGAP (#b052cdda, live regression caught in #84b0694d verify): VectorSearch's
	// read path has not been rewired onto memory_chunks yet (that's subtask 6/8,
	// still open) — it still requires memories.embedding IS NOT NULL. Writing only
	// the chunks, as the original design intended once read-path lands, made every
	// memory embedded through this path invisible to dense search: brand-new memories
	// never had memories.embedding populated at all (~200/hour after deploy). Until
	// VectorSearch reads memory_chunks directly, keep memories.embedding populated too
	// — the first chunk's vector, not the whole document, so this stays proportionate
	// to what a single embed call already produced (no extra embedder round trip).
	// UpdateEmbedding also sets embedding_model/embedding_dim, so it subsumes the old
	// MarkEmbeddingModel-only watermark this replaced.
	if err := storeVector(ctx, id, firstVec, s.embedder.Model(), len(firstVec)); err != nil {
		return fmt.Errorf("update embedding (chunked stopgap): %w", err)
	}
	return nil
}

// runeOffsetToByteOffset converts a rune index into runes (as produced by chunkText,
// which operates on runes to never split a multi-byte character) into the equivalent
// byte offset into the original string — the unit memory_chunks.chunk_start/chunk_end
// are stored in (ADR-0002), matching how Postgres substring() and most tooling index text.
func runeOffsetToByteOffset(runes []rune, runeOffset int) int {
	return len(string(runes[:runeOffset]))
}

// RecallResult holds the paginated recall response with metadata.
type RecallResult struct {
	Items        []domain.ScoredMemory
	Total        int
	Limit        int
	Offset       int
	DecayApplied bool
}

// Recall is the thin, stats-dropping wrapper over RecallWithStats. It exists so
// the mode-only signature — which every caller but the REST handler uses, and
// which ~25 tests are written against — stays exactly as it was; the arm counts
// are additive, and a caller that does not need them should not have to say so.
func (s *memoryService) Recall(ctx context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.SearchMode, error) {
	items, stats, err := s.RecallWithStats(ctx, opts)
	return items, stats.Mode, err
}

// RecallWithStats performs a hybrid search (keyword + optional vector) and returns
// ranked results together with the domain.RecallStats describing how they were
// actually SERVED — the mode, plus how many rows each arm returned.
//
// The second return value is load-bearing: step 2 below FAILS OPEN. If the
// embedder is down (or unconfigured), the dense arm silently contributes
// nothing and the caller still gets a 200 with BM25-only results. Returning the
// mode is what lets callers — the REST envelope, the Prometheus counters, and
// the CI recall gate — tell "memory got worse" apart from "the embedder died".
// Without it, a cross-mode score drop is indistinguishable from a code
// regression, which is how an infra outage turns into a repo-wide merge wedge.
//
// Mode has its own blind spot, which is why the row counts travel with it: it
// reports that the dense arm RAN, not that it FOUND anything. See
// domain.RecallStats — a corpus with no embeddings at all serves "hybrid",
// "degraded: false", and zero vector candidates.
//
// Algorithm:
//  1. Always: full-text keyword search via tsvector (ts_rank_cd).
//  2. If embedder configured: embed query → vector similarity search.
//  3. Merge both result sets using Reciprocal Rank Fusion (RRF).
//  4. Apply freshness multiplier (always) and temporal exp decay (when ApplyDecay or decayed_relevance).
//     Decay formula: score *= freshness_score × exp(-Δt·ln2/half_life). All scopes affected.
//  5. Boost relevance of returned memories as positive feedback (non-fatal).
//
// When extended filter params are present (TagsAny, CreatedBy, Since, Until, etc.),
// the repository List method is used instead of FullTextSearch for precise SQL filtering.
func (s *memoryService) RecallWithStats(ctx context.Context, opts domain.RecallOpts) ([]domain.ScoredMemory, domain.RecallStats, error) {
	if opts.Query == "" {
		return nil, domain.RecallStats{}, apierror.ValidationError(map[string]string{
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

	// ── Steps 1+2: BM25 sparse arm + vector arm in parallel ──────────────────
	// Both arms fetch candidateMultiplier × limit results. The BM25 arm uses the
	// 'english' dictionary (stemming + stopwords) for higher linguistic precision;
	// the vector arm ranks by cosine similarity. Both contribute to the RRF merge.
	//
	// bm25FTSTimeout prevents a slow FTS query from stalling Recall; on timeout the
	// BM25 arm is dropped and Recall degrades gracefully to vector-only. k=60 is the
	// standard RRF constant used in reciprocalRankFusion.
	const bm25FTSTimeout = 3 * time.Second

	// Eligibility predicates (scope, tags, tags_any) go INTO both arms, not after them.
	// Each arm truncates to poolSize; a row this filter would keep but which ranked below
	// an arm's cut is unrecoverable downstream, so post-filtering both narrows the result
	// AND leaves the pool sized by ineligible rows. Pre-filtering makes poolSize a budget
	// over the eligible set instead. See task #2c087b2a.
	searchFilter := opts.SearchFilter()

	var (
		kwResults  []domain.ScoredMemory
		vecResults []domain.ScoredMemory
		kwErr      error
		// denseArmRan is set by the vector goroutine only when the dense arm
		// completed end-to-end (embed OK, non-empty vector, VectorSearch OK).
		// Written before wg.Wait() returns and read after — the WaitGroup is the
		// happens-before edge, so no extra synchronisation is needed.
		denseArmRan bool
	)

	ftsCtx, ftsCancel := context.WithTimeout(ctx, bm25FTSTimeout)
	defer ftsCancel()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		kwResults, kwErr = s.memRepo.FullTextSearchRanked(ftsCtx, opts.WorkspaceID, projID, opts.Query, searchFilter, poolSize)
	}()

	if !embedding.IsNoop(s.embedder) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Query embed shares embedAndStore's embedSem (#3d10774e) — without this,
			// EMBEDDING_CONCURRENCY bounded only the write path, and every concurrent
			// recall fired its own unbounded embed call straight at the embedder. Wait
			// on the caller's own ctx (not context.Background()): a recall stuck behind
			// a full semaphore should fail open to BM25 like any other embed failure,
			// not hold a goroutine open past the request's own deadline.
			release, semErr := s.acquireEmbedSem(ctx)
			if semErr != nil {
				log.Printf("memory recall: embed semaphore wait aborted, using bm25-only: %v", semErr)
				pkgmetrics.RecordMemoryEmbedFailure("recall")
				return
			}
			defer release()

			queryVec, embedErr := s.embedder.Embed(ctx, opts.Query)
			if embedErr != nil {
				// FAIL OPEN: recall still succeeds, but on BM25 alone. This is the
				// silent degradation — count it so it is alertable, and report it
				// via SearchMode so callers can see it.
				log.Printf("memory recall: embedding failed, using bm25-only: %v", embedErr)
				pkgmetrics.RecordMemoryEmbedFailure("recall")
				return
			}
			if len(queryVec) == 0 {
				return
			}
			var vecErr error
			vecResults, vecErr = s.memRepo.VectorSearch(ctx, queryVec, opts.WorkspaceID, projID, searchFilter, poolSize)
			if vecErr != nil {
				log.Printf("memory recall: vector search failed: %v", vecErr)
				return
			}
			denseArmRan = true
		}()
	}

	wg.Wait()

	// The mode a recall was SERVED in — not the one it was configured for.
	// A noop embedder, an embedder error, an empty vector and a failed
	// VectorSearch all land in the same place: BM25 alone.
	searchMode := domain.SearchModeBM25Only
	if denseArmRan {
		searchMode = domain.SearchModeHybrid
	}
	pkgmetrics.RecordMemoryRecall(string(searchMode))

	if kwErr != nil {
		log.Printf("memory recall: bm25 fts failed (using vector-only): %v", kwErr)
		kwResults = nil
	}

	// Counted HERE — after both arms have finished and after the FTS-error reset
	// above, but before the RRF merge and every post-filter below. These report
	// what each ARM produced, not what survived downstream: a dense arm that
	// returned 300 candidates of which none passed the tag filter is a healthy
	// arm and a narrow query, while a dense arm that returned 0 is not an arm at
	// all. Collapsing those two into one number is the blindness this exists to
	// remove, so the count must not move below the merge.
	stats := domain.RecallStats{
		Mode:       searchMode,
		DenseRows:  len(vecResults),
		SparseRows: len(kwResults),
	}

	// ── Step 3: RRF merge ─────────────────────────────────────────────────────
	merged := reciprocalRankFusion(kwResults, vecResults, s.rrfTextWeight, s.rrfVectorWeight)

	// ── Step 4: Apply extended filters (TagsAny, CreatedBy, Since, Until, etc.) ─
	// Apply the default importance_score threshold when the caller hasn't overridden it.
	if opts.MinImportance == nil {
		def := defaultMinImportance
		opts.MinImportance = &def
	}
	// Default: exclude superseded memories unless the caller explicitly opts in.
	// ExcludeSuperseded=false means "let me see superseded" — zero value = false so
	// the caller must pass true to get the default behaviour; we flip the default here.
	if !opts.ExcludeSuperseded {
		// Only skip the default when the caller explicitly passed a StatusFilter that
		// includes superseded, signalling they want to see those memories.
		wantSuperseded := false
		for _, s := range opts.StatusFilter {
			if s == domain.MemoryStatusSuperseded {
				wantSuperseded = true
				break
			}
		}
		if !wantSuperseded {
			opts.ExcludeSuperseded = true
		}
	}
	// Always apply extended filters (handles importance, status, tags, etc.).
	merged = applyExtendedFilters(merged, opts)

	// ── Step 5: Freshness and universal temporal decay ────────────────────────
	// Decay is applied uniformly to ALL memory scopes when requested
	// (ApplyDecay=true or decayed_relevance ordering).
	// FreshnessScore is always multiplied in to penalise stale/superseded memories
	// regardless of the time-decay flag.
	now := time.Now()
	halfLife := s.halfLifeDays
	if opts.HalfLifeDays > 0 {
		halfLife = opts.HalfLifeDays
	}
	lambda := math.Log(2) / halfLife
	// Was `opts.OrderBy == "decayed_relevance"` — a strict compare against a form
	// nothing produces (#655c6d12). Every caller sends the suffixed
	// "decayed_relevance:desc": the REST handler substitutes it, the MCP temporal
	// profile hard-codes it, and the SQL switch matches only it. So the right-hand
	// side never fired, and decay on this path came exclusively from ApplyDecay.
	// It stayed invisible because the one profile that asks for this ordering
	// (temporal) also sets ApplyDecay=true, so the intended behaviour happened for
	// the wrong reason — a caller passing order_by ALONE, exactly as the tool
	// description and the RecallOpts doc comment invite, silently got no decay.
	applyTimeDecay := opts.ApplyDecay || domain.IsDecayedRelevanceOrder(opts.OrderBy)

	for i := range merged {
		m := &merged[i]

		// Temporal recency factor: exp(-Δt·ln2/half_life).
		var recencyFactor float64
		if applyTimeDecay {
			ageDays := now.Sub(m.CreatedAt).Hours() / 24
			recencyFactor = math.Exp(-lambda * ageDays)
		} else {
			recencyFactor = 1.0
		}
		m.RecencyScore = recencyFactor

		// FreshnessScore health-based multiplier: always applied.
		// Pre-P1-A memories default to 0; treat them as active (1.0).
		freshnessScore := float64(m.FreshnessScore)
		if freshnessScore == 0 {
			freshnessScore = 1.0
		}
		m.Score *= freshnessScore * recencyFactor
	}

	// Re-sort after freshness/decay adjustment.
	slices.SortFunc(merged, func(a, b domain.ScoredMemory) int {
		return cmp.Compare(b.Score, a.Score)
	})

	// ── Step 6: Inject pinned memories (kind:pinned always surfaced) ─────────
	// Pinned memories bypass retrieval entirely — they are always prepended to
	// the result, regardless of relevance score or min_importance threshold.
	//
	// What they do NOT bypass is eligibility: a pinned memory still has to satisfy
	// scope/tags. "Pinned" means "do not let ranking bury this", not "show this to a
	// caller who asked for a different scope" — otherwise a caller asking for
	// scope='agent' gets workspace rows back and the isolation contract is broken by
	// the one path that is exempt from every other check.
	//
	// Nor do they bypass `limit`. Injection happens BEFORE the trim below, so a
	// pinned row DISPLACES the weakest retrieval hit rather than being handed to
	// the caller on top of a full page. Prepending after the trim (as this did
	// until #4c65d3e2) returned limit+len(pinned) rows: `limit` stopped being a
	// bound the caller could rely on, and the overflow was invisible because the
	// response still echoed the requested limit.
	var pinnedProjID *uuid.UUID
	if opts.ProjectID != uuid.Nil {
		pinnedProjID = &opts.ProjectID
	}
	if pinned, pinnedErr := s.memRepo.FindPinned(ctx, opts.WorkspaceID, pinnedProjID); pinnedErr == nil && len(pinned) > 0 {
		seenIDs := make(map[uuid.UUID]struct{}, len(merged))
		for _, m := range merged {
			seenIDs[m.ID] = struct{}{}
		}
		var pinnedScored []domain.ScoredMemory
		for _, p := range pinned {
			if _, seen := seenIDs[p.ID]; seen {
				continue // already in results via retrieval
			}
			if !searchFilter.Allows(p) {
				continue // pinned still has to be eligible for THIS query
			}
			pinnedScored = append(pinnedScored, domain.ScoredMemory{Memory: p, Score: 2.0}) // score > any retrieval score
		}
		merged = append(pinnedScored, merged...)
	}

	// ── Step 7: Trim to requested limit — LAST, after every injection ────────
	// Everything that can add rows must run above this line. `limit` is a
	// contract with the caller, not a hint applied midway through the pipeline.
	if len(merged) > opts.Limit {
		merged = merged[:opts.Limit]
	}

	// ── Boost relevance as positive feedback (non-fatal) ─────────────────────
	// Boost only what the caller actually receives — rows trimmed above were
	// never seen and must not be treated as a hit.
	if len(merged) > 0 {
		ids := make([]uuid.UUID, len(merged))
		for i, r := range merged {
			ids[i] = r.ID
		}
		_ = s.memRepo.BoostRelevance(ctx, ids)
	}

	return merged, stats, nil
}

// applyExtendedFilters filters a slice of ScoredMemory using the extended RecallOpts fields
// (CreatedBy, Since, Until, RelevanceMin, ExcludeSuperseded, StatusFilter, MinImportance).
//
// Scope/Tags/TagsAny are ALSO re-checked here, via the same MemorySearchFilter both arms
// use as SQL. That is deliberate redundancy, not the enforcement point: the arms are the
// enforcement point, because only they can keep an eligible row from being cut. This pass
// is the second lock — it catches any row reaching the client through a path that did not
// pre-filter, and it makes scope isolation hold even if an arm is later changed to ignore
// the filter. Before #2c087b2a there was no scope branch here at all and the BM25 arm was
// never given scope, so scope was unenforced on every BM25 row and unenforced entirely in
// bm25-only mode — the fail-open path taken whenever the embedder is down.
func applyExtendedFilters(items []domain.ScoredMemory, opts domain.RecallOpts) []domain.ScoredMemory {
	searchFilter := opts.SearchFilter()
	out := items[:0]
	for _, m := range items {
		if !searchFilter.Allows(m.Memory) {
			continue
		}
		if opts.ExcludeSuperseded && m.Status == domain.MemoryStatusSuperseded {
			continue
		}
		if len(opts.StatusFilter) > 0 {
			match := false
			for _, s := range opts.StatusFilter {
				if m.Status == s {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
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
		// NB: Scope/Tags/TagsAny are handled by searchFilter.Allows at the top of the loop.
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
func reciprocalRankFusion(kw, vec []domain.ScoredMemory, textWeight, vectorWeight float64) []domain.ScoredMemory {
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
		scores[id].score += textWeight * (1.0 / (float64(rrfK) + float64(rank+1)))
	}

	for rank, m := range vec {
		id := m.ID
		if _, ok := scores[id]; !ok {
			mc := m.Memory
			scores[id] = &entry{mem: mc}
		}
		scores[id].score += vectorWeight * (1.0 / (float64(rrfK) + float64(rank+1)))
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

// GetProjectKnowledge returns memories for a workspace/project pair with optional pagination.
// When projectID is nil (workspace-tier), filter.Limit/Offset/MinImportance/TagsAny are applied.
// Returns the slice and total count before pagination.
func (s *memoryService) GetProjectKnowledge(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID, filter domain.MemoryListFilter) ([]domain.Memory, int64, error) {
	return s.memRepo.ListByWorkspaceProject(ctx, workspaceID, projectID, filter)
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

	remResult, err := s.Remember(ctx, mem)
	if err != nil {
		return nil, "", fmt.Errorf("set_project_knowledge: %w", err)
	}
	outcome := remResult.Outcome

	// ── Amendment 4: canonical-supersede ─────────────────────────────────────────
	// Every set_project_knowledge call writes a canonical (project-scoped) fact.
	// If memories exist in the same project that share ≥60% semantic tag overlap
	// with the new canonical entry, they are likely stale predecessors. Create a
	// supersedes edge from the canonical entry → stale memory and archive the stale
	// one so it drops off the recall top. Only memories with importance_score < 0.75
	// are superseded: canonical/decision entries (≥0.75) are never auto-archived.
	if s.edgeRepo != nil && mem.ID != uuid.Nil {
		semanticTags := filterSemanticTags(mem.Tags)
		if len(semanticTags) > 0 {
			if candidates, scanErr := s.memRepo.FindByScope(ctx, mem.WorkspaceID, mem.ProjectID, string(mem.Scope), 200); scanErr == nil {
				for _, candidate := range candidates {
					if candidate.ID == mem.ID || candidate.Archived {
						continue
					}
					if candidate.ImportanceScore >= 0.75 {
						continue
					}
					candidateSemantic := filterSemanticTags([]string(candidate.Tags))
					if len(candidateSemantic) == 0 {
						continue
					}
					if tagOverlapRatio(candidateSemantic, semanticTags) >= 0.6 {
						edge := &domain.MemoryEdge{
							MemoryFromID:     mem.ID,
							MemoryToID:       candidate.ID,
							RelationshipType: domain.EdgeSupersedes,
							Weight:           1.0,
							WorkspaceID:      mem.WorkspaceID,
						}
						_ = s.edgeRepo.UpsertEdge(ctx, edge)
						_ = s.memRepo.SetArchived(ctx, candidate.ID, true)
					}
				}
			}
		}
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
		prefix := mem.Key + " " + strings.Join(mem.Tags, " ") + " "
		go s.embedAndStore(memID, mem.Content, prefix)
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
	// Limit=0 → no pagination: export always fetches all matching records.
	memories, _, err := s.memRepo.ListByWorkspaceProject(ctx, workspaceID, projectID, domain.MemoryListFilter{})
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

// BatchEmbed finds all memories that need (re)embedding with the configured embedder —
// those with no vector, AND those whose vector was produced by a different model — and
// embeds them. It is a no-op when the embedder is the noop variant. Returns the count of
// memories successfully embedded.
//
// Including the different-model case is what makes switching embedding provider/model a
// supported operation: a vector from another model sits in another vector space, scores 0
// in cosineSimilarity (dimension guard), and would otherwise remain invisible to semantic
// recall forever. Call this repeatedly (it works in batches) after changing the provider.
//
// Both branches write through UpdateEmbeddingKeepUpdatedAt, not UpdateEmbedding: every row
// this selects is an EXISTING memory nobody edited — only its embedding representation is
// catching up (to the current model, or, via the chunked branch, to the current chunking
// scheme). Bumping updated_at here would be the same defect RechunkStale was built to avoid
// (see its doc and #f96b6670): MarkStaleByAge and DecayRelevance key off that column, and a
// provider switch selects the WHOLE corpus into this loop, silently resetting both clocks in
// one run with the original timestamps recoverable from nowhere.
func (s *memoryService) BatchEmbed(ctx context.Context, workspaceID uuid.UUID) (int, error) {
	if embedding.IsNoop(s.embedder) {
		return 0, nil
	}

	const batchSize = 100
	memories, err := s.memRepo.ListNeedingEmbedding(ctx, workspaceID, s.embedder.Model(), batchSize)
	if err != nil {
		return 0, fmt.Errorf("memory batch embed: list: %w", err)
	}

	count := 0
	for _, m := range memories {
		prefix := m.Key + " " + strings.Join(m.Tags, " ") + " "

		// This loop is already sequential (one memory at a time), so the semaphore
		// never gates BatchEmbed against itself. It still needs a slot: BatchEmbed
		// shares s.embedder with embedAndStore and RecallWithStats, and a manual
		// backfill run concurrently with live write/recall traffic must count
		// against the same budget rather than adding an extra unbounded caller.
		release, semErr := s.acquireEmbedSem(ctx)
		if semErr != nil {
			log.Printf("memory batch embed: semaphore wait aborted: %v", semErr)
			break // ctx is dead; every remaining acquire would fail identically
		}

		if s.chunkRepo != nil {
			embedErr := s.embedChunkedStoring(ctx, m.ID, m.Content, prefix, s.memRepo.UpdateEmbeddingKeepUpdatedAt)
			release()
			if embedErr != nil {
				log.Printf("memory batch embed (chunked): id=%s: %v", m.ID, embedErr)
				continue
			}
			count++
			continue
		}

		vec, embedErr := s.embedder.Embed(ctx, prefix+m.Content)
		release()
		if embedErr != nil {
			log.Printf("memory batch embed: embed id=%s: %v", m.ID, embedErr)
			continue
		}
		if len(vec) == 0 {
			continue
		}
		if storeErr := s.memRepo.UpdateEmbeddingKeepUpdatedAt(ctx, m.ID, vec, s.embedder.Model(), s.embedder.Dimensions()); storeErr != nil {
			log.Printf("memory batch embed: store id=%s: %v", m.ID, storeErr)
			continue
		}
		count++
	}

	return count, nil
}

// BackfillChunks finds memories in workspaceID with no memory_chunks rows yet and embeds
// them through the chunked path — see the interface doc for why this needs its own
// selection query rather than reusing BatchEmbed's model-switch filter. A no-op (0, nil)
// when chunked embedding isn't configured or the embedder is a noop, matching BatchEmbed's
// convention: this is a normal "nothing to do yet" state, not an error.
//
// Writes through UpdateEmbeddingKeepUpdatedAt for the same reason as BatchEmbed above: this
// is chunking a memory that already existed with content nobody is editing right now, not a
// write to the memory itself.
func (s *memoryService) BackfillChunks(ctx context.Context, workspaceID uuid.UUID, limit int) (int, error) {
	if s.chunkRepo == nil || embedding.IsNoop(s.embedder) {
		return 0, nil
	}
	if limit <= 0 {
		limit = 100
	}

	memories, err := s.memRepo.ListNotYetChunked(ctx, workspaceID, limit)
	if err != nil {
		return 0, fmt.Errorf("backfill chunks: list: %w", err)
	}

	count := 0
	for _, m := range memories {
		prefix := m.Key + " " + strings.Join(m.Tags, " ") + " "
		if embedErr := s.embedChunkedStoring(ctx, m.ID, m.Content, prefix, s.memRepo.UpdateEmbeddingKeepUpdatedAt); embedErr != nil {
			log.Printf("memory backfill chunks: id=%s: %v", m.ID, embedErr)
			continue
		}
		count++
	}

	return count, nil
}

// RechunkStale re-embeds memories whose chunk offsets no longer index their content — the
// pre-#494 corpus, chunked as the composite key+content+tags rather than content alone. See
// the interface doc for the contract and ListNeedingRechunk's doc for why the selection reads
// chunk offsets instead of a version column.
//
// `remaining` is re-counted from the database AFTER the batch, never derived from
// `processed`. The two are independent on purpose: a repair job that reports how many rows it
// touched cannot tell "healed them all" from "never selected the sick ones", and the second
// is what actually happened in #b052cdda. When rows fail to re-embed, `processed` falls
// behind the batch size while `remaining` stops dropping — visible, instead of a confident
// count and a corpus that never converges.
func (s *memoryService) RechunkStale(ctx context.Context, workspaceID uuid.UUID, limit int) (processed, remaining int, err error) {
	if s.chunkRepo == nil || embedding.IsNoop(s.embedder) {
		// Not an error: same "nothing to do in this configuration" convention as
		// BackfillChunks. Still report `remaining` — an operator running this against a
		// deployment with chunking switched off deserves to see the population is there
		// and untouched, rather than a bare 0 that reads like "already converged".
		idle, countErr := s.memRepo.CountNeedingRechunk(ctx, workspaceID)
		if countErr != nil {
			return 0, 0, fmt.Errorf("rechunk stale: count: %w", countErr)
		}
		return 0, idle, nil
	}
	if limit <= 0 {
		limit = 100
	}

	memories, listErr := s.memRepo.ListNeedingRechunk(ctx, workspaceID, limit)
	if listErr != nil {
		return 0, 0, fmt.Errorf("rechunk stale: list: %w", listErr)
	}

	for _, m := range memories {
		prefix := m.Key + " " + strings.Join(m.Tags, " ") + " "
		// UpdateEmbeddingKeepUpdatedAt, not UpdateEmbedding: this is a repair of how an
		// existing memory was indexed, not an edit of the memory. Bumping updated_at here
		// would irreversibly reset staleness (MarkStaleByAge) and relevance decay
		// (DecayRelevance) for the entire corpus in one run.
		if embedErr := s.embedChunkedStoring(ctx, m.ID, m.Content, prefix, s.memRepo.UpdateEmbeddingKeepUpdatedAt); embedErr != nil {
			log.Printf("memory rechunk stale: id=%s: %v", m.ID, embedErr)
			continue
		}
		processed++
	}

	remaining, countErr := s.memRepo.CountNeedingRechunk(ctx, workspaceID)
	if countErr != nil {
		return processed, 0, fmt.Errorf("rechunk stale: count remaining: %w", countErr)
	}
	return processed, remaining, nil
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
	// recencyWeight=0: this related-memory path keeps legacy FTS-only ordering.
	results, err := s.memRepo.FullTextSearch(ctx, query, mem.WorkspaceID, projID, "", nil, limit+1, 0)
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
	// Scope/Tags/TagsAny are part of the key: they change WHICH rows are eligible, so two
	// calls differing only in scope have genuinely different results. Omitting them would
	// let a scope-less call populate the entry that a scope='agent' call then reads —
	// serving exactly the leak the filter exists to prevent, from cache.
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%d|%f|%s|%v|%v",
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
		opts.Scope,
		opts.Tags,
		opts.TagsAny,
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

	graphFilter := opts.SearchFilter()

	seedOpts := domain.RecallOpts{
		Query:       opts.Query,
		WorkspaceID: opts.WorkspaceID,
		Limit:       recallGraphSeedLimit,
		Scope:       domain.MemoryScope(opts.Scope),
		Tags:        opts.Tags,
		TagsAny:     opts.TagsAny,
	}
	if projID != nil {
		seedOpts.ProjectID = *projID
	}

	// NOTE: the seed recall's SearchMode is deliberately dropped here. RecallGraph
	// memoises its results in recallGraphCache, so a mode surfaced from this path
	// could be served from a cache entry filled while the embedder was healthy —
	// i.e. it would report "hybrid" for a call that never touched an embedder.
	// A stale mode is worse than no mode for anything that gates on it, so the
	// recall_graph envelope stays mode-free; use /memories/search for the signal.
	seedResults, _, err := s.Recall(ctx, seedOpts)
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
				Key:             sm.Key,
				Scope:           sm.Scope,
				Tags:            []string(sm.Tags),
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
		edges, edgeErr := s.edgeRepo.GetNeighbors(ctx, frontier, opts.WorkspaceID, opts.WeightThreshold, 200)
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

			// GetByID is a primary-key lookup with no workspace predicate, so an
			// edge pointing out of the tenant would surface a foreign memory here.
			// GetNeighbors already confines edges to opts.WorkspaceID; this is the
			// second lock on the same door — the node itself must also belong to it.
			if mem.WorkspaceID != opts.WorkspaceID {
				continue
			}

			// Drop low-importance graph-expanded memories.
			if mem.ImportanceScore < graphMinImportance {
				continue
			}

			// Edges are scope-blind, so an in-scope seed can be adjacent to an
			// out-of-scope memory. Expanded neighbours must satisfy the same
			// eligibility filter the seed recall applied, or graph expansion becomes
			// a way around it.
			if !graphFilter.Allows(*mem) {
				continue
			}

			nodes[neighborID] = &nodeInfo{
				result: domain.RecallGraphResult{
					ID:              mem.ID,
					Key:             mem.Key,
					Scope:           mem.Scope,
					Tags:            []string(mem.Tags),
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

// embedBudgetPerCall is the wall-clock budget for a single embedder round trip. It was
// previously the budget for an entire memory, which silently became a cap on document
// length once chunking multiplied the calls per memory (#67f4e0d9).
const embedBudgetPerCall = 30 * time.Second

// embedBudgetCap bounds the scaled budget so a pathologically long document cannot pin
// a goroutine (and its embedSem slot) indefinitely.
const embedBudgetCap = 5 * time.Minute

// embedMaxAttempts is the number of tries for one embed call. Under concurrent load the
// shared TEI instance returns context-deadline/5xx transiently, which is retryable — the
// pre-fix code had no retry at all, so a single blip lost the memory permanently (the
// goroutine logs and exits; nothing re-embeds it until a manual reindex).
const embedMaxAttempts = 3

// embedBudget returns the total budget for embedding a memory split into n chunks. Chunks
// are sent in batches of up to maxClientBatchSize per round trip, so the budget scales
// with the number of ROUND TRIPS, plus retry headroom — not with the chunk count directly.
func embedBudget(chunks int) time.Duration {
	if chunks < 1 {
		chunks = 1
	}
	roundTrips := (chunks + embedBatchSize - 1) / embedBatchSize
	budget := time.Duration(roundTrips) * embedBudgetPerCall * embedMaxAttempts
	if budget > embedBudgetCap {
		return embedBudgetCap
	}
	return budget
}

// embedBatchSize mirrors the embedder's client batch limit. Kept in sync with
// embedding.maxClientBatchSize; a mismatch only affects budget arithmetic, never correctness.
const embedBatchSize = 32

// embedWithRetry calls EmbedBatch with bounded exponential backoff. It gives up
// immediately when the caller's context is done — retrying against an expired deadline
// burns the remaining budget for nothing.
func embedWithRetry(ctx context.Context, embedder embedding.Embedder, texts []string) ([][]float32, error) {
	var lastErr error
	for attempt := 0; attempt < embedMaxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("embed retry aborted: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}
		vecs, err := embedder.EmbedBatch(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			// Budget exhausted — further attempts cannot succeed.
			return nil, fmt.Errorf("embed after %d attempt(s): %w", attempt+1, err)
		}
	}
	return nil, fmt.Errorf("embed after %d attempts: %w", embedMaxAttempts, lastErr)
}

// isForeignKeyViolation reports whether err is a Postgres FK violation (SQLSTATE 23503).
// For the chunked embed path this means the memory row was deleted while its embedding
// goroutine was still running — a normal race with sweeps/deletes, not an embedder fault.
// Counting it as an embedding failure masks the real ones (34 of these appeared alongside
// the 538 genuine timeouts in the #67f4e0d9 window).
func isForeignKeyViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23503"
	}
	return false
}
