package main

import (
	"testing"

	"github.com/entire-vc/evc-mesh/internal/auth"
)

// A generated admin password is useless if the auth service refuses it, and the
// failure would only surface on a self-hoster's very first boot — the worst
// possible place to find out. Pin the two together.
func TestGenerateAdminPasswordSatisfiesValidator(t *testing.T) {
	seen := make(map[string]bool)

	for i := 0; i < 200; i++ {
		pw, err := generateAdminPassword()
		if err != nil {
			t.Fatalf("generateAdminPassword returned an error: %v", err)
		}
		if err := auth.ValidatePassword(pw); err != nil {
			t.Fatalf("generated password %q rejected by auth.ValidatePassword: %v", pw, err)
		}
		if seen[pw] {
			t.Fatalf("generated a duplicate password %q within %d draws — not random", pw, i+1)
		}
		seen[pw] = true
	}
}

func TestGenerateAdminPasswordLength(t *testing.T) {
	pw, err := generateAdminPassword()
	if err != nil {
		t.Fatalf("generateAdminPassword returned an error: %v", err)
	}
	if len(pw) != 24 {
		t.Errorf("expected a 24-character password, got %d (%q)", len(pw), pw)
	}
}
