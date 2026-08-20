package service

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// The other direction of the same primitive.
//
// document_comment_anchor_units_test.go covers offsets a client measured and the
// server checks. This file covers text a client quotes and the server measures —
// the door an agent uses, which exists because an agent has no selection to
// measure and gets the arithmetic wrong when it tries: a naive character index
// reports 475 where the byte answer is 853, and the resulting row points
// confidently at somebody else's sentence with nothing anywhere to notice.
//
// Every test here is on Cyrillic, and not as decoration. A character index and a
// byte offset are the same number in ASCII, so an ASCII version of this file
// would pass in full against a service that resolved quotes in entirely the wrong
// units. quoteBodyIsNotUnitAgnostic below refuses to let that happen silently.

// quoteDocumentBody is the fixture markdown.
//
// Three properties are load-bearing and each is asserted rather than trusted:
//   - "первым делом" occurs exactly once — the unambiguous case;
//   - "потом миграция" occurs twice — the ambiguous case, and the case
//     quote_prefix has to settle;
//   - "откатите образ" sits inside ** ** so a quote taken from the RENDERED text
//     spans markup the markdown carries and the quote does not.
const quoteDocumentBody = "# Регламент выката\n\n" +
	"Если прод отдаёт 500 — **откатите образ** первым делом, потом миграция.\n\n" +
	"Второй абзац существует, чтобы промах было куда деть, потом миграция.\n"

const (
	uniqueQuote    = "первым делом"
	repeatedQuote  = "потом миграция"
	renderedQuote  = "откатите образ" // reads across ** in the markdown
	absentQuote    = "этой фразы в документе нет"
	quoteCommentTx = "это предложение противоречит абзацу выше"
)

// seedQuoteBody replaces the fixture document's markdown with the Russian prose
// above.
func seedQuoteBody(t *testing.T, f *documentCommentFixture) {
	t.Helper()
	f.docs.Seed(&domain.Document{
		ID: f.documentID, ProjectID: f.projectID, Title: "Регламент", Body: quoteDocumentBody,
	})
}

// quoteInput is the create under test: a quote and nothing else varying.
func quoteInput(f *documentCommentFixture, quote string) CreateDocumentCommentInput {
	return CreateDocumentCommentInput{
		DocumentID:  f.documentID,
		WorkspaceID: f.wsID,
		Body:        quoteCommentTx,
		Quote:       quote,
		AuthorID:    f.author,
		AuthorType:  domain.ActorTypeUser,
	}
}

// TestQuoteFixtureIsNotUnitAgnostic is the guard on every other test in this
// file.
//
// The whole class of bug being prevented is invisible where a byte offset and a
// character index coincide. If somebody rewrites the fixture into ASCII — or
// moves the quote to the first line — these tests keep passing while proving
// nothing, which is the failure mode that is worse than a red test. So the
// distinction the suite depends on is asserted directly.
func TestQuoteFixtureIsNotUnitAgnostic(t *testing.T) {
	for _, quote := range []string{uniqueQuote, repeatedQuote} {
		at := strings.Index(quoteDocumentBody, quote)
		require.GreaterOrEqual(t, at, 0, "fixture must contain %q", quote)

		asCharacters := utf8.RuneCountInString(quoteDocumentBody[:at])
		assert.NotEqual(t, at, asCharacters,
			"fixture is unit-agnostic for %q: byte offset %d equals the character index, so a "+
				"service resolving in the wrong units would pass every test in this file",
			quote, at)
	}

	assert.Equal(t, 1, strings.Count(quoteDocumentBody, uniqueQuote),
		"the unambiguous case needs a quote that occurs exactly once")
	assert.Equal(t, 2, strings.Count(quoteDocumentBody, repeatedQuote),
		"the ambiguous case needs a quote that occurs more than once")
	assert.NotContains(t, quoteDocumentBody, renderedQuote+" первым",
		"the markup case needs the quote to be interrupted by markdown in the source")
}

// AC1 — the criterion, checked the way the card insists it be checked: by
// slicing the stored body with the stored offsets, not by the request returning
// 201. A 201 is what the broken version also returns.
func TestDocumentCommentService_Create_QuoteResolvesToTheCyrillicPhraseItNames(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	c, err := f.svc.Create(context.Background(), quoteInput(f, uniqueQuote))

	require.NoError(t, err)
	require.NotNil(t, c.Anchor)
	require.NotNil(t, c.Anchor.Start)
	require.NotNil(t, c.Anchor.End)

	assert.Equal(t, uniqueQuote, quoteDocumentBody[*c.Anchor.Start:*c.Anchor.End],
		"the stored offsets must slice the document down to the quoted phrase")
	assert.False(t, c.Anchor.IsOrphaned(), "a resolved anchor has a position")
}

// The offsets are bytes, stated as a number rather than as a property, so the
// test fails loudly if the units ever change rather than quietly agreeing with
// whatever the code now produces.
func TestDocumentCommentService_Create_QuoteOffsetsAreBytesNotCharacters(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	c, err := f.svc.Create(context.Background(), quoteInput(f, uniqueQuote))
	require.NoError(t, err)
	require.NotNil(t, c.Anchor.Start)

	wantBytes := strings.Index(quoteDocumentBody, uniqueQuote)
	naiveCharacters := utf8.RuneCountInString(quoteDocumentBody[:wantBytes])

	assert.Equal(t, wantBytes, *c.Anchor.Start)
	assert.NotEqual(t, naiveCharacters, *c.Anchor.Start,
		"a character index is the number an agent computes by hand; storing it is the bug")
}

// The two directions of the primitive have to agree, and this is the assertion
// that ties them together: an anchor the server resolved must satisfy the guard
// the server applies to anchors a client measured. If either side drifts — a new
// resolver, a stricter guard — this goes red rather than the two quietly
// disagreeing about one column.
func TestDocumentCommentService_Create_ResolvedAnchorSatisfiesTheClientAnchorGuard(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	c, err := f.svc.Create(context.Background(), quoteInput(f, uniqueQuote))
	require.NoError(t, err)

	assert.True(t,
		mdoc.SpanMatchesQuote(quoteDocumentBody, *c.Anchor.Start, *c.Anchor.End, c.Anchor.Exact),
		"the server's own anchor must pass the check the server applies to a client's")
}

// A quote taken from the rendered page carries no markdown, and the markdown it
// came from does. Resolving it is the whole reason the resolver is markup
// tolerant, and the offsets must still bracket the source's version.
func TestDocumentCommentService_Create_QuoteFromRenderedTextSpansMarkup(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	c, err := f.svc.Create(context.Background(), quoteInput(f, renderedQuote+" первым делом"))

	require.NoError(t, err)
	require.NotNil(t, c.Anchor.Start)

	span := quoteDocumentBody[*c.Anchor.Start:*c.Anchor.End]
	assert.Contains(t, span, renderedQuote)
	assert.Contains(t, span, "**", "the source span keeps the markup the quote did not carry")
	assert.True(t, strings.HasSuffix(span, "первым делом"))
}

// AC2 — an ambiguous quote is refused with the number of matches, and the number
// survives the handler's error path.
//
// The count is the entire content of the answer: "occurs twice" tells an agent to
// add context, "ambiguous" tells it nothing. So the assertion is on the typed
// error reaching the caller — that is what handleError renders as
// {code: ambiguous_quote, matches: N} — and not merely on some 400 arriving.
func TestDocumentCommentService_Create_AmbiguousQuoteIsRefusedWithItsMatchCount(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	_, err := f.svc.Create(context.Background(), quoteInput(f, repeatedQuote))

	require.Error(t, err)
	var ambiguous *mdoc.AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous,
		"the typed error must reach the handler intact; flattened into a string, the match count "+
			"is gone and the caller cannot tell what to do next")
	assert.Equal(t, 2, ambiguous.Matches)
	assert.Contains(t, ambiguous.Error(), "2 times")
}

// The other half of AC2: context settles the ambiguity rather than the server
// picking an occurrence. The comment must land on the FIRST "потом миграция",
// the one that follows "первым делом, " — proved by slicing, not by 201.
func TestDocumentCommentService_Create_QuotePrefixSelectsTheIntendedOccurrence(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	input := quoteInput(f, repeatedQuote)
	input.QuotePrefix = "первым делом, "

	c, err := f.svc.Create(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, c.Anchor.Start)

	first := strings.Index(quoteDocumentBody, repeatedQuote)
	assert.Equal(t, first, *c.Anchor.Start, "the prefix names the first occurrence")
	assert.Equal(t, repeatedQuote, quoteDocumentBody[*c.Anchor.Start:*c.Anchor.End])

	// And the negative control: the same quote with the OTHER neighbourhood must
	// land somewhere else. Without this, a resolver that ignored context entirely
	// and always returned the first match would pass the assertion above.
	other := quoteInput(f, repeatedQuote)
	other.QuotePrefix = "куда деть, "

	c2, err := f.svc.Create(context.Background(), other)
	require.NoError(t, err)
	assert.Equal(t, strings.LastIndex(quoteDocumentBody, repeatedQuote), *c2.Anchor.Start,
		"context must actually select, not decorate a fixed answer")
	assert.NotEqual(t, *c.Anchor.Start, *c2.Anchor.Start)
}

// AC3 — quote and anchor together are refused. Two answers to one question, and
// the server must not pick one silently.
func TestDocumentCommentService_Create_QuoteWithAnchorIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	at := strings.Index(quoteDocumentBody, uniqueQuote)
	input := quoteInput(f, uniqueQuote)
	input.Anchor = &domain.DocumentCommentAnchor{
		Exact: uniqueQuote,
		Start: intPointer(at),
		End:   intPointer(at + len(uniqueQuote)),
	}

	_, err := f.svc.Create(context.Background(), input)

	requireValidationErrorOn(t, err, "quote")

	// Deliberately an anchor that is CORRECT: a request refused only because its
	// offsets were also wrong would leave the rule untested. The refusal is about
	// asking and asserting at once, not about the answer being bad.
	require.True(t, mdoc.SpanMatchesQuote(quoteDocumentBody, at, at+len(uniqueQuote), uniqueQuote),
		"this test is only meaningful if the anchor it sends alongside is a valid one")
}

// Context with nothing to narrow. Silently ignoring it would let a caller believe
// they had disambiguated a search that never ran.
func TestDocumentCommentService_Create_QuoteContextWithoutAQuoteIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, prefix, suffix string }{
		{"prefix", "первым делом, ", ""},
		{"suffix", "", ", потом"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupDocumentCommentService(t)
			seedQuoteBody(t, f)

			input := quoteInput(f, "")
			input.QuotePrefix, input.QuoteSuffix = tc.prefix, tc.suffix

			_, err := f.svc.Create(context.Background(), input)

			requireValidationErrorOn(t, err, "quote")
		})
	}
}

// A quote that is not in the document is a 400 about the quote, not a 404 about
// the document and not a comment stored pointing at nothing.
func TestDocumentCommentService_Create_QuoteNotInDocumentIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	_, err := f.svc.Create(context.Background(), quoteInput(f, absentQuote))

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Equal(t, 0, f.comments.Count(), "nothing may be stored for an unfindable quote")
}

// A reply carrying a quote is refused for the reason a reply carrying an anchor
// is refused, and is told about the field it actually sent.
func TestDocumentCommentService_Create_ReplyWithQuoteIsRefusedNamingQuote(t *testing.T) {
	f := setupDocumentCommentService(t)

	// The parent is created against the fixture's own body, then the document is
	// reseeded: the reply is refused before anything reads the markdown, and
	// building the parent under the Cyrillic body would fail for an unrelated
	// reason (its anchor points into the English one).
	parent := f.create(t)
	seedQuoteBody(t, f)

	input := quoteInput(f, uniqueQuote)
	input.ParentCommentID = &parent.ID

	_, err := f.svc.Create(context.Background(), input)

	requireValidationErrorOn(t, err, "quote")
}

// Oversized context is refused rather than scored against every occurrence of the
// quote.
func TestDocumentCommentService_Create_OversizedQuoteContextIsRefused(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	input := quoteInput(f, uniqueQuote)
	input.QuotePrefix = strings.Repeat("я", maxAnchorTextBytes) // 2 bytes a letter

	_, err := f.svc.Create(context.Background(), input)

	requireValidationErrorOn(t, err, "quote_prefix")
}

// AC4 as a property rather than a hope: with no quote in the request, nothing
// about the anchor path changes — including that a comment with neither is still
// a comment on the document as a whole.
func TestDocumentCommentService_Create_WithoutAQuoteTheAnchorPathIsUntouched(t *testing.T) {
	f := setupDocumentCommentService(t)
	seedQuoteBody(t, f)

	at := strings.Index(quoteDocumentBody, uniqueQuote)
	c, err := f.svc.Create(context.Background(), anchoredInput(f, uniqueQuote, at, at+len(uniqueQuote)))

	require.NoError(t, err)
	require.NotNil(t, c.Anchor.Start)
	assert.Equal(t, at, *c.Anchor.Start, "an anchor a client measured is stored as sent, not re-resolved")

	plain := quoteInput(f, "")
	unanchored, err := f.svc.Create(context.Background(), plain)
	require.NoError(t, err)
	assert.Nil(t, unanchored.Anchor, "no quote and no anchor is still a comment on the whole document")
}

// requireValidationErrorOn asserts a 400 naming a particular request field.
func requireValidationErrorOn(t *testing.T, err error, field string) {
	t.Helper()

	require.Error(t, err)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode())
	assert.Contains(t, apiErr.Validation, field,
		"the refusal must name the field the caller actually sent: %+v", apiErr.Validation)
}
