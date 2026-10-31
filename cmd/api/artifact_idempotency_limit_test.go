package main

import (
	"encoding/base64"
	"math"
	"testing"
)

func TestArtifactIdempotencyBodyLimit(t *testing.T) {
	const envelope = 2 << 20
	for _, n := range []int64{0, 1, 2, 3, 25 << 20, 1 << 20} {
		want := int64(base64.StdEncoding.EncodedLen(int(n))) + envelope
		if got := artifactIdempotencyBodyLimit(n); got != want {
			t.Errorf("n=%d: got %d, want %d", n, got, want)
		}
	}

	ceiling := artifactIdempotencyBodyLimit(maxArtifactBodyBytes)
	for _, n := range []int64{math.MaxInt64, math.MaxInt64 - 1, maxArtifactBodyBytes + 1} {
		if got := artifactIdempotencyBodyLimit(n); got != ceiling {
			t.Errorf("n=%d: got %d, want clamp %d", n, got, ceiling)
		}
	}
	for _, n := range []int64{-1, math.MinInt64} {
		if got := artifactIdempotencyBodyLimit(n); got != envelope {
			t.Errorf("n=%d: got %d, want %d", n, got, envelope)
		}
	}
}
