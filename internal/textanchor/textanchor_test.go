package textanchor

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cyrillicBody is the fixture the coordinate-system tests run against.
//
// It is Russian on purpose, and every assertion that could be written against an
// English body is written against this one instead. On ASCII a byte offset, a
// character offset and a UTF-16 index are the same number, so an ASCII test of
// this package passes whichever of the three the implementation happens to use —
// it is green and it proves nothing. That is how the 475/853 defect reached prod
// once already.
const cyrillicBody = `# Резолвер цитаты в якорь

Смещения якоря — это байтовые смещения в исходный markdown, полуинтервал
[start, end). Наивный индекс по символам даёт другое число.

Правильный ответ — байты, а не символы. Проверять срезом байтов.
`

// requireByteOffsetsAreNotCharacterOffsets fails the test when the fixture it is
// given cannot tell the two apart.
//
// Without it a "Cyrillic test" can still be vacuous: if the quote happens to sit
// in an all-ASCII prefix of the body, both coordinate systems agree at that
// position and the assertion below holds for a wrong implementation too. This is
// the discriminator, asserted explicitly rather than assumed.
func requireByteOffsetsAreNotCharacterOffsets(t *testing.T, body string, start int) {
	t.Helper()
	chars := utf8.RuneCountInString(body[:start])
	require.NotEqual(t, chars, start,
		"fixture is vacuous: at this position a character offset (%d) and a byte offset (%d) coincide, "+
			"so the test would pass on an implementation using either", chars, start)
}

// --- AC1: the anchor points at the quote, on Cyrillic ---

func TestResolve_CyrillicAnchorPointsAtItsOwnQuote(t *testing.T) {
	quote := "Правильный ответ — байты, а не символы."

	anchor, err := Resolve(cyrillicBody, quote, Context{})
	require.NoError(t, err)

	requireByteOffsetsAreNotCharacterOffsets(t, cyrillicBody, anchor.Start)

	// The acceptance criterion itself: slice the body BYTES with the offsets that
	// came back and see the quote. Nothing about the numbers is asserted directly —
	// this is what "the anchor points at the phrase" means operationally.
	assert.Equal(t, quote, cyrillicBody[anchor.Start:anchor.End],
		"the returned offsets do not slice back to the quote")
	assert.Equal(t, quote, anchor.Exact)
	assert.Equal(t, len(quote), anchor.End-anchor.Start)
}

func TestResolve_CyrillicOffsetsAreBytesNotCharacters(t *testing.T) {
	quote := "Проверять срезом байтов."

	anchor, err := Resolve(cyrillicBody, quote, Context{})
	require.NoError(t, err)

	// The two numbers the 2026-08-19 measurement was about, named here so a
	// regression reads as "it returned the character offset" rather than as an
	// unexplained integer mismatch.
	wantBytes := strings.Index(cyrillicBody, quote)
	naiveChars := utf8.RuneCountInString(cyrillicBody[:wantBytes])
	require.NotEqual(t, wantBytes, naiveChars)

	assert.Equal(t, wantBytes, anchor.Start)
	assert.NotEqual(t, naiveChars, anchor.Start,
		"this is the character offset, not the byte offset — the anchor points %d bytes short",
		wantBytes-naiveChars)
}

func TestResolve_CyrillicNeighboursAreWholeRunes(t *testing.T) {
	quote := "Наивный индекс по символам"

	anchor, err := Resolve(cyrillicBody, quote, Context{})
	require.NoError(t, err)

	assert.True(t, utf8.ValidString(anchor.Prefix), "prefix was cut inside a rune: %q", anchor.Prefix)
	assert.True(t, utf8.ValidString(anchor.Suffix), "suffix was cut inside a rune: %q", anchor.Suffix)
	assert.True(t, strings.HasSuffix(cyrillicBody[:anchor.Start], anchor.Prefix))
	assert.True(t, strings.HasPrefix(cyrillicBody[anchor.End:], anchor.Suffix))
	assert.LessOrEqual(t, utf8.RuneCountInString(anchor.Prefix), ContextLength)
	assert.LessOrEqual(t, utf8.RuneCountInString(anchor.Suffix), ContextLength)
}

// --- AC2: several matches → the count, never the first one ---

func TestResolve_AmbiguousQuoteCarriesTheMatchCount(t *testing.T) {
	body := "Токен обязателен. Токен обязателен. Токен обязателен."
	quote := "Токен обязателен."

	_, err := Resolve(body, quote, Context{})

	var ambiguous *AmbiguousError
	require.True(t, errors.As(err, &ambiguous), "want AmbiguousError, got %#v", err)
	assert.Equal(t, 3, ambiguous.Matches)
	assert.False(t, ambiguous.AtLeast)
	assert.Contains(t, ambiguous.Error(), "3 times")
}

func TestResolve_AmbiguousQuoteDoesNotSilentlyTakeTheFirst(t *testing.T) {
	body := "первый раз здесь. второй раз здесь. третий раз здесь."
	quote := "раз здесь"

	anchor, err := Resolve(body, quote, Context{})

	require.Error(t, err, "an ambiguous quote must not resolve")
	assert.Zero(t, anchor.Start, "no anchor may be returned alongside the refusal")
	assert.Zero(t, anchor.End)
	assert.NotEqual(t, strings.Index(body, quote), anchor.Start)
}

func TestResolve_ContextPicksBetweenRepeats(t *testing.T) {
	body := "первый раз здесь. второй раз здесь. третий раз здесь."
	quote := "раз здесь"

	anchor, err := Resolve(body, quote, Context{Prefix: "второй "})
	require.NoError(t, err)

	assert.Equal(t, quote, body[anchor.Start:anchor.End])
	assert.Equal(t, strings.Index(body, "второй раз здесь")+len("второй "), anchor.Start)
}

func TestResolve_SurroundingFragmentPicksBetweenRepeats(t *testing.T) {
	body := "первый раз здесь. второй раз здесь. третий раз здесь."
	quote := "раз здесь"

	anchor, err := Resolve(body, quote, Context{Fragment: "третий раз здесь."})
	require.NoError(t, err)

	assert.Equal(t, strings.Index(body, "третий раз здесь")+len("третий "), anchor.Start)
	assert.Equal(t, quote, body[anchor.Start:anchor.End])
}

func TestResolve_ContextThatFitsSeveralIsStillAmbiguous(t *testing.T) {
	body := "у нас раз здесь, и у нас раз здесь, и снова у нас раз здесь."
	quote := "раз здесь"

	// "у нас " precedes all three. A context that narrows nothing must not be
	// treated as having narrowed something.
	_, err := Resolve(body, quote, Context{Prefix: "у нас "})

	var ambiguous *AmbiguousError
	require.True(t, errors.As(err, &ambiguous), "want AmbiguousError, got %#v", err)
	assert.Equal(t, 3, ambiguous.Matches)
}

func TestResolve_ContextMatchingNoneIsStillAmbiguous(t *testing.T) {
	body := "раз здесь. раз здесь."
	quote := "раз здесь"

	_, err := Resolve(body, quote, Context{Prefix: "ничего похожего "})

	var ambiguous *AmbiguousError
	assert.True(t, errors.As(err, &ambiguous), "want AmbiguousError, got %#v", err)
}

// --- AC3: no such quote ---

func TestResolve_MissingQuoteIsNamedAsSuch(t *testing.T) {
	_, err := Resolve(cyrillicBody, "этой фразы в документе нет", Context{})

	var notFound *NotFoundError
	require.True(t, errors.As(err, &notFound), "want NotFoundError, got %#v", err)
	assert.Contains(t, notFound.Error(), "no such quote in the document")
	assert.Contains(t, notFound.Error(), "этой фразы")
}

func TestResolve_EmptyQuoteIsRefused(t *testing.T) {
	_, err := Resolve(cyrillicBody, "", Context{})
	assert.ErrorIs(t, err, ErrEmptyQuote)
}

func TestResolve_ContextFormsAreExclusive(t *testing.T) {
	_, err := Resolve(cyrillicBody, "байты", Context{Prefix: "ответ — ", Fragment: "ответ — байты, а"})

	var ce *ContextError
	require.True(t, errors.As(err, &ce), "want ContextError, got %#v", err)
	assert.Contains(t, ce.Error(), "not both")
}

func TestResolve_FragmentWithoutTheQuoteIsRefused(t *testing.T) {
	_, err := Resolve(cyrillicBody, "байты", Context{Fragment: "совершенно другой фрагмент"})

	var ce *ContextError
	require.True(t, errors.As(err, &ce), "want ContextError, got %#v", err)
	assert.Contains(t, ce.Error(), "does not contain the quote")
}

// --- AC5: a quote that spans markup ---

func TestResolve_QuoteSpanningMarkupResolvesToTheSourceRange(t *testing.T) {
	body := "Порядок отката: **сначала верните образ**, потом миграцию."
	// What the reader selected, and therefore what an agent quotes: the rendered
	// text, with no asterisks in it.
	quote := "сначала верните образ, потом миграцию."

	anchor, err := Resolve(body, quote, Context{})
	require.NoError(t, err)

	slice := body[anchor.Start:anchor.End]
	assert.Contains(t, slice, "**", "the raw slice is expected to carry the markup the quote does not")
	assert.NotEqual(t, quote, slice, "exact and the raw slice differ across markup — that is the design")
	assert.Equal(t, "сначала верните образ**, потом миграцию.", slice)
	assert.Equal(t, quote, anchor.Exact)

	// The invariant that survives markup, and the one the anchor guard has to use.
	assert.True(t, SpanMatchesQuote(body, Span{Start: anchor.Start, End: anchor.End}, quote))
}

func TestResolve_QuoteSpanningALinkResolves(t *testing.T) {
	body := "См. [руководство по деплою](https://example.com/deploy) перед выкаткой."
	quote := "руководство по деплою перед выкаткой."

	anchor, err := Resolve(body, quote, Context{})
	require.NoError(t, err)

	assert.Contains(t, body[anchor.Start:anchor.End], "https://example.com/deploy")
	assert.True(t, SpanMatchesQuote(body, Span{Start: anchor.Start, End: anchor.End}, quote))
}

func TestResolve_QuoteAcrossASoftLineBreakResolves(t *testing.T) {
	body := "Смещения якоря — это байтовые смещения в исходный markdown, полуинтервал\n[start, end)."
	// A reader sees one line; the source wraps. Whitespace runs compare as runs.
	quote := "полуинтервал [start, end)."

	anchor, err := Resolve(body, quote, Context{})
	require.NoError(t, err)
	assert.Equal(t, "полуинтервал\n[start, end).", body[anchor.Start:anchor.End])
}

// --- AC6: grapheme safety ---

func TestResolve_OffsetsNeverLandInsideACombiningSequence(t *testing.T) {
	// "café" written as e + U+0301, so "cafe" is a legal byte prefix of a glyph
	// the reader sees as one character.
	body := "заказ: café latte, оплачено"

	_, err := Resolve(body, "cafe", Context{})

	var notFound *NotFoundError
	require.True(t, errors.As(err, &notFound),
		"a match ending between 'e' and its combining acute is a hit on half a character, not an anchor")

	// The whole cluster is findable, and its end is a boundary.
	anchor, err := Resolve(body, "café latte", Context{})
	require.NoError(t, err)
	assert.True(t, IsGraphemeBoundary(body, anchor.Start))
	assert.True(t, IsGraphemeBoundary(body, anchor.End))
}

func TestResolve_OffsetsNeverLandInsideAZWJCluster(t *testing.T) {
	// 👩‍👧 is woman + ZWJ + girl: three code points, one glyph.
	family := "\U0001F469\u200d\U0001F467"
	body := "семья " + family + " приехала"

	_, err := Resolve(body, "\U0001F469", Context{})
	var notFound *NotFoundError
	assert.True(t, errors.As(err, &notFound),
		"matching the woman alone cuts the family in half at a valid UTF-8 boundary")

	anchor, err := Resolve(body, family, Context{})
	require.NoError(t, err)
	assert.Equal(t, family, body[anchor.Start:anchor.End])
	assert.True(t, IsGraphemeBoundary(body, anchor.Start))
	assert.True(t, IsGraphemeBoundary(body, anchor.End))
}

func TestIsGraphemeBoundary(t *testing.T) {
	body := "ab" + "é" + "cd"

	assert.True(t, IsGraphemeBoundary(body, 0))
	assert.True(t, IsGraphemeBoundary(body, len(body)))
	assert.True(t, IsGraphemeBoundary(body, 2), "start of the é cluster")
	assert.False(t, IsGraphemeBoundary(body, 3), "between 'e' and its combining acute")
	assert.True(t, IsGraphemeBoundary(body, 5), "end of the é cluster")
	assert.False(t, IsGraphemeBoundary(body, 4), "mid-rune inside the combining acute")
}

// --- the guard direction (#44ad429a consumes this) ---

func TestSpanMatchesQuote_AcceptsAByteAnchorOnCyrillic(t *testing.T) {
	quote := "Правильный ответ"
	start := strings.Index(cyrillicBody, quote)

	assert.True(t, SpanMatchesQuote(cyrillicBody, Span{Start: start, End: start + len(quote)}, quote))
}

func TestSpanMatchesQuote_RejectsACharacterOffsetAnchorOnCyrillic(t *testing.T) {
	quote := "Правильный ответ"
	byteStart := strings.Index(cyrillicBody, quote)
	charStart := utf8.RuneCountInString(cyrillicBody[:byteStart])
	require.NotEqual(t, byteStart, charStart, "fixture must distinguish the two coordinate systems")

	// The anchor a client counting characters would send for the same selection.
	assert.False(t, SpanMatchesQuote(
		cyrillicBody,
		Span{Start: charStart, End: charStart + utf8.RuneCountInString(quote)},
		quote,
	), "a character-offset anchor points at different text and must not pass as a byte anchor")
}

func TestSpanMatchesQuote_RejectsDegenerateSpans(t *testing.T) {
	assert.False(t, SpanMatchesQuote(cyrillicBody, Span{Start: 0, End: 0}, "Резолвер"))
	assert.False(t, SpanMatchesQuote(cyrillicBody, Span{Start: -1, End: 5}, "Резолвер"))
	assert.False(t, SpanMatchesQuote(cyrillicBody, Span{Start: 0, End: len(cyrillicBody) + 1}, "Резолвер"))
	assert.False(t, SpanMatchesQuote(cyrillicBody, Span{Start: 0, End: 5}, ""))
}

// --- FindQuote, the primitive both directions share ---

func TestFindQuote_ReturnsEveryOccurrence(t *testing.T) {
	body := "раз. два. раз. три. раз."

	spans := FindQuote(body, "раз")

	require.Len(t, spans, 3)
	for _, span := range spans {
		assert.Equal(t, "раз", body[span.Start:span.End])
	}
}

func TestFindQuote_PrefersVerbatimOverTolerant(t *testing.T) {
	// The quote is present both verbatim and as a markup-crossing match. Verbatim
	// wins outright: the tolerant scan is a fallback, not an alternative.
	body := "**одно** слово, и ещё одно слово."

	spans := FindQuote(body, "одно слово")

	require.Len(t, spans, 1)
	assert.Equal(t, "одно слово", body[spans[0].Start:spans[0].End])
	assert.Equal(t, strings.LastIndex(body, "одно слово"), spans[0].Start)
}

func TestFindQuote_EmptyQuoteFindsNothing(t *testing.T) {
	assert.Empty(t, FindQuote(cyrillicBody, ""))
}
