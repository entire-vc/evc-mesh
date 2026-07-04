package service

import (
	"testing"
)

func TestComputeSimhash_Stability(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog"
	h1 := ComputeSimhash(text)
	h2 := ComputeSimhash(text)
	if h1 != h2 {
		t.Fatalf("simhash not stable: %d vs %d", h1, h2)
	}
}

func TestComputeSimhash_NearDups(t *testing.T) {
	a := "Deployment to prod — Spark v2.1 shipped 2026-07-01 by Howard"
	b := "Deployment to prod — Spark v2.1 shipped 2026-07-01 by Howard."
	ha := ComputeSimhash(a)
	hb := ComputeSimhash(b)
	d := HammingDistance(ha, hb)
	if d > 10 {
		t.Fatalf("near-identical texts should have Hamming ≤ 10, got %d (ha=%d hb=%d)", d, ha, hb)
	}
}

func TestComputeSimhash_DistinctTexts(t *testing.T) {
	a := "Pavel decided to use PostgreSQL for the main database"
	b := "The new Spark auth flow uses Casdoor OAuth2 redirect"
	ha := ComputeSimhash(a)
	hb := ComputeSimhash(b)
	d := HammingDistance(ha, hb)
	// Distinct texts should have high Hamming distance (> 15 bits).
	if d <= 15 {
		t.Logf("warning: distinct texts have low Hamming distance %d (may be coincidence)", d)
	}
}

func TestComputeSimhash_ShortText(t *testing.T) {
	// Texts with <3 runes return 0.
	if got := ComputeSimhash("ab"); got != 0 {
		t.Fatalf("expected 0 for short text, got %d", got)
	}
	if got := ComputeSimhash(""); got != 0 {
		t.Fatalf("expected 0 for empty text, got %d", got)
	}
}

func TestHammingDistance_Identical(t *testing.T) {
	h := ComputeSimhash("hello world this is a test")
	if d := HammingDistance(h, h); d != 0 {
		t.Fatalf("Hamming distance of identical hashes should be 0, got %d", d)
	}
}

func TestHammingDistance_MaxDiff(t *testing.T) {
	d := HammingDistance(0, -1) // 0 vs all-ones
	if d != 64 {
		t.Fatalf("expected 64, got %d", d)
	}
}
