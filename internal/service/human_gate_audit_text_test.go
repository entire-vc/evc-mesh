package service

import "testing"

// The sample is built by index from triageExitNegators; reordering that
// dictionary must not silently change the hint the handler returns.
func TestWithdrawalNegatorExamples_Wording(t *testing.T) {
	want := `"не нужен", "не нужно", "не требуется", "снят", "resolved"`
	if got := WithdrawalNegatorExamples(); got != want {
		t.Fatalf("WithdrawalNegatorExamples() = %s, want %s", got, want)
	}
}
