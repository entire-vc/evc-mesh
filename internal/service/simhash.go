package service

import "github.com/entire-vc/evc-mesh/internal/simhash"

// ComputeSimhash returns the 64-bit content fingerprint stored in
// memories.content_simhash. See internal/simhash for the algorithm.
func ComputeSimhash(text string) int64 { return simhash.Compute(text) }

// HammingDistance returns the number of bit positions that differ between a and b.
// Range: 0 (identical) to 64 (all bits differ).
func HammingDistance(a, b int64) int { return simhash.HammingDistance(a, b) }
