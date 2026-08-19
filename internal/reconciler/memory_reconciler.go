package reconciler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/embedding"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

const (
	monitorBatchCap   = 500
	linkerWindowH     = 24 * time.Hour
	linkerMaxBatch    = 50 // max memories to process per linker run
	linkerCandidates  = 20
	simThreshold      = 0.85
	autoActThreshold  = 0.70
	highImportance    = float32(0.8)
	defaultStaleAfter = 30 * 24 * time.Hour
)

// Config holds runtime configuration for MemoryReconciler.
type Config struct {
	// Epoch is the cold-start gate: only memories created after Epoch are eligible
	// for stale marking. Zero value means "use time.Now() on first run" (safe default).
	Epoch time.Time
	// StaleAfter is the inactivity window before a memory is marked stale.
	// Defaults to 30 days when zero.
	StaleAfter time.Duration
}

// MemoryReconciler runs the monitor, linker, and decision-engine phases for the
// memory freshness lifecycle. It plugs into the existing 6h scheduler in cmd/api/main.go.
type MemoryReconciler struct {
	memRepo    repository.MemoryRepository
	edgeRepo   repository.MemoryEdgeRepository
	embedder   embedding.Embedder
	epoch      time.Time
	staleAfter time.Duration
}

// New creates a MemoryReconciler. If cfg.Epoch is zero, the current time is used
// (safe default: only future memories are eligible for stale marking).
// If cfg.StaleAfter is zero, defaults to 30 days.
// embedder may be nil (or embedding.NewNoopEmbedder()); linker phase is skipped when it is a noop.
func New(memRepo repository.MemoryRepository, edgeRepo repository.MemoryEdgeRepository, embedder embedding.Embedder, cfg Config) *MemoryReconciler {
	epoch := cfg.Epoch
	if epoch.IsZero() {
		epoch = time.Now()
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = defaultStaleAfter
	}
	if embedder == nil {
		embedder = embedding.NewNoopEmbedder()
	}
	return &MemoryReconciler{
		memRepo:    memRepo,
		edgeRepo:   edgeRepo,
		embedder:   embedder,
		epoch:      epoch,
		staleAfter: staleAfter,
	}
}

// Run executes all reconciler phases sequentially. Errors in one phase are logged
// but do not abort subsequent phases (monitor failure should not block linker).
func (r *MemoryReconciler) Run(ctx context.Context) error {
	if err := r.runMonitor(ctx); err != nil {
		log.Printf("reconciler: monitor phase error: %v", err)
	}
	if !embedding.IsNoop(r.embedder) {
		if err := r.runLinker(ctx); err != nil {
			log.Printf("reconciler: linker phase error: %v", err)
		}
	}
	return nil
}

// runMonitor executes the two bulk UPDATE steps (expire + stale detection).
func (r *MemoryReconciler) runMonitor(ctx context.Context) error {
	n, err := r.memRepo.ExpireByValidUntil(ctx)
	if err != nil {
		return fmt.Errorf("expire valid_until: %w", err)
	}
	if n > 0 {
		log.Printf("reconciler: expired %d memories (valid_until passed)", n)
	}

	n, err = r.memRepo.MarkStaleByAge(ctx, r.epoch, r.staleAfter)
	if err != nil {
		return fmt.Errorf("mark stale by age: %w", err)
	}
	if n > 0 {
		log.Printf("reconciler: marked %d memories as stale (age > %v)", n, r.staleAfter)
	}
	return nil
}

// runLinker fetches recently created embedded memories and runs the decision engine
// on semantically similar pairs.
func (r *MemoryReconciler) runLinker(ctx context.Context) error {
	since := time.Now().Add(-linkerWindowH)
	mems, err := r.memRepo.ListCreatedSince(ctx, since, linkerMaxBatch)
	if err != nil {
		return fmt.Errorf("list created since: %w", err)
	}
	if len(mems) == 0 {
		return nil
	}

	// Batch-embed all memory contents in one API call. Deliberately NOT gated by
	// memoryService's embedSem (#3d10774e): this reconciler holds its own embedder
	// reference (constructed alongside, not through, memoryService) with no shared
	// semaphore plumbed in, it runs on a 6h ticker over at most linkerMaxBatch=50
	// memories, and it is already one round trip rather than one call per memory.
	// If this ever needs the same bound, share memoryService's *embedding.Semaphore
	// (or equivalent) at construction in cmd/api/main.go rather than duplicating a
	// second unbounded-by-default limit here.
	texts := make([]string, len(mems))
	for i, m := range mems {
		texts[i] = m.Content
	}
	vecs, err := r.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed batch: %w", err)
	}

	for i, mem := range mems {
		if i >= len(vecs) || len(vecs[i]) == 0 {
			continue
		}
		if err := r.processMemory(ctx, mem, vecs[i]); err != nil {
			log.Printf("reconciler: process memory %s: %v", mem.ID, err)
		}
	}
	return nil
}

// processMemory runs the decision engine for a single memory against its top-20
// similar neighbours in the same workspace.
func (r *MemoryReconciler) processMemory(ctx context.Context, mem domain.Memory, queryVec []float32) error {
	// Zero filter on purpose: the linker looks for near-duplicates and relations across
	// the whole workspace, so it must see every scope — unlike Recall, which is answering
	// a caller who asked for a specific one.
	candidates, err := r.memRepo.VectorSearch(ctx, queryVec, mem.WorkspaceID, nil, domain.MemorySearchFilter{}, linkerCandidates)
	if err != nil {
		return fmt.Errorf("vector search: %w", err)
	}

	for _, c := range candidates {
		if c.ID == mem.ID {
			continue // skip self
		}
		if c.Score < simThreshold {
			continue // skip low-similarity pairs
		}
		// Already handled — skip memories already superseded or archived.
		if c.Status == domain.MemoryStatusSuperseded || c.Status == domain.MemoryStatusArchived {
			continue
		}
		if err := r.decide(ctx, mem, c.Memory, c.Score); err != nil {
			log.Printf("reconciler: decide pair (%s, %s): %v", mem.ID, c.ID, err)
		}
	}
	return nil
}

// decide applies the confidence-based decision engine to a (query, candidate) pair.
// It determines which is newer/older and routes to supersede or markReviewNeeded.
func (r *MemoryReconciler) decide(ctx context.Context, a, b domain.Memory, _ float64) error {
	var older, newer domain.Memory
	if a.CreatedAt.After(b.CreatedAt) {
		newer, older = a, b
	} else {
		newer, older = b, a
	}

	// Protect high-importance memories from automatic supersession.
	if older.ImportanceScore >= highImportance {
		return nil
	}

	conf := computeConfidence(older, newer)
	if conf >= autoActThreshold {
		return r.supersede(ctx, older, newer, conf)
	}
	return r.markReviewNeeded(ctx, older)
}

// supersede marks older as superseded by newer, sets superseded_by, and creates a KG edge.
func (r *MemoryReconciler) supersede(ctx context.Context, older, newer domain.Memory, confidence float64) error {
	newID := newer.ID
	if err := r.memRepo.SetMemoryStatus(ctx, older.ID, domain.MemoryStatusSuperseded, &newID); err != nil {
		return fmt.Errorf("set superseded status: %w", err)
	}
	edge := &domain.MemoryEdge{
		ID:               uuid.New(),
		MemoryFromID:     newer.ID,
		MemoryToID:       older.ID,
		RelationshipType: domain.EdgeSupersedes,
		Weight:           float32(confidence),
		WorkspaceID:      newer.WorkspaceID,
	}
	if err := r.edgeRepo.UpsertEdge(ctx, edge); err != nil {
		return fmt.Errorf("upsert supersedes edge: %w", err)
	}
	log.Printf("reconciler: superseded memory_id=%s by %s (confidence=%.2f)", older.ID, newer.ID, confidence)
	return nil
}

// markReviewNeeded sets the memory status to review_needed without creating a KG edge.
func (r *MemoryReconciler) markReviewNeeded(ctx context.Context, mem domain.Memory) error {
	if mem.Status == domain.MemoryStatusReviewNeeded {
		return nil // already set, idempotent
	}
	if err := r.memRepo.SetMemoryStatus(ctx, mem.ID, domain.MemoryStatusReviewNeeded, nil); err != nil {
		return fmt.Errorf("set review_needed status: %w", err)
	}
	log.Printf("reconciler: review_needed memory_id=%s (below auto-act threshold)", mem.ID)
	return nil
}

// computeConfidence returns a rule-based confidence score [0.0, 1.0] that older
// is superseded by newer. Higher = more certain.
func computeConfidence(older, newer domain.Memory) float64 {
	var conf float64

	// Same key prefix (first 2 segments, e.g. "episode:garfield"): +0.3
	if sameKeyPrefix(older.Key, newer.Key, 2) {
		conf += 0.3
	}

	// Tag overlap >= 50%: +0.2
	if tagOverlapRatio(older.Tags, newer.Tags) >= 0.5 {
		conf += 0.2
	}

	// Content simhash Hamming distance <= 10 bits: +0.3
	if older.ContentSimhash != nil && newer.ContentSimhash != nil {
		if hammingDistance(*older.ContentSimhash, *newer.ContentSimhash) <= 10 {
			conf += 0.3
		}
	}

	// Newer was created at least 1 hour after older: +0.2
	if newer.CreatedAt.After(older.CreatedAt.Add(time.Hour)) {
		conf += 0.2
	}

	if conf > 1.0 {
		conf = 1.0
	}
	return conf
}

// sameKeyPrefix returns true if a and b share the same first n slash/colon-separated segments.
func sameKeyPrefix(a, b string, n int) bool {
	segsA := splitKey(a)
	segsB := splitKey(b)
	if len(segsA) < n || len(segsB) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if segsA[i] != segsB[i] {
			return false
		}
	}
	return true
}

// splitKey splits a memory key on ":" or "/" separators.
func splitKey(key string) []string {
	key = strings.ReplaceAll(key, "/", ":")
	return strings.Split(key, ":")
}

// tagOverlapRatio returns the fraction of tags in the smaller set that appear in the larger set.
func tagOverlapRatio(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	matches := 0
	for _, t := range b {
		if _, ok := setA[t]; ok {
			matches++
		}
	}
	// Use the smaller set as denominator.
	denom := len(a)
	if len(b) < denom {
		denom = len(b)
	}
	return float64(matches) / float64(denom)
}

// hammingDistance returns the number of differing bits between two int64 SimHash values.
func hammingDistance(a, b int64) int {
	x := uint64(a ^ b)
	count := 0
	for x != 0 {
		count += int(x & 1)
		x >>= 1
	}
	return count
}
