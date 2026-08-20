package mdoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func anchors(hs []Heading) []string {
	out := make([]string, len(hs))
	for i := range hs {
		out[i] = hs[i].Anchor
	}
	return out
}

func texts(hs []Heading) []string {
	out := make([]string, len(hs))
	for i := range hs {
		out[i] = hs[i].Text
	}
	return out
}

func TestOutline_LevelsTextAndAnchors(t *testing.T) {
	src := strings.Join([]string{
		"# Runbook",
		"",
		"Intro paragraph.",
		"",
		"## Deploy",
		"### Rollback",
		"###### Deepest",
		"",
	}, "\n")

	got := Outline(src)

	require.Len(t, got, 4)
	assert.Equal(t, []int{1, 2, 3, 6}, []int{got[0].Level, got[1].Level, got[2].Level, got[3].Level})
	assert.Equal(t, []string{"Runbook", "Deploy", "Rollback", "Deepest"}, texts(got))
	assert.Equal(t, []string{"runbook", "deploy", "rollback", "deepest"}, anchors(got))
	assert.Equal(t, []int{1, 5, 6, 7}, []int{got[0].Line, got[1].Line, got[2].Line, got[3].Line})

	for _, h := range got {
		assert.Equal(t, src[h.Start:h.Start+h.Level], strings.Repeat("#", h.Level),
			"Start is the byte offset of the heading line itself")
	}
}

func TestOutline_EmptyDocumentIsEmptySlice(t *testing.T) {
	got := Outline("")
	assert.NotNil(t, got, "marshals as [] rather than null")
	assert.Empty(t, got)
}

// The negative control the whole parser exists for. Every runbook here contains
// shell, and `# something` is a comment in it — a line-by-line scan with no
// fence state puts that in the table of contents.
func TestOutline_HashInsideCodeFenceIsNotAHeading(t *testing.T) {
	src := strings.Join([]string{
		"# Real Heading",
		"",
		"```bash",
		"# rebuild the index",
		"## not a heading either",
		"reindex --all",
		"```",
		"",
		"## Second Real Heading",
		"",
		"~~~",
		"# tilde fences count too",
		"~~~",
		"",
		"### Third Real Heading",
	}, "\n")

	got := Outline(src)

	assert.Equal(t, []string{"Real Heading", "Second Real Heading", "Third Real Heading"}, texts(got),
		"a # inside a fenced code block is a comment, not a heading")
}

// A fence closes only on the same character, at least as long. A shorter run, or
// the other character, leaves the block open — and if the parser got that wrong
// it would either swallow the rest of the document or leak the middle of it.
func TestOutline_FenceClosingRules(t *testing.T) {
	src := strings.Join([]string{
		"````",
		"```",
		"# still inside the longer fence",
		"````",
		"",
		"# Out again",
	}, "\n")

	assert.Equal(t, []string{"Out again"}, texts(Outline(src)))
}

// A line indented four spaces or more is an indented code block, not a fence, so
// it cannot CLOSE one. A parser that let it close would end the block early and
// put the shell comments below it in the outline.
func TestOutline_IndentedLineCannotCloseAFence(t *testing.T) {
	src := strings.Join([]string{
		"```",
		"# inside the fence",
		"    ```",
		"# still inside: that line is four spaces in",
		"```",
		"",
		"# Out",
	}, "\n")

	assert.Equal(t, []string{"Out"}, texts(Outline(src)))
}

// Nor can it OPEN one — and the unindented heading after it is a real heading,
// which is what stops this from being a way to hide the rest of a document.
func TestOutline_IndentedLineCannotOpenAFence(t *testing.T) {
	src := "    ```\n# A Heading After Indented Code\n"

	assert.Equal(t, []string{"A Heading After Indented Code"}, texts(Outline(src)))
}

// One or two backticks are inline code, not a fence.
func TestOutline_ShortBacktickRunIsNotAFence(t *testing.T) {
	src := "``\n\n# Still A Heading\n"

	assert.Equal(t, []string{"Still A Heading"}, texts(Outline(src)))
}

// Inline code in a sentence must not open a block: the info string of a backtick
// fence may not itself contain a backtick.
func TestOutline_InlineCodeDoesNotOpenAFence(t *testing.T) {
	src := "Use ```go fmt``` sparingly.\n\n# Heading After Inline Code\n"

	assert.Equal(t, []string{"Heading After Inline Code"}, texts(Outline(src)))
}

// Every document in this workspace is required to carry YAML frontmatter, and #
// is YAML's comment marker. Without the frontmatter state, a commented-out field
// at the top of the file becomes the document's first heading.
func TestOutline_FrontmatterCommentIsNotAHeading(t *testing.T) {
	src := strings.Join([]string{
		"---",
		"# created: 2026-08-20",
		"title: Runbook",
		"tags:",
		"  - mesh",
		"---",
		"",
		"# The Actual Title",
	}, "\n")

	assert.Equal(t, []string{"The Actual Title"}, texts(Outline(src)))
}

// A `---` in the middle of a document is a thematic break, not the start of
// frontmatter: only line 1 opens it.
func TestOutline_ThematicBreakMidDocumentIsNotFrontmatter(t *testing.T) {
	src := "# One\n\n---\n\n# Two\n\n---\n\n# Three\n"

	assert.Equal(t, []string{"One", "Two", "Three"}, texts(Outline(src)))
}

func TestOutline_NotHeadings(t *testing.T) {
	cases := map[string]string{
		"no space after the marker": "#hashtag in prose\n",
		"seven hashes":              "####### too deep\n",
		"indented four spaces":      "    # indented code block\n",
		"hash mid-line":             "prose with # in it\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, Outline(src))
		})
	}
}

func TestOutline_ThreeSpaceIndentIsStillAHeading(t *testing.T) {
	assert.Equal(t, []string{"Indented But Legal"}, texts(Outline("   # Indented But Legal\n")))
}

func TestOutline_ClosingHashSequenceIsStripped(t *testing.T) {
	got := Outline("## Heading ##\n\n## C#\n\n## ###\n")

	require.Len(t, got, 3)
	assert.Equal(t, "Heading", got[0].Text, "a separated closing run is markup")
	assert.Equal(t, "C#", got[1].Text, "an unseparated # is part of the text")
	assert.Equal(t, "", got[2].Text, "a heading that is only a closing run has no text")
}

// Anchors have to keep Cyrillic: an ASCII-only slugifier reduces every heading in
// a Russian document to the empty string, so none of them is addressable.
func TestOutline_AnchorsKeepNonASCIILetters(t *testing.T) {
	got := Outline("# Регламент дежурства\n\n## Эскалация\n")

	assert.Equal(t, []string{"регламент-дежурства", "эскалация"}, anchors(got))
}

func TestOutline_AnchorDropsPunctuationAndInlineMarkup(t *testing.T) {
	got := Outline("## The **bold** word, and a `code` span!\n")

	assert.Equal(t, "the-bold-word-and-a-code-span", got[0].Anchor)
	assert.Equal(t, "The **bold** word, and a `code` span!", got[0].Text,
		"Text is what the document says; only the anchor is normalised")
}

// Slugify is exported for reuse by document slug generation
// (internal/service/document_service.go), which needs the same unicode-aware
// rule but its own fallback for an empty result — this locks down that unlike
// anchorize, Slugify returns "" rather than substituting anything.
func TestSlugify_ReturnsEmptyRatherThanAFallback(t *testing.T) {
	assert.Equal(t, "", Slugify("***"))
	assert.Equal(t, "регламент-дежурства", Slugify("Регламент дежурства"))
}

func TestOutline_HeadingWithNoSluggableCharacters(t *testing.T) {
	got := Outline("## ***\n\n## +++\n")

	assert.Equal(t, []string{"section", "section-1"}, anchors(got),
		"a heading of pure punctuation is still addressable, and two of them are still distinct")
}

// Duplicate headings must each be addressable — the spec's requirement, and the
// reason the anchor is not simply the slug.
func TestOutline_DuplicateHeadingsGetDistinctAnchors(t *testing.T) {
	got := Outline("## Rollback\n\n## Rollback\n\n## Rollback\n")

	assert.Equal(t, []string{"rollback", "rollback-1", "rollback-2"}, anchors(got))
}

// And the collision the naive counter walks into: a document that already
// contains the disambiguated form.
func TestOutline_DuplicateAnchorsDoNotCollideWithARealHeading(t *testing.T) {
	got := Outline("## Rollback\n\n## Rollback 1\n\n## Rollback\n")

	assert.Equal(t, []string{"rollback", "rollback-1", "rollback-2"}, anchors(got))
	assert.Len(t, uniqueStrings(anchors(got)), 3, "every anchor addresses exactly one heading")
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// End is the boundary the section extraction is cut on, so it is worth pinning
// directly: a heading owns its subsections and stops at its next sibling.
func TestOutline_EndSpansSubsectionsButNotSiblings(t *testing.T) {
	src := strings.Join([]string{
		"# Top",       // 0
		"## Alpha",    // 1
		"### Alpha A", // 2
		"body",        // 3
		"## Beta",     // 4
		"body",        // 5
		"",
	}, "\n")

	got := Outline(src)
	require.Len(t, got, 4)

	top, alpha, alphaA, beta := got[0], got[1], got[2], got[3]

	assert.Equal(t, len(src), top.End, "the top-level heading runs to the end of the document")
	assert.Equal(t, beta.Start, alpha.End, "a section ends where its next sibling begins")
	assert.Equal(t, beta.Start, alphaA.End, "a subsection ends at the parent's next sibling too")
	assert.Contains(t, src[alpha.Start:alpha.End], "### Alpha A", "subsections are inside")
	assert.NotContains(t, src[alpha.Start:alpha.End], "## Beta", "the next sibling is not")
}

func TestOutline_CRLFLineEndings(t *testing.T) {
	got := Outline("# One\r\n\r\n```\r\n# not a heading\r\n```\r\n\r\n## Two\r\n")

	assert.Equal(t, []string{"One", "Two"}, texts(got))
}
