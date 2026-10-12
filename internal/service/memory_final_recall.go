package service

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

// FinalRecallService is additive: old callers retain their search/graph contracts.
// An implementation without this capability must return 503, never legacy search.
type FinalRecallService interface {
	FinalRecall(context.Context, domain.FinalRecallOpts) (*domain.FinalRecallResult, error)
}

type recallCandidateReader interface {
	RecallCandidatesByIDs(context.Context, uuid.UUID, []uuid.UUID) ([]domain.Memory, error)
}

const finalRecallMaxCandidates = 200
const finalRecallNoiseCeiling = 0.012
const finalRecallScoreBand = 0.001

func (s *memoryService) FinalRecall(ctx context.Context, req domain.FinalRecallOpts) (*domain.FinalRecallResult, error) {
	opts := req.RecallOpts
	if opts.Limit < 1 || opts.Limit > 50 || opts.Offset < 0 || opts.Offset > 150 {
		return nil, apierror.BadRequest("final recall requires limit in [1,50] and offset in [0,150]")
	}
	if opts.MinImportance == nil {
		value := defaultMinImportance
		opts.MinImportance = &value
	}
	candidateLimit := min(finalRecallMaxCandidates, max(10, opts.Limit+opts.Offset))
	candidateOpts := opts
	candidateOpts.Limit, candidateOpts.Offset = candidateLimit, 0
	base, stats, err := s.recallCandidates(ctx, candidateOpts, false)
	if err != nil {
		return nil, err
	}
	base = finalEligible(base, opts, time.Now())
	base = finalRerank(base, opts)
	best := 0.0
	for _, row := range base {
		best = max(best, row.Score)
	}
	// Same measured noise signal as the existing MCP: no changed thresholds,
	// query rewrites or gold-key pins. Gate the candidate set before graph expansion.
	if stats.DenseRows > 0 && best < finalRecallNoiseCeiling && !finalQueryOverlap(opts.Query, base) {
		base = nil
	}
	rows := make([]domain.FinalRecallItem, 0, len(base))
	for _, row := range base {
		rows = append(rows, domain.FinalRecallItem{ScoredMemory: row, Provenance: domain.ProvenanceRecall})
	}
	if req.Graph && len(base) > 0 && opts.Limit > 1 {
		graph, graphErr := s.finalGraphCandidates(ctx, base, opts)
		if graphErr != nil {
			return nil, graphErr
		}
		// Conditional admission: a neighbour must have query evidence of its own.
		// An edge alone cannot displace an answer-bearing retrieval hit.
		sort.SliceStable(graph, func(i, j int) bool { return graph[i].CompositeScore > graph[j].CompositeScore })
		reserve := max(1, opts.Limit/4)
		seen := make(map[uuid.UUID]bool, len(base))
		for _, row := range base {
			seen[row.ID] = true
		}
		picked := make([]domain.FinalRecallItem, 0, reserve)
		for _, row := range graph {
			if seen[row.ID] || !finalQueryOverlap(opts.Query, []domain.ScoredMemory{row.ScoredMemory}) {
				continue
			}
			picked = append(picked, row)
			seen[row.ID] = true
			if len(picked) == reserve {
				break
			}
		}
		// Preserve pagination over the final ordered union. The graph reserve applies
		// to the requested window and displaces only that window's tail.
		keep := min(len(rows), opts.Offset+opts.Limit-len(picked))
		if keep >= opts.Offset {
			rows = append(rows[:keep], picked...)
		}
	}
	start := min(opts.Offset, len(rows))
	page := make([]domain.FinalRecallItem, 0, opts.Limit)
	docs := 0
	for _, row := range rows[start:] {
		if row.SourceType == domain.SourceDoc {
			if docs == docMaxSlots(opts.Limit) {
				continue
			}
			docs++
		}
		page = append(page, row)
		if len(page) == opts.Limit {
			break
		}
	}
	rows = page
	if rows == nil {
		rows = []domain.FinalRecallItem{}
	}
	ids := make([]uuid.UUID, 0, len(rows))
	seen := make(map[uuid.UUID]bool, len(rows))
	count := 0
	for _, row := range rows {
		if row.GraphBoost {
			count++
		}
		if row.SourceType != domain.SourceDoc && row.ID != uuid.Nil && !seen[row.ID] {
			ids = append(ids, row.ID)
			seen[row.ID] = true
		}
	}
	if len(ids) > 0 {
		_ = s.memRepo.BoostRelevance(ctx, ids)
		_ = s.memRepo.TouchAccessed(ctx, ids)
	}
	if len(rows) == 0 {
		pkgmetrics.RecordMemoryRecallEmpty(string(stats.Mode))
	}
	top := 0.0
	for _, row := range rows {
		if !row.GraphBoost && row.Score < pinnedSentinelScore {
			top = max(top, row.Score)
		}
	}
	pkgmetrics.ObserveMemoryRecallTopScore(string(stats.Mode), top)
	return &domain.FinalRecallResult{Items: rows, Stats: stats, CandidateLimit: candidateLimit, GraphBoostCount: count}, nil
}

func finalEligible(rows []domain.ScoredMemory, opts domain.RecallOpts, now time.Time) []domain.ScoredMemory {
	rows = applyExtendedFilters(rows, opts)
	out := rows[:0]
	seen := map[uuid.UUID]bool{}
	for _, row := range rows {
		if row.WorkspaceID != opts.WorkspaceID || row.ID == uuid.Nil || seen[row.ID] {
			continue
		}
		if opts.ProjectID != uuid.Nil {
			if row.ProjectID == nil {
				if row.Scope != domain.ScopeWorkspace || !slices.Contains(row.Tags, domain.KindPinned) {
					continue
				}
			} else if *row.ProjectID != opts.ProjectID {
				continue
			}
		}
		if !opts.IncludeArchived && (row.Archived || row.Status == domain.MemoryStatusArchived) {
			continue
		}
		if !opts.IncludeExpired && row.ExpiresAt != nil && !row.ExpiresAt.After(now) {
			continue
		}
		seen[row.ID] = true
		out = append(out, row)
	}
	return out
}

// finalRerank retains the query/key/project/scope tiers and importance tie-bands.
// Within each tier, dense memory hits use their query's raw cosine similarity:
// RRF rewards appearing in both arms and can otherwise bury a semantic answer.
// Sparse-only hits, documents and pins retain their slots. Graph scores are separate.
func finalRerank(rows []domain.ScoredMemory, opts domain.RecallOpts) []domain.ScoredMemory {
	q := strings.ToLower(opts.Query)
	boost := func(row domain.ScoredMemory) int {
		key := strings.ToLower(row.Key)
		value := 0
		if key != "" {
			switch {
			case key == q:
				value = 3
			case strings.Contains(q, key) || strings.Contains(key, q):
				value = 2
			default:
				shared := map[string]bool{}
				for _, tok := range strings.FieldsFunc(key, func(r rune) bool { return r == '-' || r == '_' }) {
					if len(tok) >= 4 && strings.Contains(q, tok) {
						shared[tok] = true
					}
				}
				if len(shared) >= 3 {
					value = 1
				}
			}
		}
		if opts.ProjectID != uuid.Nil && row.ProjectID != nil && *row.ProjectID == opts.ProjectID {
			value++
		}
		if opts.Scope != "" && row.Scope == opts.Scope {
			value++
		}
		return value
	}
	sort.SliceStable(rows, func(i, j int) bool { return boost(rows[i]) > boost(rows[j]) })
	start := len(rows)
	for i, row := range rows {
		if boost(row) == 0 {
			start = i
			break
		}
	}
	tail := rows[start:]
	sort.SliceStable(tail, func(i, j int) bool { return tail[i].Score > tail[j].Score })
	for head := 0; head < len(tail); {
		end := head + 1
		for end < len(tail) && tail[head].Score-tail[end].Score <= finalRecallScoreBand {
			end++
		}
		band := tail[head:end]
		sort.SliceStable(band, func(i, j int) bool { return band[i].ImportanceScore > band[j].ImportanceScore })
		head = end
	}
	for head := 0; head < len(rows); {
		end := head + 1
		for end < len(rows) && boost(rows[end]) == boost(rows[head]) {
			end++
		}
		indices := make([]int, 0, end-head)
		dense := make([]domain.ScoredMemory, 0, end-head)
		for i := head; i < end; i++ {
			row := rows[i]
			if row.DenseScore != nil && row.SourceType != domain.SourceDoc && row.Score < pinnedSentinelScore {
				indices = append(indices, i)
				dense = append(dense, row)
			}
		}
		sort.SliceStable(dense, func(i, j int) bool { return *dense[i].DenseScore > *dense[j].DenseScore })
		for i, index := range indices {
			rows[index] = dense[i]
		}
		head = end
	}
	return rows
}

func finalQueryOverlap(query string, rows []domain.ScoredMemory) bool {
	split := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	}
	tokens := []string{}
	for _, token := range split(query) {
		if len([]rune(token)) >= 4 {
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 {
		return true
	}
	for _, row := range rows {
		have := split(row.Key + " " + row.Content + " " + strings.Join(row.Tags, " "))
		for _, token := range tokens {
			if slices.Contains(have, token) {
				return true
			}
		}
	}
	return false
}

func (s *memoryService) finalGraphCandidates(ctx context.Context, base []domain.ScoredMemory, opts domain.RecallOpts) ([]domain.FinalRecallItem, error) {
	if s.edgeRepo == nil {
		return nil, nil
	}
	reader, ok := s.memRepo.(recallCandidateReader)
	if !ok {
		return nil, apierror.ServiceUnavailable("side-effect-free graph recall is unavailable")
	}
	type path struct {
		score float64
		hop   int
	}
	paths := map[uuid.UUID]path{}
	frontier := []uuid.UUID{}
	for _, row := range base {
		if row.SourceType == domain.SourceDoc {
			continue
		}
		paths[row.ID] = path{score: row.Score}
		frontier = append(frontier, row.ID)
		if len(frontier) == recallGraphSeedLimit {
			break
		}
	}
	result := []domain.FinalRecallItem{}
	budget := finalRecallMaxCandidates
	for hop := 1; hop <= 2 && len(frontier) > 0 && budget > 0; hop++ {
		edges, err := s.edgeRepo.GetNeighbors(ctx, frontier, opts.WorkspaceID, 0.1, budget)
		if err != nil {
			return nil, fmt.Errorf("final recall graph: %w", err)
		}
		pending := map[uuid.UUID]path{}
		for _, edge := range edges {
			for _, pair := range [][2]uuid.UUID{{edge.MemoryFromID, edge.MemoryToID}, {edge.MemoryToID, edge.MemoryFromID}} {
				if !slices.Contains(frontier, pair[0]) || edge.Weight < 0.1 {
					continue
				}
				if _, seen := paths[pair[1]]; seen {
					continue
				}
				candidate := path{score: paths[pair[0]].score * float64(edge.Weight), hop: hop}
				if old, exists := pending[pair[1]]; !exists || candidate.score > old.score {
					pending[pair[1]] = candidate
				}
			}
		}
		ids := make([]uuid.UUID, 0, len(pending))
		for id := range pending {
			ids = append(ids, id)
		}
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		if len(ids) > budget {
			ids = ids[:budget]
		}
		budget -= len(ids)
		memories, err := reader.RecallCandidatesByIDs(ctx, opts.WorkspaceID, ids)
		if err != nil {
			return nil, fmt.Errorf("final recall graph candidates: %w", err)
		}
		frontier = nil
		for _, memory := range memories {
			p, exists := pending[memory.ID]
			if !exists || memory.ImportanceScore < graphMinImportance {
				continue
			}
			row := domain.ScoredMemory{Memory: memory}
			if len(finalEligible([]domain.ScoredMemory{row}, opts, time.Now())) == 0 {
				continue
			}
			paths[memory.ID] = p
			frontier = append(frontier, memory.ID)
			result = append(result, domain.FinalRecallItem{ScoredMemory: row, GraphBoost: true, Provenance: domain.ProvenanceGraph, HopDistance: p.hop, CompositeScore: p.score})
		}
	}
	return result, nil
}
