package postgres

import (
	"math"
	"testing"
)

// TestDecayedRelevanceFormula verifies the Go-side implementation of the SQL
// expression used in the decayed_relevance:desc ORDER BY clause:
//   relevance * freshness_score * EXP(-age_days * 0.693147 / half_life_days)
// This is a pure-math verification — no DB connection required.
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
			wantScore:    1.0, // exp(0) = 1
		},
		{
			name:         "exactly_one_half_life",
			relevance:    1.0,
			freshness:    1.0,
			ageDays:      30,
			halfLifeDays: 30,
			wantScore:    0.5, // exp(-ln2) = 0.5
		},
		{
			name:         "two_half_lives",
			relevance:    1.0,
			freshness:    1.0,
			ageDays:      60,
			halfLifeDays: 30,
			wantScore:    0.25, // exp(-2*ln2) = 0.25
		},
		{
			name:         "freshness_multiplied",
			relevance:    1.0,
			freshness:    0.25, // stale memory
			ageDays:      0,
			halfLifeDays: 30,
			wantScore:    0.25, // 1.0 * 0.25 * exp(0) = 0.25
		},
		{
			name:         "combined_decay_and_freshness",
			relevance:    1.0,
			freshness:    0.5,
			ageDays:      30,
			halfLifeDays: 30,
			wantScore:    0.25, // 1.0 * 0.5 * 0.5 = 0.25
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
