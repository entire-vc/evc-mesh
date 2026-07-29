package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUUID  = "82377a26-b856-4a2b-9c31-1f0e7d5a4b21"
	testShort = "82377a26"
)

// firstRef returns the highest-priority candidate, which is what the resolver
// tries first.
func firstRef(t *testing.T, sources ...TaskRefSource) TaskRef {
	t.Helper()
	refs := ExtractTaskRefs(sources...)
	require.NotEmpty(t, refs, "expected at least one candidate reference")
	return refs[0]
}

func body(text string) TaskRefSource { return TaskRefSource{Name: "body", Text: text} }

func TestExtractTaskRefs_RecognisedSpellings(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantKind  TaskRefKind
		wantFull  string // "" when the form yields a short id
		wantShort string
	}{
		{
			name:     "MESH- prefix (the only form the webhook used to know)",
			text:     "MESH-" + testUUID + " tidy up the gate",
			wantKind: RefKindMeshPrefix,
			wantFull: testUUID,
		},
		{
			name:     "task URL with full uuid",
			text:     "see https://mesh.entire.host/t/" + testUUID + " for context",
			wantKind: RefKindURL,
			wantFull: testUUID,
		},
		{
			name:      "task URL with short id",
			text:      "https://mesh.entire.host/t/" + testShort,
			wantKind:  RefKindURL,
			wantShort: testShort,
		},
		{
			name:     "keyword then bare uuid",
			text:     "Closes: " + testUUID,
			wantKind: RefKindKeywordUUID,
			wantFull: testUUID,
		},
		{
			name:      "hash short id — the form agents actually write",
			text:      "Refs #" + testShort + "\n\nSome more body.",
			wantKind:  RefKindShortID,
			wantShort: testShort,
		},
		{
			name:      "hash short id at start of line",
			text:      "#" + testShort + " merge train fix",
			wantKind:  RefKindShortID,
			wantShort: testShort,
		},
		{
			name:      "keyword then short id without the sigil",
			text:      "Fixes " + testShort,
			wantKind:  RefKindShortID,
			wantShort: testShort,
		},
		{
			name:      "12-hex short id",
			text:      "Refs #82377a26b856",
			wantKind:  RefKindShortID,
			wantShort: "82377a26b856",
		},
		{
			name:     "uppercase is accepted and normalised",
			text:     "MESH-" + "82377A26-B856-4A2B-9C31-1F0E7D5A4B21",
			wantKind: RefKindMeshPrefix,
			wantFull: testUUID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstRef(t, body(tc.text))
			assert.Equal(t, tc.wantKind, got.Kind)
			if tc.wantFull != "" {
				assert.Equal(t, uuid.MustParse(tc.wantFull), got.Full)
				assert.Empty(t, got.Short)
			} else {
				assert.Equal(t, tc.wantShort, got.Short)
				assert.Equal(t, uuid.Nil, got.Full)
			}
		})
	}
}

// The whole point of requiring a sigil or a keyword: a PR body is dense with
// hex that is not a task id. Matching any of these would link the wrong task,
// which is strictly worse than linking nothing.
func TestExtractTaskRefs_RejectsNonReferences(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"bare uuid with no context", "the row is " + testUUID + " in the dump"},
		{"bare hex token", "commit " + testShort + " looks fine"},
		{"github issue number", "closes #114 and #9"},
		{"all-digit hash token", "see #12345678 upstream"},
		{"long sha", "reverted 82377a26b856c04d1f9e3a7b5c2d8e6f0a1b3c4d"},
		{"hex inside a longer word", "abc82377a26def"},
		{"html entity", "spacing &#8212; not a ref"},
		{"short id inside a url path that is not /t/", "https://github.com/entire-vc/evc-mesh/commit/82377a26"},
		{"empty", ""},
		{"prose only", "Bump the pinned toolchain and regenerate mocks."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, ExtractTaskRefs(body(tc.text)),
				"text must not be read as a task reference: %q", tc.text)
		})
	}
}

// A truncated uuid must never be taken as a short-id prefix — resolving the
// head of a uuid is how you silently link a different task that happens to
// share eight hex digits.
func TestExtractTaskRefs_KeywordUUIDNotTruncatedToShortID(t *testing.T) {
	refs := ExtractTaskRefs(body("Refs " + testUUID))
	require.NotEmpty(t, refs)
	assert.Equal(t, RefKindKeywordUUID, refs[0].Kind)
	for _, r := range refs {
		assert.NotEqual(t, RefKindShortID, r.Kind, "uuid head must not surface as a short id")
	}
}

func TestExtractTaskRefs_BranchSegment(t *testing.T) {
	refs := ExtractTaskRefs(
		TaskRefSource{Name: "title", Text: "cost tracking dashboard"},
		TaskRefSource{Name: "body", Text: ""},
		TaskRefSource{Name: "branch", Text: "linus/" + testShort + "-cost-tracking"},
	)
	require.Len(t, refs, 1)
	assert.Equal(t, RefKindBranch, refs[0].Kind)
	assert.Equal(t, testShort, refs[0].Short)
	assert.Equal(t, "branch", refs[0].Source)
}

func TestExtractTaskRefs_BranchWithoutIDYieldsNothing(t *testing.T) {
	assert.Empty(t, ExtractTaskRefs(
		TaskRefSource{Name: "branch", Text: "linus/cost-tracking-dashboard"},
	))
}

// Ordering is the guard against an incidental match in a long body outranking
// a deliberate reference in the title.
func TestExtractTaskRefs_ExplicitFormsOutrankIncidentalOnes(t *testing.T) {
	other := "11111111-2222-4333-8444-555555555555"
	refs := ExtractTaskRefs(
		TaskRefSource{Name: "title", Text: "Refs #aaaa1111"},
		TaskRefSource{Name: "body", Text: "MESH-" + other},
		TaskRefSource{Name: "branch", Text: "linus/bbbb2222-slug"},
	)
	require.Len(t, refs, 3)
	assert.Equal(t, RefKindMeshPrefix, refs[0].Kind, "MESH- must be tried first")
	assert.Equal(t, RefKindShortID, refs[1].Kind)
	assert.Equal(t, RefKindBranch, refs[2].Kind, "branch is the weakest signal, tried last")
}

func TestExtractTaskRefs_DeduplicatesRepeats(t *testing.T) {
	refs := ExtractTaskRefs(body("Refs #" + testShort + " … and again #" + testShort))
	require.Len(t, refs, 1)
	assert.Equal(t, testShort, refs[0].Short)
}

func TestIsShortID(t *testing.T) {
	for _, ok := range []string{"82377a26", "abcdef", "82377a26b856", "0000000a"} {
		assert.True(t, isShortID(ok, true), "%q should be a valid short id", ok)
	}
	for _, bad := range []string{"", "82377", "82377a26b856c", "82377g26", "82377A26"} {
		assert.False(t, isShortID(bad, true), "%q should not be a valid short id", bad)
		assert.False(t, isShortID(bad, false), "%q is malformed regardless of the letter rule", bad)
	}
	// The letter rule is contextual, not intrinsic: an all-digit prefix is a
	// perfectly real task id, refused only where nothing else marks it as one.
	assert.False(t, isShortID("12345678", true), "bare #12345678 reads as an issue number")
	assert.True(t, isShortID("12345678", false), "with a keyword or /t/ path it is just an id")
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", truncate("short", 20))
	assert.Equal(t, "a b c", truncate("a\nb\n\nc", 20), "newlines collapse so a body cannot flood the log")
	assert.Equal(t, "abcde…", truncate("abcdefghij", 5))
}
