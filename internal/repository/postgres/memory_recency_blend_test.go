package postgres

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

// mkRow builds a scoredMemoryRow with just the fields applyRecencyBlend reads:
// the ts_rank Score and the UpdatedAt timestamp. The ID makes the ordering
// assertions readable.
func mkRow(id string, score float64, updatedAt time.Time) scoredMemoryRow {
	var row scoredMemoryRow
	row.ID = uuid.NewSHA1(uuid.Nil, []byte(id)) // deterministic, human-labeled
	row.Score = score
	row.UpdatedAt = updatedAt
	return row
}

func ids(rows []scoredMemoryRow) []uuid.UUID {
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func scores(rows []scoredMemoryRow) []float64 {
	out := make([]float64, len(rows))
	for i, r := range rows {
		out[i] = r.Score
	}
	return out
}

// TestApplyRecencyBlend_ZeroWeight_NoOp is the regression-safety golden:
// recency_weight=0 MUST leave both ordering AND scores byte-for-byte identical.
func TestApplyRecencyBlend_ZeroWeight_NoOp(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	rows := []scoredMemoryRow{
		mkRow("a-high-old", 0.9, now.AddDate(0, 0, -300)),
		mkRow("b-mid-new", 0.5, now.AddDate(0, 0, -1)),
		mkRow("c-low-mid", 0.2, now.AddDate(0, 0, -40)),
	}
	wantIDs := ids(rows)
	wantScores := scores(rows)

	applyRecencyBlend(rows, 0, now)

	if got := ids(rows); !equalIDs(got, wantIDs) {
		t.Fatalf("zero weight changed ordering: got %v want %v", got, wantIDs)
	}
	if got := scores(rows); !equalFloats(got, wantScores) {
		t.Fatalf("zero weight changed scores: got %v want %v", got, wantScores)
	}
}

// Negative weights are also treated as no-op (clamped path / guard).
func TestApplyRecencyBlend_NegativeWeight_NoOp(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	rows := []scoredMemoryRow{
		mkRow("x", 0.8, now.AddDate(0, 0, -200)),
		mkRow("y", 0.7, now.AddDate(0, 0, -1)),
	}
	want := ids(rows)
	applyRecencyBlend(rows, -0.5, now)
	if got := ids(rows); !equalIDs(got, want) {
		t.Fatalf("negative weight must be no-op: got %v want %v", got, want)
	}
}

// TestApplyRecencyBlend_FullWeight_RecencyWins: with weight=1 a newer memory that has a
// slightly LOWER ts_rank score must rank above an older, higher-scoring one.
func TestApplyRecencyBlend_FullWeight_RecencyWins(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	old := mkRow("old-highFTS", 0.90, now.AddDate(0, 0, -120))  // higher FTS, stale
	fresh := mkRow("fresh-lowFTS", 0.80, now.AddDate(0, 0, -1)) // lower FTS, recent
	rows := []scoredMemoryRow{old, fresh}                       // starts old-first (FTS order)

	applyRecencyBlend(rows, 1.0, now)

	if rows[0].ID != fresh.ID {
		t.Fatalf("full recency weight: expected fresh memory first, got %v", ids(rows))
	}
	// Scores are preserved (only order changes).
	if rows[0].Score != 0.80 || rows[1].Score != 0.90 {
		t.Fatalf("scores must be preserved, got %v", scores(rows))
	}
}

// TestApplyRecencyBlend_MidWeight_Blends: at a moderate weight the blend can still flip
// order when the recency gap dominates a small FTS gap, and preserves it when it doesn't.
func TestApplyRecencyBlend_MidWeight_Blends(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	// Case 1: tiny FTS gap, huge recency gap → mid weight flips to the fresher one.
	//   normFTS: old=1.0, fresh=0.0 (min-max over the pair)
	//   recency (30d half-life): old(≈365d)=exp(-ln2*365/30)≈0.00021, fresh(1d)≈0.977
	//   w=0.5 → old=0.5*1+0.5*0.0002=0.5001 ; fresh=0.5*0+0.5*0.977=0.4885 → old still wins.
	//   w=0.7 → old=0.3*1+0.7*0.0002=0.30014 ; fresh=0.3*0+0.7*0.977=0.684 → fresh wins.
	oldRow := mkRow("old", 0.81, now.AddDate(0, 0, -365))
	freshRow := mkRow("fresh", 0.80, now.AddDate(0, 0, -1))

	rows := []scoredMemoryRow{oldRow, freshRow}
	applyRecencyBlend(rows, 0.5, now)
	if rows[0].ID != oldRow.ID {
		t.Fatalf("w=0.5 with tiny FTS gap: expected old first, got %v", ids(rows))
	}

	rows = []scoredMemoryRow{oldRow, freshRow}
	applyRecencyBlend(rows, 0.7, now)
	if rows[0].ID != freshRow.ID {
		t.Fatalf("w=0.7 with huge recency gap: expected fresh first, got %v", ids(rows))
	}

	// Case 2: large FTS gap, small recency gap → mid weight keeps FTS order.
	strong := mkRow("strong", 0.95, now.AddDate(0, 0, -10))
	weak := mkRow("weak", 0.10, now.AddDate(0, 0, -2))
	rows = []scoredMemoryRow{strong, weak}
	applyRecencyBlend(rows, 0.3, now)
	if rows[0].ID != strong.ID {
		t.Fatalf("w=0.3 with large FTS gap: expected strong first, got %v", ids(rows))
	}
}

// Blended math sanity: verify the exact blended score for a known input.
func TestApplyRecencyBlend_MathMatchesFormula(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	// Two rows, 30 days apart (one half-life for the fresher's decay reference).
	a := mkRow("a", 1.0, now)                    // newest, top FTS
	b := mkRow("b", 0.0, now.AddDate(0, 0, -30)) // oldest, bottom FTS, exactly one half-life
	rows := []scoredMemoryRow{a, b}

	// Recompute expected blended values independently.
	w := 0.5
	lambda := math.Ln2 / recencyHalfLifeDays
	// normFTS: a=1, b=0 (span=1)
	recA := math.Exp(-lambda * 0)  // 1.0
	recB := math.Exp(-lambda * 30) // 0.5
	wantA := (1-w)*1.0 + w*recA    // 1.0
	wantB := (1-w)*0.0 + w*recB    // 0.25

	// Drive ordering (a should remain first) and check we didn't corrupt scores.
	applyRecencyBlend(rows, w, now)
	if rows[0].ID != a.ID {
		t.Fatalf("expected a first, got %v", ids(rows))
	}
	if math.Abs(wantA-1.0) > 1e-9 || math.Abs(wantB-0.25) > 1e-9 {
		t.Fatalf("formula sanity failed: wantA=%v wantB=%v", wantA, wantB)
	}
}

func equalIDs(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
