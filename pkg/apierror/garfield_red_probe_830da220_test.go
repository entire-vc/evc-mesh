package apierror

import "testing"

// Intentional red test — merge-gate live-verify probe for task #830da220.
// Confirms a code PR with a failing required check still gets 405 on merge
// attempt even with enforce_admins=true. Delete before merge (this PR is
// never meant to land).
func TestGarfieldRedProbe830da220(t *testing.T) {
	t.Fatal("intentional red: CI merge-gate probe for #830da220")
}
