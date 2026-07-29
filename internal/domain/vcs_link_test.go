package domain

import "testing"

func TestParseVCSLinkType(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  VCSLinkType
		ok    bool
	}{
		{"canonical pr", "pr", VCSLinkTypePR, true},
		{"canonical commit", "commit", VCSLinkTypeCommit, true},
		{"canonical branch", "branch", VCSLinkTypeBranch, true},
		{"pull_request alias", "pull_request", VCSLinkTypePR, true},
		{"uppercase canonical", "PR", VCSLinkTypePR, true},
		{"mixed-case alias", "Pull_Request", VCSLinkTypePR, true},
		{"surrounding whitespace", "  pr\n", VCSLinkTypePR, true},
		{"empty", "", "", false},
		{"unknown word", "merge_request", "", false},
		{"no separator", "pullrequest", "", false},
		{"partial", "pu", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseVCSLinkType(tt.input)
			if ok != tt.ok {
				t.Fatalf("ParseVCSLinkType(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("ParseVCSLinkType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Every accepted spelling must resolve to one of the three canonical values —
// an alias table entry pointing at a fourth spelling would silently split the
// stored vocabulary in two.
func TestVCSLinkTypeAliasesResolveToCanonicalValues(t *testing.T) {
	canonical := map[VCSLinkType]bool{
		VCSLinkTypePR:     true,
		VCSLinkTypeCommit: true,
		VCSLinkTypeBranch: true,
	}
	for spelling, resolved := range vcsLinkTypeAliases {
		if !canonical[resolved] {
			t.Errorf("alias %q resolves to non-canonical value %q", spelling, resolved)
		}
	}
}

// The documented list and the accepted set must not drift apart: the list is
// what a rejected caller is told to use.
func TestVCSLinkTypeNamesMatchAcceptedSet(t *testing.T) {
	if len(VCSLinkTypeNames) != len(vcsLinkTypeAliases) {
		t.Fatalf("VCSLinkTypeNames has %d entries, alias table has %d",
			len(VCSLinkTypeNames), len(vcsLinkTypeAliases))
	}
	for _, name := range VCSLinkTypeNames {
		if _, ok := ParseVCSLinkType(name); !ok {
			t.Errorf("documented spelling %q is not accepted", name)
		}
	}
}
