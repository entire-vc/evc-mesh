package domain

import "testing"

func TestDefaultExtensionAllowed(t *testing.T) {
	for trust, want := range map[string]bool{
		"": true, string(ExtensionTrustBuiltin): true, "official": true, "verified": true,
		"community": false, "custom": false, "unknown": false,
	} {
		if got := DefaultExtensionAllowed(trust); got != want {
			t.Errorf("DefaultExtensionAllowed(%q) = %v, want %v", trust, got, want)
		}
	}
}
