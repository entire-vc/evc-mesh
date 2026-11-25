package simhash

import "testing"

// The stored content_simhash values are only comparable within one Algorithm.
// These golden values fail on any change to Compute's output: change Algorithm
// with them, so the API recomputes the stored hashes on its next start.
func TestCompute_GoldenValuesPinAlgorithm(t *testing.T) {
	if Algorithm != "trigram-tf-fnv64a-fmix64" {
		t.Fatalf("Algorithm = %q: update the golden values below together with it", Algorithm)
	}
	for _, tc := range []struct {
		text string
		want int64
	}{
		{"", 0},
		{"ok", 0}, // shorter than one trigram
		{"The nightly export job keeps fourteen archives.", 6811942539483481393},
		{"Деплой сервиса отчётов перенесён на четверг.", 2435121571972594300},
	} {
		if got := Compute(tc.text); got != tc.want {
			t.Errorf("Compute(%q) = %d, want %d (output changed: bump Algorithm)", tc.text, got, tc.want)
		}
	}
}

func TestCompute_NormalizesCaseAndWhitespace(t *testing.T) {
	a := Compute("The nightly export job keeps fourteen archives.")
	b := Compute("  the NIGHTLY export\njob keeps\tfourteen archives. ")
	if a != b {
		t.Fatalf("case/whitespace variants differ: %d vs %d", a, b)
	}
	if HammingDistance(a, a) != 0 || HammingDistance(0, -1) != 64 {
		t.Fatal("HammingDistance")
	}
}
