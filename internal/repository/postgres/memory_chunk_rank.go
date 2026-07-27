package postgres

import (
	"sort"

	"github.com/google/uuid"
)

// chunkScore is one scored chunk produced during vector search: the cosine
// similarity of a single chunk's embedding against the query vector, tagged
// with the memory that chunk belongs to.
//
// Chunked embedding means one memory contributes N vectors instead of one (see
// ADR-0002). Everything downstream of vector search — most importantly
// reciprocalRankFusion, which is rank-based — assumes one entry per memory, so
// the per-chunk scores must be reduced back to per-memory before they leave the
// repository.
type chunkScore struct {
	memoryID uuid.UUID
	chunkIdx int
	score    float64
}

// bestChunkPerMemory reduces per-chunk scores to at most one entry per memory —
// max-over-chunks — and returns them ordered by descending score, truncated to
// limit. A limit <= 0 means "no truncation".
//
// Two ordering rules exist to make the result a deterministic function of its
// input. Without them the recall gate would flake on ties, and a flaky gate
// teaches people to ignore it:
//
//   - Between memories with equal scores, the lower memoryID wins. Cosine ties
//     are not hypothetical here: duplicate and near-duplicate memories are
//     routine in this corpus, and identical text yields bit-identical vectors.
//   - Within one memory, when two chunks tie, the lower chunkIdx wins. Adjacent
//     chunks overlap by ~300 runes, so two chunks covering the same fact can
//     score identically; the earlier one is the more useful provenance because
//     it starts nearer the fact.
//
// The truncation to limit happens AFTER the reduction, never before. Cutting the
// chunk list first would let one long memory's chunks consume the whole page and
// starve every other memory out of the result — the same class of bug as
// applying a limit before pinning (evc-mesh#384).
func bestChunkPerMemory(scores []chunkScore, limit int) []chunkScore {
	if len(scores) == 0 {
		return nil
	}

	// Reduce: keep the highest-scoring chunk per memory.
	best := make(map[uuid.UUID]chunkScore, len(scores))
	for _, cs := range scores {
		cur, seen := best[cs.memoryID]
		if !seen || betterChunk(cs, cur) {
			best[cs.memoryID] = cs
		}
	}

	out := make([]chunkScore, 0, len(best))
	for _, cs := range best {
		out = append(out, cs)
	}

	// Map iteration order is randomised, so this sort is what makes the output
	// deterministic — it must fully order the slice, with no ties left over.
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return uuidLess(out[i].memoryID, out[j].memoryID)
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// betterChunk reports whether a should replace b as the representative chunk of
// a memory: a higher score wins, and an equal score is broken by the lower
// chunk index.
func betterChunk(a, b chunkScore) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.chunkIdx < b.chunkIdx
}

// uuidLess orders UUIDs by their raw bytes, giving a stable, content-derived
// tiebreak that does not depend on map iteration or row order.
func uuidLess(a, b uuid.UUID) bool {
	ab, bb := a[:], b[:]
	for i := range ab {
		if ab[i] != bb[i] {
			return ab[i] < bb[i]
		}
	}
	return false
}
