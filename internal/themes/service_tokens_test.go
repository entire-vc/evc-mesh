package themes

import "testing"

func TestNonNilTokens(t *testing.T) {
	if got := nonNilTokens(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil tokens must become an empty map (JSON {}), got %#v", got)
	}
	in := map[string]map[string]string{"light": {"primary": "1 1% 1%"}}
	if got := nonNilTokens(in); got["light"]["primary"] != "1 1% 1%" {
		t.Fatalf("tokens must pass through, got %#v", got)
	}
}
