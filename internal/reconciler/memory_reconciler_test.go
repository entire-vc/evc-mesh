package reconciler

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func ptrInt64(v int64) *int64 { return &v }

func TestHammingDistance(t *testing.T) {
	tests := []struct {
		name string
		a, b int64
		want int
	}{
		{"identical", 0, 0, 0},
		{"identical nonzero", 0x0F0F0F0F, 0x0F0F0F0F, 0},
		{"one bit", 0b0000, 0b0001, 1},
		{"two bits", 0b0000, 0b0011, 2},
		{"all bits low nibble", 0b0000, 0b1111, 4},
		{"negative xor high bit", -1, 0, 64},
		{"mixed", 0b1010, 0b0101, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hammingDistance(tc.a, tc.b); got != tc.want {
				t.Fatalf("hammingDistance(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestSameKeyPrefix(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		n    int
		want bool
	}{
		{"same two colon segments", "episode:garfield:2026-07-04", "episode:garfield:2026-07-05", 2, true},
		{"differ at second segment", "episode:garfield:x", "episode:linus:y", 2, false},
		{"differ at first segment", "fact:garfield:x", "episode:garfield:y", 2, false},
		{"slash separator normalized", "episode/garfield/a", "episode:garfield:b", 2, true},
		{"mixed separators", "episode/garfield:a", "episode:garfield/b", 2, true},
		{"too few segments a", "episode", "episode:garfield:z", 2, false},
		{"too few segments b", "episode:garfield:z", "episode", 2, false},
		{"n=1 same", "episode:a", "episode:b", 1, true},
		{"n=1 differ", "fact:a", "episode:b", 1, false},
		{"empty keys", "", "", 2, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameKeyPrefix(tc.a, tc.b, tc.n); got != tc.want {
				t.Fatalf("sameKeyPrefix(%q, %q, %d) = %v, want %v", tc.a, tc.b, tc.n, got, tc.want)
			}
		})
	}
}

func TestTagOverlapRatio(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want float64
	}{
		{"empty a", nil, []string{"x"}, 0},
		{"empty b", []string{"x"}, nil, 0},
		{"both empty", nil, nil, 0},
		{"full overlap same size", []string{"x", "y"}, []string{"x", "y"}, 1.0},
		{"no overlap", []string{"x", "y"}, []string{"a", "b"}, 0.0},
		{"half overlap", []string{"x", "y"}, []string{"x", "z"}, 0.5},
		{"smaller-set denominator", []string{"x"}, []string{"x", "y", "z"}, 1.0},
		{"partial with unequal sizes", []string{"x", "y", "z"}, []string{"x", "y"}, 1.0},
		{"one of three matches larger", []string{"x", "y", "z"}, []string{"x", "q", "r", "s"}, 1.0 / 3.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tagOverlapRatio(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("tagOverlapRatio(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestComputeConfidence(t *testing.T) {
	base := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	// Helper to build a memory with the fields the confidence engine reads.
	mem := func(key string, tags []string, simhash *int64, created time.Time) domain.Memory {
		return domain.Memory{
			ID:             uuid.New(),
			Key:            key,
			Tags:           pq.StringArray(tags),
			ContentSimhash: simhash,
			CreatedAt:      created,
		}
	}

	tests := []struct {
		name        string
		older       domain.Memory
		newer       domain.Memory
		want        float64
		wantAtLeast bool // if true, assert got >= want (for capped/combined cases)
	}{
		{
			name:  "no signals",
			older: mem("a:b:c", []string{"p"}, nil, base),
			newer: mem("x:y:z", []string{"q"}, nil, base),
			want:  0.0,
		},
		{
			name:  "only key prefix",
			older: mem("episode:garfield:1", []string{"p"}, nil, base),
			newer: mem("episode:garfield:2", []string{"q"}, nil, base),
			want:  0.3,
		},
		{
			name:  "only tag overlap",
			older: mem("a:b:1", []string{"x", "y"}, nil, base),
			newer: mem("c:d:2", []string{"x", "y"}, nil, base),
			want:  0.2,
		},
		{
			name:  "only simhash close",
			older: mem("a:b:1", []string{"p"}, ptrInt64(0b0000), base),
			newer: mem("c:d:2", []string{"q"}, ptrInt64(0b0011), base), // 2 bits apart, <= 10
			want:  0.3,
		},
		{
			name:  "simhash too far no credit",
			older: mem("a:b:1", []string{"p"}, ptrInt64(0), base),
			newer: mem("c:d:2", []string{"q"}, ptrInt64(0x7FF), base), // 11 bits set, > 10
			want:  0.0,
		},
		{
			name:  "nil simhash no credit",
			older: mem("a:b:1", []string{"p"}, nil, base),
			newer: mem("c:d:2", []string{"q"}, ptrInt64(0), base),
			want:  0.0,
		},
		{
			name:  "only recency (>1h newer)",
			older: mem("a:b:1", []string{"p"}, nil, base),
			newer: mem("c:d:2", []string{"q"}, nil, base.Add(2*time.Hour)),
			want:  0.2,
		},
		{
			name:  "recency exactly 1h no credit",
			older: mem("a:b:1", []string{"p"}, nil, base),
			newer: mem("c:d:2", []string{"q"}, nil, base.Add(time.Hour)),
			want:  0.0,
		},
		{
			name:  "same timestamp no recency credit",
			older: mem("a:b:1", []string{"p"}, nil, base),
			newer: mem("c:d:2", []string{"q"}, nil, base),
			want:  0.0,
		},
		{
			name:  "key + tags",
			older: mem("episode:garfield:1", []string{"x", "y"}, nil, base),
			newer: mem("episode:garfield:2", []string{"x", "y"}, nil, base),
			want:  0.5,
		},
		{
			name:  "all four signals capped at 1.0",
			older: mem("episode:garfield:1", []string{"x", "y"}, ptrInt64(0b0000), base),
			newer: mem("episode:garfield:2", []string{"x", "y"}, ptrInt64(0b0001), base.Add(2*time.Hour)),
			want:  1.0, // sum of all four components equals one point zero
		},
		{
			name:  "key + simhash + recency = 0.8",
			older: mem("episode:garfield:1", []string{"p"}, ptrInt64(0), base),
			newer: mem("episode:garfield:2", []string{"q"}, ptrInt64(0b1), base.Add(2*time.Hour)),
			want:  0.8, // key prefix plus simhash plus recency components
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeConfidence(tc.older, tc.newer)
			if tc.wantAtLeast {
				if got < tc.want {
					t.Fatalf("computeConfidence = %v, want >= %v", got, tc.want)
				}
				return
			}
			// Float comparison with small epsilon for additive cases.
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("computeConfidence = %v, want %v", got, tc.want)
			}
		})
	}
}
