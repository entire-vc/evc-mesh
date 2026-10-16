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

func TestParseVCSLinkStatus(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  VCSLinkStatus
		ok    bool
	}{
		{"empty means unspecified", "", "", true},
		{"open", "open", VCSLinkStatusOpen, true},
		{"merged", "merged", VCSLinkStatusMerged, true},
		{"closed", "closed", VCSLinkStatusClosed, true},
		{"uppercase", "MERGED", VCSLinkStatusMerged, true},
		{"surrounding whitespace", "  merged\n", VCSLinkStatusMerged, true},
		{"unknown word", "pending", "", false},
		{"pr status confused with link_type", "pr", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseVCSLinkStatus(tt.input)
			if ok != tt.ok {
				t.Fatalf("ParseVCSLinkStatus(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("ParseVCSLinkStatus(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// The documented list and the accepted set must not drift apart, same
// reasoning as TestVCSLinkTypeNamesMatchAcceptedSet.
func TestVCSLinkStatusNamesMatchAcceptedSet(t *testing.T) {
	for _, name := range VCSLinkStatusNames {
		if _, ok := ParseVCSLinkStatus(name); !ok {
			t.Errorf("documented status %q is not accepted", name)
		}
	}
}

func TestNormalizeVCSURL(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
	}{
		{"identical", "https://github.com/entire-vc/evc-mesh/pull/40", "https://github.com/entire-vc/evc-mesh/pull/40"},
		{"trailing slash", "https://github.com/entire-vc/evc-mesh/pull/40", "https://github.com/entire-vc/evc-mesh/pull/40/"},
		{"http vs https", "http://gitlab.example.invalid/entire-vc/team-relay-ops/-/merge_requests/14", "https://gitlab.example.invalid/entire-vc/team-relay-ops/-/merge_requests/14"},
		{"host case", "https://GITLAB.example.invalid/entire-vc/team-relay-ops/-/merge_requests/14", "https://gitlab.example.invalid/entire-vc/team-relay-ops/-/merge_requests/14"},
		{"scheme case", "HTTPS://gitlab.example.invalid/entire-vc/team-relay-ops/-/merge_requests/14", "https://gitlab.example.invalid/entire-vc/team-relay-ops/-/merge_requests/14"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotA, gotB := NormalizeVCSURL(tt.a), NormalizeVCSURL(tt.b)
			if gotA != gotB {
				t.Fatalf("NormalizeVCSURL(%q)=%q != NormalizeVCSURL(%q)=%q, want equal", tt.a, gotA, tt.b, gotB)
			}
		})
	}
}

// #0fbed572 defect 2's negative control: a GitHub PR and a GitLab MR that
// happen to share the same number must NEVER normalize to the same string —
// folding that far is what silently collapsed two distinct objects into one.
func TestNormalizeVCSURL_DifferentProvidersSameNumberStayDistinct(t *testing.T) {
	github := NormalizeVCSURL("https://github.com/entire-vc/team-relay-ops/pull/14")
	gitlab := NormalizeVCSURL("https://gitlab.example.invalid/entire-vc/team-relay-ops/-/merge_requests/14")
	if github == gitlab {
		t.Fatalf("a GitHub PR and a GitLab MR normalized to the same string: %q", github)
	}
}

// Path casing is left alone deliberately — GitHub/GitLab URL paths are not
// case-insensitive in general, and folding them is not what this function
// exists to fix (only scheme/host/trailing-slash cosmetics are).
func TestNormalizeVCSURL_PathCasingIsSignificant(t *testing.T) {
	lower := NormalizeVCSURL("https://github.com/entire-vc/evc-mesh/pull/40")
	upper := NormalizeVCSURL("https://github.com/Entire-VC/evc-mesh/pull/40")
	if lower == upper {
		t.Fatalf("path casing was folded away: %q", lower)
	}
}

func TestNormalizeVCSURL_MalformedInputComparesConsistently(t *testing.T) {
	a := NormalizeVCSURL("not a url")
	b := NormalizeVCSURL("not a url")
	if a != b {
		t.Fatalf("same malformed input normalized differently: %q vs %q", a, b)
	}
}

func TestVCSLink_IsReviewSubject(t *testing.T) {
	tests := []struct {
		name string
		meta []byte
		want bool
	}{
		{"role review", []byte(`{"role":"review"}`), true},
		{"role review with source", []byte(`{"role":"review","source":"description"}`), true},
		{"other role", []byte(`{"role":"work"}`), false},
		{"no role", []byte(`{}`), false},
		{"empty metadata", nil, false},
		{"malformed metadata", []byte(`{"role":`), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (VCSLink{Metadata: tc.meta}).IsReviewSubject(); got != tc.want {
				t.Fatalf("IsReviewSubject() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractChangeRequestRefs(t *testing.T) {
	refs := ExtractChangeRequestRefs("MR: https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/19, again (https://gitlab.example.invalid/entire-vc/evc-keep/-/merge_requests/19/) and https://github.com/o/r/pull/5.")
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want 2 (duplicate URL must collapse): %+v", len(refs), refs)
	}
	if refs[0].Provider != VCSProviderGitLab || refs[0].ExternalID != "19" {
		t.Errorf("first ref = %+v, want gitlab !19", refs[0])
	}
	if refs[1].Provider != VCSProviderGitHub || refs[1].ExternalID != "5" {
		t.Errorf("second ref = %+v, want github #5", refs[1])
	}
	if got := ExtractChangeRequestRefs("no links, MR !19"); len(got) != 0 {
		t.Errorf("prose without URL produced refs: %+v", got)
	}
}
