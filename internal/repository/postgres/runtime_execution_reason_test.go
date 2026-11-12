package postgres

import "testing"

func TestExecutionReason(t *testing.T) {
	cases := []struct {
		name                         string
		hasBinding, managed, enabled bool
		want                         string // "" = nil
	}{
		{"no binding", false, false, false, "unmanaged"},
		{"binding, integration off", true, false, false, "integration_disabled"},
		{"managed but route disabled", true, true, false, "disabled"},
		{"managed and enabled", true, true, true, ""},
	}
	for _, c := range cases {
		got := executionReason(c.hasBinding, c.managed, c.enabled)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: want nil, got %q", c.name, *got)
		case c.want != "" && (got == nil || *got != c.want):
			t.Errorf("%s: want %q, got %v", c.name, c.want, got)
		}
	}
}
