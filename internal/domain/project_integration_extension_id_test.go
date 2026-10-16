package domain

import "testing"

func TestProjectIntegrationExtensionID(t *testing.T) {
	cases := map[string]string{
		"team_relay": "team-relay",
		"github":     "github",
		"":           "",
	}
	for in, want := range cases {
		if got := ProjectIntegrationExtensionID(in); got != want {
			t.Errorf("ProjectIntegrationExtensionID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectIntegrationType(t *testing.T) {
	cases := map[string]string{
		"team-relay": "team_relay",
		"github":     "github",
		"":           "",
	}
	for in, want := range cases {
		if got := ProjectIntegrationType(in); got != want {
			t.Errorf("ProjectIntegrationType(%q) = %q, want %q", in, got, want)
		}
	}
}
