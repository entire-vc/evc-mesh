package domain

import "testing"

// #655c6d12: the recall service compared OrderBy against the bare
// "decayed_relevance" while every producer emits "decayed_relevance:desc", so
// the decay branch was unreachable. CanonicalOrderBy is where that spelling
// question is now settled once; these cases pin both directions of it.
func TestCanonicalOrderBy(t *testing.T) {
	cases := map[string]struct {
		canonical string
		isDecayed bool
	}{
		// Both spellings of the decay ordering must land on one value — that
		// they did not is the whole defect.
		"decayed_relevance":      {OrderByDecayedRelevanceDesc, true},
		"decayed_relevance:desc": {OrderByDecayedRelevanceDesc, true},

		// Discriminating control: near neighbours must NOT be read as the decay
		// ordering. Without these the test passes on a function that returns
		// OrderByDecayedRelevanceDesc unconditionally.
		"relevance":      {OrderByRelevanceDesc, false},
		"relevance:desc": {OrderByRelevanceDesc, false},
		"created_at":     {OrderByCreatedAtDesc, false},
		"created_at:asc": {OrderByCreatedAtAsc, false},
		"":               {"", false},

		// A prefix match would swallow this one; the comparison is exact.
		"decayed_relevance_something_else": {"decayed_relevance_something_else", false},

		// Unknown values pass through untouched, so each caller keeps its own
		// default for them instead of having one invented in the normaliser.
		"nonsense": {"nonsense", false},
	}
	for in, want := range cases {
		if got := CanonicalOrderBy(in); got != want.canonical {
			t.Errorf("CanonicalOrderBy(%q) = %q, want %q", in, got, want.canonical)
		}
		if got := IsDecayedRelevanceOrder(in); got != want.isDecayed {
			t.Errorf("IsDecayedRelevanceOrder(%q) = %v, want %v", in, got, want.isDecayed)
		}
	}
}
