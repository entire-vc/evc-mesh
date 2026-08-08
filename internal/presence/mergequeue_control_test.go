package presence

import "testing"

// TEMPORARY — merge-queue negative control (task f421ad57). Delete after the run.
//
// A merge queue is supposed to catch what `strict: true` cannot: two pull
// requests that are each green on their own branch and broken once combined.
// This helper is deliberately declared under the SAME NAME on two branches, so
// either branch compiles alone and the pair does not. That is the exact shape of
// the duplicate-helper collision that broke main on 2026-08-08 (c2d24e5).
func mergeQueueNegativeControl() string { return "arm-a" }

func TestMergeQueueNegativeControlA(t *testing.T) {
	if mergeQueueNegativeControl() == "" {
		t.Fatal("control helper returned empty")
	}
}
