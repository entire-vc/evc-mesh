package postgres

import (
	"testing"

	"github.com/google/uuid"
)

// chunkMemID builds a deterministic UUID from a single byte so tests can reason about
// ordering without depending on random IDs.
func chunkMemID(b byte) uuid.UUID {
	var u uuid.UUID
	u[0] = b
	return u
}

func chunkIDs(scores []chunkScore) []uuid.UUID {
	out := make([]uuid.UUID, len(scores))
	for i, cs := range scores {
		out[i] = cs.memoryID
	}
	return out
}

func TestBestChunkPerMemory_DedupsToOneSlotPerMemory(t *testing.T) {
	a := chunkMemID(1)
	// One memory contributing five chunks must still occupy exactly one slot.
	in := []chunkScore{
		{a, 0, 0.10},
		{a, 1, 0.90},
		{a, 2, 0.40},
		{a, 3, 0.55},
		{a, 4, 0.20},
	}

	got := bestChunkPerMemory(in, 10)

	if len(got) != 1 {
		t.Fatalf("one memory must yield exactly one result, got %d: %+v", len(got), got)
	}
	if got[0].score != 0.90 {
		t.Errorf("score = %v, want the max chunk score 0.90", got[0].score)
	}
	if got[0].chunkIdx != 1 {
		t.Errorf("chunkIdx = %d, want 1 (the winning chunk, for provenance)", got[0].chunkIdx)
	}
}

func TestBestChunkPerMemory_RanksByBestChunkNotByChunkCount(t *testing.T) {
	// The regression this guards: a memory with many mediocre chunks must not
	// outrank a memory with one excellent chunk. Sorting or scoring by anything
	// aggregate (sum, count, mean) would invert this.
	many, one := chunkMemID(1), chunkMemID(2)
	in := []chunkScore{
		{many, 0, 0.50}, {many, 1, 0.51}, {many, 2, 0.49}, {many, 3, 0.52}, {many, 4, 0.48},
		{one, 0, 0.95},
	}

	got := bestChunkPerMemory(in, 10)

	if len(got) != 2 {
		t.Fatalf("want 2 memories, got %d", len(got))
	}
	if got[0].memoryID != one {
		t.Errorf("first result = %v, want the single-strong-chunk memory %v", got[0].memoryID, one)
	}
}

func TestBestChunkPerMemory_LimitAppliesAfterDedup(t *testing.T) {
	// The core trap: truncating the CHUNK list to limit before reducing would
	// let one memory's chunks fill the whole page. Here memory `hog` supplies
	// the three top-scoring chunks; with a limit of 2 the page must still
	// contain two DISTINCT memories.
	hog, b, c := chunkMemID(1), chunkMemID(2), chunkMemID(3)
	in := []chunkScore{
		{hog, 0, 0.99}, {hog, 1, 0.98}, {hog, 2, 0.97},
		{b, 0, 0.60},
		{c, 0, 0.50},
	}

	got := bestChunkPerMemory(in, 2)

	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	seen := map[uuid.UUID]bool{}
	for _, cs := range got {
		if seen[cs.memoryID] {
			t.Fatalf("memory %v occupies more than one slot: %+v", cs.memoryID, got)
		}
		seen[cs.memoryID] = true
	}
	if got[0].memoryID != hog || got[1].memoryID != b {
		t.Errorf("order = %v, want [hog b]", chunkIDs(got))
	}
}

func TestBestChunkPerMemory_DeterministicOnTies(t *testing.T) {
	// Equal scores across memories, and equal scores within a memory. Both must
	// resolve identically on every run — map iteration order must not leak out.
	a, b := chunkMemID(1), chunkMemID(2)
	in := []chunkScore{
		{b, 3, 0.70},
		{b, 1, 0.70},
		{a, 2, 0.70},
		{a, 0, 0.70},
	}

	first := bestChunkPerMemory(in, 10)
	for i := 0; i < 200; i++ {
		got := bestChunkPerMemory(in, 10)
		if len(got) != len(first) {
			t.Fatalf("run %d: length changed %d -> %d", i, len(first), len(got))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d: position %d changed %+v -> %+v", i, j, first[j], got[j])
			}
		}
	}

	if first[0].memoryID != a || first[1].memoryID != b {
		t.Errorf("tie between memories = %v, want lower UUID first [a b]", chunkIDs(first))
	}
	if first[0].chunkIdx != 0 || first[1].chunkIdx != 1 {
		t.Errorf("tie within memory picked chunkIdx %d/%d, want the lower index 0/1",
			first[0].chunkIdx, first[1].chunkIdx)
	}
}

func TestBestChunkPerMemory_EdgeCases(t *testing.T) {
	a, b := chunkMemID(1), chunkMemID(2)

	if got := bestChunkPerMemory(nil, 10); got != nil {
		t.Errorf("nil input = %+v, want nil", got)
	}
	if got := bestChunkPerMemory([]chunkScore{}, 10); got != nil {
		t.Errorf("empty input = %+v, want nil", got)
	}

	in := []chunkScore{{a, 0, 0.5}, {b, 0, 0.4}}
	if got := bestChunkPerMemory(in, 0); len(got) != 2 {
		t.Errorf("limit 0 must not truncate, got %d results", len(got))
	}
	if got := bestChunkPerMemory(in, -1); len(got) != 2 {
		t.Errorf("negative limit must not truncate, got %d results", len(got))
	}
	if got := bestChunkPerMemory(in, 99); len(got) != 2 {
		t.Errorf("limit above result count must not pad or drop, got %d results", len(got))
	}
}

func TestBestChunkPerMemory_NegativeScores(t *testing.T) {
	// Cosine similarity is defined on [-1, 1]. A memory whose every chunk scores
	// negative must still be reduced (not dropped), and ordering must hold.
	a, b := chunkMemID(1), chunkMemID(2)
	in := []chunkScore{
		{a, 0, -0.90}, {a, 1, -0.10},
		{b, 0, -0.50},
	}

	got := bestChunkPerMemory(in, 10)

	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d", len(got))
	}
	if got[0].memoryID != a || got[0].score != -0.10 {
		t.Errorf("first = %v score %v, want memory a with its max -0.10", got[0].memoryID, got[0].score)
	}
}
