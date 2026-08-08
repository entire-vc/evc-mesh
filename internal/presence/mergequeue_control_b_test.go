package presence

import "testing"

// TEMPORARY — merge-queue negative control (task f421ad57). Delete after the run.
//
// Same name as arm A's helper, in a different file. Each branch builds; the
// combined tree does not. Nothing on either PR's own checks can see this — which
// is the whole point of the queue testing the merged result.
func mergeQueueNegativeControl() string { return "arm-b" }

func TestMergeQueueNegativeControlB(t *testing.T) {
	if mergeQueueNegativeControl() == "" {
		t.Fatal("control helper returned empty")
	}
}
