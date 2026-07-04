package service

import (
	"hash/fnv"
	"strings"
	"unicode"
)

// ComputeSimhash returns a 64-bit simhash of the given text using 3-character shingles
// and FNV-64a feature hashes. The result is stable across calls for identical input.
//
// Algorithm:
//  1. Normalize: lowercase, collapse whitespace, strip non-letter/digit runes.
//  2. Extract all consecutive 3-character shingles from the normalized text.
//  3. For each shingle, compute FNV-64a hash.
//  4. Accumulate per-bit counts: +1 for each set bit, -1 for each clear bit.
//  5. Build result: bit i = 1 if count[i] > 0, else 0.
//
// Texts with ≤2 characters return 0 (no 3-gram shingles possible).
func ComputeSimhash(text string) int64 {
	normalized := normalizeForSimhash(text)
	if len([]rune(normalized)) < 3 {
		return 0
	}

	// counts[i] accumulates the vote for bit i of the final hash.
	var counts [64]int

	runes := []rune(normalized)
	for i := 0; i <= len(runes)-3; i++ {
		shingle := string(runes[i : i+3])
		h := shingleHash(shingle)
		for bit := 0; bit < 64; bit++ {
			if (h>>uint(bit))&1 == 1 {
				counts[bit]++
			} else {
				counts[bit]--
			}
		}
	}

	var result int64
	for bit := 0; bit < 64; bit++ {
		if counts[bit] > 0 {
			result |= 1 << uint(bit)
		}
	}
	return result
}

// HammingDistance returns the number of bit positions that differ between a and b.
// Range: 0 (identical) to 64 (all bits differ).
func HammingDistance(a, b int64) int {
	xor := uint64(a ^ b)
	return popcount(xor)
}

// popcount counts set bits in a uint64 (Brian Kernighan's method).
func popcount(x uint64) int {
	n := 0
	for x != 0 {
		x &= x - 1
		n++
	}
	return n
}

// shingleHash computes FNV-64a of a shingle string.
func shingleHash(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64())
}

// normalizeForSimhash lowercases text, strips non-alphanumeric runes (keeping spaces),
// and collapses consecutive spaces into one.
func normalizeForSimhash(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevSpace = false
		} else if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		}
		// strip everything else
	}
	return strings.TrimSpace(b.String())
}
