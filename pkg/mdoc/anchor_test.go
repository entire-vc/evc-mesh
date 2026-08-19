package mdoc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// The load-bearing one: the server's anchor equals the frontend's
// ---------------------------------------------------------------------------

// frontendFixture is testdata/frontend_anchors.json, produced by running the
// real web/src/lib/doc-comments/anchor.ts — see scripts/gen-anchor-fixture.mjs.
type frontendFixture struct {
	SourceSHA256 map[string]string `json:"_source_sha256"`
	Cases        []struct {
		Name        string `json:"name"`
		Note        string `json:"note"`
		Source      string `json:"source"`
		SameSpace   bool   `json:"same_space"`
		Selection   string `json:"selection"`
		Occurrences int    `json:"occurrences"`
		Anchor      struct {
			Exact  string `json:"exact"`
			Prefix string `json:"prefix"`
			Suffix string `json:"suffix"`
			Start  int    `json:"start"`
			End    int    `json:"end"`
		} `json:"anchor"`
	} `json:"cases"`
}

// TestResolveQuote_MatchesFrontendFixture is the proof that the two
// implementations have not drifted.
//
// The expected offsets in the fixture were not written by a Go author. They came
// out of buildAnchorFromSelection in web/src/lib/doc-comments/anchor.ts, run over
// a real selection of the same text. If this file and that one ever start placing
// the same quote differently, an agent's comment and a human's comment on the
// same sentence land in different places — and nothing else in either toolchain
// would tell us.
//
// What it does NOT prove: that the TypeScript still produces these numbers today.
// Asserting that needs a vitest against the same fixture, in web/, which this
// change does not touch. The fixture records the sha256 of the sources it was
// generated from so the gap is visible rather than assumed.
func TestResolveQuote_MatchesFrontendFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/frontend_anchors.json")
	require.NoError(t, err)

	var fx frontendFixture
	require.NoError(t, json.Unmarshal(raw, &fx))
	require.NotEmpty(t, fx.Cases)
	require.NotEmpty(t, fx.SourceSHA256, "the fixture must record which sources produced it")

	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			// The server is handed exactly what the frontend stored: the quote and
			// the neighbourhood it remembered. Nothing about the selection.
			got, resolveErr := ResolveQuote(c.Source, c.Anchor.Exact, c.Anchor.Prefix, c.Anchor.Suffix)
			require.NoError(t, resolveErr, c.Note)

			assert.Equal(t, c.Anchor.Start, got.Start, "start offset: %s", c.Note)
			assert.Equal(t, c.Anchor.End, got.End, "end offset: %s", c.Note)
			assert.Equal(t, c.Anchor.Exact, got.Exact)

			if c.SameSpace {
				// The markdown and the rendered text are the same string, so the
				// frontend's context came from the same characters the server reads
				// and the two must agree exactly.
				assert.Equal(t, c.Anchor.Prefix, got.Prefix, "prefix")
				assert.Equal(t, c.Anchor.Suffix, got.Suffix, "suffix")
			}
			// Where they are NOT the same string, the frontend takes prefix/suffix
			// from the rendered text and the server from the markdown, deliberately
			// and by both files' documented design. The offsets are what point at
			// text; the context only breaks ties, and comparing markdown context
			// against rendered context would assert a difference we chose.
		})
	}
}

// The fixture has to keep covering the cases it was built for, or it degrades
// into a set of English one-liners that any implementation would pass.
func TestFrontendFixture_CoversTheHardCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/frontend_anchors.json")
	require.NoError(t, err)
	var fx frontendFixture
	require.NoError(t, json.Unmarshal(raw, &fx))

	var cyrillic, multiByteOffset, repeated, crossesMarkup bool
	for _, c := range fx.Cases {
		if strings.ContainsAny(c.Anchor.Exact, "абвгдеёжзийклмнопрстуфхцчшщъыьэюя") {
			cyrillic = true
			// A Cyrillic case only exercises the byte/character distinction if the
			// two numbers actually differ for it.
			if c.SameSpace && c.Anchor.Start != utf8.RuneCountInString(c.Source[:c.Anchor.Start]) {
				multiByteOffset = true
			}
		}
		if c.Occurrences > 1 {
			repeated = true
		}
		if !c.SameSpace {
			crossesMarkup = true
		}
	}

	assert.True(t, cyrillic, "no Cyrillic case: the fixture would not catch a byte/rune mistake")
	assert.True(t, multiByteOffset,
		"every Cyrillic case sits where the byte and character offsets happen to agree, so it proves nothing")
	assert.True(t, repeated, "no repeated-quote case: nothing exercises disambiguation by context")
	assert.True(t, crossesMarkup, "no markup-crossing case: nothing exercises the tolerant scan")
}

// ---------------------------------------------------------------------------
// Cyrillic byte offsets
// ---------------------------------------------------------------------------

const ruDoc = `# Регламент дежурства

Дежурный инженер отвечает за приём инцидентов в рабочие часы и за то, чтобы
каждое обращение получило ответ в течение пятнадцати минут. Если инцидент
затрагивает продакшн, дежурный обязан немедленно поднять эскалацию и позвать
владельца сервиса, не дожидаясь окончания диагностики.
`

// The mandatory test. Byte offsets are the contract, and Russian is where the
// naive answer and the right one part company: on 2026-08-19 a quote sitting at
// byte 853 was reported at 475 by a character index, and an anchor off by that
// much points at somebody else's sentence with total confidence.
func TestResolveQuote_CyrillicOffsetsAreBytes(t *testing.T) {
	const quote = "дежурный обязан немедленно поднять эскалацию"

	got, err := ResolveQuote(ruDoc, quote, "", "")
	require.NoError(t, err)

	// The assertion that matters: slicing the body with the returned offsets
	// yields the phrase, and nothing adjacent to it.
	assert.Equal(t, quote, ruDoc[got.Start:got.End],
		"the anchor must point at the quote when the body is sliced by bytes")

	// And the guard that keeps this test honest. If the phrase happened to sit
	// where the two counts agree, the test above would pass for an implementation
	// that returns character offsets.
	runeIndex := utf8.RuneCountInString(ruDoc[:got.Start])
	assert.NotEqual(t, runeIndex, got.Start,
		"this quote sits where the byte and character offsets coincide, so the test proves nothing — move it")
	assert.Greater(t, got.Start, runeIndex, "UTF-8 Cyrillic is two bytes a letter")
}

func TestResolveQuote_CyrillicContextIsWholeCharacters(t *testing.T) {
	got, err := ResolveQuote(ruDoc, "владельца сервиса", "", "")
	require.NoError(t, err)

	assert.True(t, utf8.ValidString(got.Prefix), "the prefix never cuts a character in half")
	assert.True(t, utf8.ValidString(got.Suffix))
	assert.LessOrEqual(t, utf8.RuneCountInString(got.Prefix), contextLength)
	assert.Contains(t, ruDoc, got.Prefix)
}

// ---------------------------------------------------------------------------
// Ambiguity, absence, and the refusal to guess
// ---------------------------------------------------------------------------

func TestResolveQuote_MissingQuote(t *testing.T) {
	_, err := ResolveQuote("The deploy gate refuses a release.\n", "the rollback gate", "", "")

	var notFound *QuoteNotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Contains(t, err.Error(), "no such quote in this document")
}

// Several matches and no context: the count is the answer, because it is what
// tells the caller whether to add a few words or quote something else.
func TestResolveQuote_AmbiguousReportsTheCount(t *testing.T) {
	src := "the API is here. and the API is here too. and the API once more.\n"

	_, err := ResolveQuote(src, "the API", "", "")

	var ambiguous *AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, 3, ambiguous.Matches)
	assert.Contains(t, err.Error(), "3 times")
}

// The failure this refusal exists to prevent: silently returning the first of
// eleven "the API"s, which looks like a result and is a coin toss.
func TestResolveQuote_NeverSilentlyPicksTheFirst(t *testing.T) {
	src := "alpha the API omega. beta the API omega.\n"

	got, err := ResolveQuote(src, "the API", "", "")

	assert.Nil(t, got)
	require.Error(t, err)
}

func TestResolveQuote_ContextDisambiguates(t *testing.T) {
	src := "alpha the API omega. beta the API omega.\n"

	first, err := ResolveQuote(src, "the API", "alpha ", "")
	require.NoError(t, err)
	second, err := ResolveQuote(src, "the API", "beta ", "")
	require.NoError(t, err)

	assert.Equal(t, "the API", src[first.Start:first.End])
	assert.Equal(t, "the API", src[second.Start:second.End])
	assert.Less(t, first.Start, second.Start, "the prefix picked out different occurrences")
}

func TestResolveQuote_SuffixDisambiguates(t *testing.T) {
	src := "the API returns 200. the API returns 404.\n"

	got, err := ResolveQuote(src, "the API", "", " returns 404")
	require.NoError(t, err)

	assert.Equal(t, " returns 404.\n", src[got.End:])
}

// Context that fits neither candidate any better than the other leaves the
// ambiguity where it was, rather than being treated as a decision.
func TestResolveQuote_UselessContextStillRefuses(t *testing.T) {
	src := "alpha the API omega. alpha the API omega.\n"

	_, err := ResolveQuote(src, "the API", "alpha ", " omega")

	var ambiguous *AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, 2, ambiguous.Matches)
}

// Context that matches nothing at all is not a tie-break either: every candidate
// scores zero, and the best of several zeros is not a winner.
func TestResolveQuote_ContextMatchingNothingStillRefuses(t *testing.T) {
	src := "alpha the API omega. beta the API omega.\n"

	_, err := ResolveQuote(src, "the API", "completely unrelated words", "")

	var ambiguous *AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous)
}

func TestResolveQuote_EmptyQuote(t *testing.T) {
	_, err := ResolveQuote("body\n", "   \n\t ", "", "")

	var empty *EmptyQuoteError
	require.ErrorAs(t, err, &empty)
}

func TestResolveQuote_QuoteTooLong(t *testing.T) {
	long := strings.Repeat("a", maxQuoteBytes+1)

	_, err := ResolveQuote(long, long, "", "")

	var tooLong *QuoteTooLongError
	require.ErrorAs(t, err, &tooLong)
	assert.Equal(t, maxQuoteBytes+1, tooLong.Bytes)
}

// The quote is trimmed, so a caller that copied a trailing newline still gets an
// anchor around the words rather than around the whitespace.
func TestResolveQuote_TrimsTheQuote(t *testing.T) {
	src := "before the quoted words after\n"

	got, err := ResolveQuote(src, "  the quoted words \n", "", "")
	require.NoError(t, err)

	assert.Equal(t, "the quoted words", src[got.Start:got.End])
	assert.Equal(t, "the quoted words", got.Exact)
}

// ---------------------------------------------------------------------------
// The markup-tolerant fallback
// ---------------------------------------------------------------------------

func TestResolveQuote_CrossesBoldMarkup(t *testing.T) {
	src := "Set PUBLIC_API_URL before **building**, or it points at localhost.\n"

	got, err := ResolveQuote(src, "before building", "", "")
	require.NoError(t, err)

	// The span covers the markup the quote had to cross, and stops as soon as the
	// quote is exhausted — the closing `**` is not part of what was quoted. Same
	// as the TypeScript, whose scan also ends on the last needle character.
	assert.Equal(t, "before **building", src[got.Start:got.End],
		"the offsets span the markdown the rendered quote came from, markup included")
	assert.Equal(t, "before building", got.Exact, "the stored quote is what the reader saw")
}

func TestResolveQuote_CrossesALink(t *testing.T) {
	src := "See the [deploy guide](https://example.com/deploy) for the order.\n"

	got, err := ResolveQuote(src, "deploy guide for the order", "", "")
	require.NoError(t, err)

	assert.Equal(t, "deploy guide](https://example.com/deploy) for the order",
		src[got.Start:got.End])
}

// A markdown soft line break renders as one space, so a quote taken from the
// rendered text has a space where the source has a newline.
func TestResolveQuote_WhitespaceRunsAreCompared(t *testing.T) {
	src := "The deploy gate refuses\na release without evidence.\n"

	got, err := ResolveQuote(src, "refuses a release", "", "")
	require.NoError(t, err)

	assert.Equal(t, "refuses\na release", src[got.Start:got.End])
}

// The tolerant scan is quadratic in the worst case, so it is skipped on a large
// document rather than allowed to hold a request open.
func TestResolveQuote_TolerantScanSkippedOnHugeSource(t *testing.T) {
	huge := strings.Repeat("Set the **value** before building. ", tolerantScanMaxSource/35+1)
	require.Greater(t, len(huge), tolerantScanMaxSource)

	_, err := ResolveQuote(huge, "the value before", "", "")

	var notFound *QuoteNotFoundError
	require.ErrorAs(t, err, &notFound, "a quote needing the tolerant scan is not found in a huge document")
}

// Verbatim wins over tolerant: the exact path is exact, and running the fallback
// when a literal match exists would let markup-skipping invent a better-looking
// candidate somewhere else.
func TestResolveQuote_VerbatimBeatsTolerant(t *testing.T) {
	src := "First the **quoted** words, then the quoted words plainly.\n"

	got, err := ResolveQuote(src, "the quoted words", "", "")
	require.NoError(t, err)

	assert.Equal(t, "the quoted words", src[got.Start:got.End])
	assert.Greater(t, got.Start, strings.Index(src, "**"), "the literal occurrence, not the marked-up one")
}

// A pathological document — the same short phrase thousands of times — must not
// turn one request into an unbounded scan. The cap is reported as ambiguity,
// which is the honest answer: it occurs at least this many times.
func TestResolveQuote_CandidateCountIsBounded(t *testing.T) {
	src := strings.Repeat("ping ", maxCandidates*3)

	_, err := ResolveQuote(src, "ping", "", "")

	var ambiguous *AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous)
	assert.Equal(t, maxCandidates, ambiguous.Matches, "the scan stops at the cap rather than counting them all")
}

func TestResolveQuote_TolerantCandidateCountIsBounded(t *testing.T) {
	// Every repetition matches the quote only through markup-skipping, so the
	// tolerant scan is what finds them and its own cap is what stops it.
	src := strings.Repeat("a**b** ", maxTolerantCandidates*3)

	_, err := ResolveQuote(src, "ab", "", "")

	var ambiguous *AmbiguousQuoteError
	require.ErrorAs(t, err, &ambiguous)
	assert.LessOrEqual(t, ambiguous.Matches, maxTolerantCandidates+1)
}

// The error text is what an agent reads to decide what to do next, so it is
// asserted rather than left to whatever the struct happens to render.
func TestResolveQuote_ErrorMessagesSayWhatToDo(t *testing.T) {
	assert.Contains(t, (&EmptyQuoteError{}).Error(), "empty")
	assert.Contains(t, (&QuoteTooLongError{Bytes: 3000}).Error(), "3000")
	assert.Contains(t, (&QuoteTooLongError{Bytes: 3000}).Error(), "one sentence is enough")
	assert.Contains(t, (&QuoteNotFoundError{Quote: "x"}).Error(), "Quote the document exactly")
	assert.Contains(t, (&AmbiguousQuoteError{Matches: 4}).Error(), "4 times")
}

// ---------------------------------------------------------------------------
// The anchor's own shape
// ---------------------------------------------------------------------------

func TestResolveQuote_ContextComesFromTheDocument(t *testing.T) {
	src := "the words before it, the quote, and the words after it\n"

	got, err := ResolveQuote(src, "the quote", "something the caller made up", "")
	require.NoError(t, err)

	assert.Equal(t, "the words before it, ", got.Prefix,
		"the anchor describes the document, not what the caller believed was around the quote")
	assert.Equal(t, ", and the words after it\n", got.Suffix)
}

func TestResolveQuote_AtDocumentEdges(t *testing.T) {
	src := "opening words and closing words"

	first, err := ResolveQuote(src, "opening words", "", "")
	require.NoError(t, err)
	assert.Equal(t, 0, first.Start)
	assert.Empty(t, first.Prefix)

	last, err := ResolveQuote(src, "closing words", "", "")
	require.NoError(t, err)
	assert.Equal(t, len(src), last.End)
	assert.Empty(t, last.Suffix)
}

func TestResolveQuote_ContextIsCappedAtContextLength(t *testing.T) {
	src := strings.Repeat("x", 500) + "QUOTE" + strings.Repeat("y", 500)

	got, err := ResolveQuote(src, "QUOTE", "", "")
	require.NoError(t, err)

	assert.Len(t, got.Prefix, contextLength)
	assert.Len(t, got.Suffix, contextLength)
}

// An astral-plane character is two UTF-16 units, four bytes and one rune. Every
// conversion in the chain has to agree, and this is the one that catches a
// half-encoded surrogate turning into U+FFFD.
func TestResolveQuote_AstralPlaneCharacterBeforeQuote(t *testing.T) {
	src := "Status: 🚀 shipped to production on Tuesday.\n"

	got, err := ResolveQuote(src, "shipped to production", "", "")
	require.NoError(t, err)

	assert.Equal(t, "shipped to production", src[got.Start:got.End])
	assert.Equal(t, "Status: 🚀 ", got.Prefix)
	assert.True(t, utf8.ValidString(got.Prefix))
}
