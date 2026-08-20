package mdoc

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sectionDoc has one section with a subsection and a sibling after it, which is
// the shape every boundary question is about.
const sectionDoc = `# Runbook

Preamble that belongs to nobody in particular.

## Deploy

Push the tag, wait for the gate.

### Rollback

Revert the tag. The migration is additive, so nothing else is needed.

## Monitoring

Watch the error rate for ten minutes.
`

func TestFindSection_IncludesSubsectionsAndStopsAtTheSibling(t *testing.T) {
	got, err := FindSection(sectionDoc, "deploy")
	require.NoError(t, err)

	assert.Equal(t, 2, got.Heading.Level)
	assert.Equal(t, "Deploy", got.Heading.Text)

	assert.True(t, strings.HasPrefix(got.Content, "## Deploy"),
		"the section starts at its own heading, so the text reads as a document")
	assert.Contains(t, got.Content, "Push the tag")
	assert.Contains(t, got.Content, "### Rollback", "a subsection is part of its parent")
	assert.Contains(t, got.Content, "Revert the tag.")
	assert.NotContains(t, got.Content, "## Monitoring", "the next sibling is not")
	assert.NotContains(t, got.Content, "Watch the error rate")
	assert.NotContains(t, got.Content, "Preamble", "nor is what came before it")
}

func TestFindSection_SubsectionStopsAtTheParentsSibling(t *testing.T) {
	got, err := FindSection(sectionDoc, "rollback")
	require.NoError(t, err)

	assert.Contains(t, got.Content, "Revert the tag.")
	assert.NotContains(t, got.Content, "## Monitoring")
	assert.NotContains(t, got.Content, "Push the tag")
}

func TestFindSection_LastSectionRunsToTheEnd(t *testing.T) {
	got, err := FindSection(sectionDoc, "monitoring")
	require.NoError(t, err)

	assert.Equal(t, len(sectionDoc), got.Heading.End)
	assert.True(t, strings.HasSuffix(got.Content, "ten minutes.\n"))
}

// The whole document is one heading's section when that heading is the first
// and highest.
func TestFindSection_TopLevelOwnsEverythingBelowIt(t *testing.T) {
	got, err := FindSection(sectionDoc, "runbook")
	require.NoError(t, err)

	assert.Equal(t, sectionDoc, got.Content)
}

// Offsets are the contract, not the string: a caller that slices the body with
// them must get the same text the API returned.
func TestFindSection_OffsetsSliceTheBody(t *testing.T) {
	got, err := FindSection(sectionDoc, "deploy")
	require.NoError(t, err)

	assert.Equal(t, got.Content, sectionDoc[got.Heading.Start:got.Heading.End])
}

func TestFindSection_ByHeadingText(t *testing.T) {
	got, err := FindSection(sectionDoc, "Deploy")
	require.NoError(t, err)
	assert.Equal(t, "deploy", got.Heading.Anchor)
}

func TestFindSection_ByHeadingTextIsCaseInsensitiveAndTrimmed(t *testing.T) {
	got, err := FindSection(sectionDoc, "   mOnItOrInG  ")
	require.NoError(t, err)
	assert.Equal(t, "monitoring", got.Heading.Anchor)
}

// Text and anchor differ as soon as a heading has punctuation in it, and both
// have to work — one is what the outline hands back, the other is what a human
// instruction says.
func TestFindSection_TextWithPunctuationResolvesEitherWay(t *testing.T) {
	src := "## Rollback, step by step\n\nbody\n"

	byText, err := FindSection(src, "Rollback, step by step")
	require.NoError(t, err)
	byAnchor, err := FindSection(src, "rollback-step-by-step")
	require.NoError(t, err)

	assert.Equal(t, byText.Heading, byAnchor.Heading)
}

// Two identical headings are both addressable — by anchor, which is what the
// anchor is for.
func TestFindSection_DuplicateHeadingsAreBothAddressable(t *testing.T) {
	src := "## Rollback\n\nfirst body\n\n## Rollback\n\nsecond body\n"

	first, err := FindSection(src, "rollback")
	require.NoError(t, err)
	second, err := FindSection(src, "rollback-1")
	require.NoError(t, err)

	assert.Contains(t, first.Content, "first body")
	assert.NotContains(t, first.Content, "second body")
	assert.Contains(t, second.Content, "second body")
	assert.NotContains(t, second.Content, "first body")
}

// Asking for duplicated text is refused rather than answered with the first one:
// both are real answers and picking one is a coin toss dressed up as a result.
func TestFindSection_DuplicateHeadingTextIsRefused(t *testing.T) {
	src := "## Rollback, step by step\n\nfirst\n\n## Rollback, step by step\n\nsecond\n"

	_, err := FindSection(src, "Rollback, step by step")

	var ambiguous *AmbiguousHeadingError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, []string{"rollback-step-by-step", "rollback-step-by-step-1"}, ambiguous.Anchors)
	assert.Contains(t, err.Error(), "rollback-step-by-step-1", "the error names the way to disambiguate")
	assert.Contains(t, err.Error(), "2 headings")
}

// The documented exception: text that IS its own first anchor is not ambiguous,
// because the anchor scheme has already decided what it means. Refusing a caller
// who copied "rollback" out of the outline would be perverse.
func TestFindSection_DuplicateTextEqualToItsOwnAnchorResolvesToTheFirst(t *testing.T) {
	src := "## Rollback\n\nfirst\n\n## Rollback\n\nsecond\n"

	got, err := FindSection(src, "Rollback ")
	require.NoError(t, err)

	assert.Equal(t, "rollback", got.Heading.Anchor)
	assert.Contains(t, got.Content, "first")
}

// A miss is an error naming what is there, not an empty result: an agent that
// asked for the wrong heading needs the list far more than it needs a "no".
func TestFindSection_UnknownHeadingListsWhatIsThere(t *testing.T) {
	_, err := FindSection(sectionDoc, "deployment")

	var notFound *HeadingNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, []string{"deploy", "monitoring", "rollback", "runbook"}, notFound.Available)
	assert.Contains(t, err.Error(), "deploy")
}

func TestFindSection_DocumentWithNoHeadings(t *testing.T) {
	_, err := FindSection("Just prose, no structure at all.\n", "anything")

	var notFound *HeadingNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Empty(t, notFound.Available)
	assert.Contains(t, err.Error(), "no headings")
}

func TestFindSection_EmptyReference(t *testing.T) {
	_, err := FindSection(sectionDoc, "   ")

	var notFound *HeadingNotFoundError
	require.ErrorAs(t, err, &notFound)
}

// The anchor is tried before the text, so a document where one heading's text
// happens to equal another's anchor still resolves the way the outline says.
func TestFindSection_AnchorWinsOverText(t *testing.T) {
	src := "## Deploy Now\n\nfirst\n\n## deploy-now\n\nsecond\n"

	got, err := FindSection(src, "deploy-now")
	require.NoError(t, err)

	assert.Contains(t, got.Content, "first", "the anchor of the first heading, not the text of the second")
}

// A heading inside a code fence is not a section boundary either — the section
// runs straight through the block.
func TestFindSection_CodeFenceInsideASectionDoesNotEndIt(t *testing.T) {
	src := strings.Join([]string{
		"## Deploy",
		"",
		"```bash",
		"# not a heading",
		"deploy --now",
		"```",
		"",
		"Still the deploy section.",
		"",
		"## Next",
	}, "\n")

	got, err := FindSection(src, "deploy")
	require.NoError(t, err)

	assert.Contains(t, got.Content, "Still the deploy section.")
	assert.NotContains(t, got.Content, "## Next")
}

func TestFindSection_Cyrillic(t *testing.T) {
	src := "# Регламент\n\nвступление\n\n## Эскалация\n\nтело раздела\n\n## Постмортем\n\nдругое\n"

	got, err := FindSection(src, "эскалация")
	require.NoError(t, err)

	assert.Equal(t, "тело раздела\n\n", strings.TrimPrefix(got.Content, "## Эскалация\n\n"))
	assert.Equal(t, got.Content, src[got.Heading.Start:got.Heading.End],
		"the byte offsets slice the body, which is the whole point of them being bytes")
}
