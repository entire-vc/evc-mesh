package domain

import "testing"

func TestIntegrationConfig_IsProjectBound(t *testing.T) {
	for scope, want := range map[string]bool{"": false, ExtensionProjectScopeWorkspace: false, ExtensionProjectScopeBound: true} {
		if got := (&IntegrationConfig{ProjectScope: scope}).IsProjectBound(); got != want {
			t.Errorf("scope %q: IsProjectBound() = %v, want %v", scope, got, want)
		}
	}
}
