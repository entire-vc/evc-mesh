package postgres

import (
	"math"
	"testing"

	"github.com/google/uuid"
)

// TestDecayedRelevanceFormula verifies the Go-side implementation of the SQL
// expression used in the decayed_relevance:desc ORDER BY clause.
// Pure-math verification — no DB connection required.
func TestDecayedRelevanceFormula(t *testing.T) {
	const ln2 = 0.693147

	cases := []struct {
		name         string
		relevance    float64
		freshness    float64
		ageDays      float64
		halfLifeDays float64
		wantScore    float64
	}{
		{
			name:         "brand_new_active",
			relevance:    1.0,
			freshness:    1.0,
			ageDays:      0,
			halfLifeDays: 30,
			wantScore:    1.0,
		},
		{
			name:         "exactly_one_half_life",
			relevance:    1.0,
			freshness:    1.0,
			ageDays:      30,
			halfLifeDays: 30,
			wantScore:    0.5,
		},
		{
			name:         "two_half_lives",
			relevance:    1.0,
			freshness:    1.0,
			ageDays:      60,
			halfLifeDays: 30,
			wantScore:    0.25,
		},
		{
			name:         "freshness_multiplied",
			relevance:    1.0,
			freshness:    0.25,
			ageDays:      0,
			halfLifeDays: 30,
			wantScore:    0.25,
		},
		{
			name:         "combined_decay_and_freshness",
			relevance:    1.0,
			freshness:    0.5,
			ageDays:      30,
			halfLifeDays: 30,
			wantScore:    0.25,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.relevance * tc.freshness * math.Exp(-tc.ageDays*ln2/tc.halfLifeDays)
			if math.Abs(got-tc.wantScore) > 1e-5 {
				t.Errorf("score mismatch: got %.6f want %.6f (relevance=%.2f freshness=%.2f ageDays=%.0f halfLife=%.0f)",
					got, tc.wantScore, tc.relevance, tc.freshness, tc.ageDays, tc.halfLifeDays)
			}
		})
	}
}

func TestTokenizeForORQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "natural language with filler words",
			input: "mesh deploy unblocked by fixing migrate-gate DSN",
			// "by" dropped (<3), "fixing"+"migrate"+"gate"+"dsn"+"mesh"+"deploy"+"unblocked" kept
			want: "mesh | deploy | unblocked | fixing | migrate | gate | dsn",
		},
		{
			name:  "short stopwords filtered",
			input: "fix for the bug in it",
			// "for","the","in","it" all <3 or exactly 3 — "the" is 3 chars, "for" is 3 chars, kept; "in" and "it" are 2 chars, dropped
			want: "fix | for | the | bug",
		},
		{
			name:  "deduplication",
			input: "mesh mesh api mesh endpoint",
			want:  "mesh | api | endpoint",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "only short tokens",
			input: "by an or a",
			want:  "",
		},
		{
			name:  "mixed case normalized",
			input: "Dispatcher Fix Pavel HUMAN-VERIFY",
			want:  "dispatcher | fix | pavel | human | verify",
		},
		{
			name:  "typical episodic golden query",
			input: "argus confidence based person dedup merge shipped",
			want:  "argus | confidence | based | person | dedup | merge | shipped",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenizeForORQuery(tc.input)
			if got != tc.want {
				t.Errorf("tokenizeForORQuery(%q)\n  got  %q\n  want %q", tc.input, got, tc.want)
			}
		})
	}
}

// scoredRow is a test helper building a scoredMemoryRow with just the fields
// mergeORScoredRows cares about (identity + score).
func scoredRow(id uuid.UUID, score float64) scoredMemoryRow {
	return scoredMemoryRow{
		memoryRow: memoryRow{ID: id},
		Score:     score,
	}
}

// TestMergeORScoredRows covers the AND+OR merge shared by the 'simple' (ftsORFallback)
// and 'english' (ftsRankedORFallback) relaxation arms. Pure — no DB connection required.
func TestMergeORScoredRows(t *testing.T) {
	idA := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	idB := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	idC := uuid.MustParse("00000000-0000-0000-0000-0000000000c3")

	t.Run("or_only_rows_are_discounted", func(t *testing.T) {
		// AND hit scores 0.5; OR-only hit has a *higher* raw score (0.6) but after the
		// 0.8x discount (0.48) it must still rank below the exact AND match.
		got := mergeORScoredRows(
			[]scoredMemoryRow{scoredRow(idA, 0.5)},
			[]scoredMemoryRow{scoredRow(idB, 0.6)},
			10,
		)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].ID != idA {
			t.Errorf("AND hit should rank first, got %v", got[0].ID)
		}
		if math.Abs(got[1].Score-0.48) > 1e-9 {
			t.Errorf("OR-only score = %v, want 0.48 (0.6 * %v)", got[1].Score, orScoreMultiplier)
		}
	})

	t.Run("dedupe_keeps_undiscounted_and_score", func(t *testing.T) {
		// A row found by BOTH arms must appear once, keeping its raw AND score.
		got := mergeORScoredRows(
			[]scoredMemoryRow{scoredRow(idA, 0.5)},
			[]scoredMemoryRow{scoredRow(idA, 0.5), scoredRow(idB, 0.1)},
			10,
		)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (idA must not appear twice)", len(got))
		}
		if got[0].ID != idA || math.Abs(got[0].Score-0.5) > 1e-9 {
			t.Errorf("idA = %v score %v, want raw 0.5 (no discount)", got[0].ID, got[0].Score)
		}
	})

	t.Run("respects_limit", func(t *testing.T) {
		got := mergeORScoredRows(
			[]scoredMemoryRow{scoredRow(idA, 0.5)},
			[]scoredMemoryRow{scoredRow(idB, 0.4), scoredRow(idC, 0.3)},
			2,
		)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (capped at limit)", len(got))
		}
		if got[0].ID != idA || got[1].ID != idB {
			t.Errorf("order = [%v %v], want [idA idB] (lowest-scoring idC dropped)", got[0].ID, got[1].ID)
		}
	})

	t.Run("no_and_hits_returns_or_rows", func(t *testing.T) {
		// The dense-arm-down / strict-AND-zero case: relaxation is the only thing
		// standing between the caller and an empty recall.
		got := mergeORScoredRows(
			nil,
			[]scoredMemoryRow{scoredRow(idB, 0.4), scoredRow(idC, 0.9)},
			10,
		)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].ID != idC {
			t.Errorf("expected highest OR score first, got %v", got[0].ID)
		}
	})
}
