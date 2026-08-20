package mdoc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// cyrillicBody is the shape the whole guard exists for: prose where a character
// index and a byte offset are different numbers. Every letter here is two bytes,
// so the two coordinate systems diverge immediately and stay diverged.
const cyrillicBody = "# Заголовок документа\n\n" +
	"Это предложение написано на русском языке для проверки смещений.\n\n" +
	"А это второй абзац, чтобы промах было куда деть.\n"

// renderedProjection is what PR #621 counts in: the text a reader sees, with the
// markdown syntax gone. It is not the markdown, and an index into it is not an
// offset into the markdown.
func renderedProjection(source string) string {
	out := make([]string, 0, 8)
	for _, line := range strings.Split(source, "\n") {
		out = append(out, strings.TrimPrefix(strings.TrimPrefix(line, "# "), "## "))
	}
	return strings.Join(out, "\n")
}

func TestSpanMatchesQuote_AcceptsByteOffsetsIntoTheMarkdown(t *testing.T) {
	quote := "написано на русском языке"
	start := strings.Index(cyrillicBody, quote)
	if start < 0 {
		t.Fatalf("fixture is wrong: %q is not in the body", quote)
	}

	if !SpanMatchesQuote(cyrillicBody, start, start+len(quote), quote) {
		t.Fatalf("byte offsets [%d,%d) were rejected; this is the documented unit and PR #619's",
			start, start+len(quote))
	}
}

// The load-bearing negative control. It has to be on Cyrillic: on ASCII a
// character index and a byte offset are the same number, so the same test would
// pass while checking nothing at all.
func TestSpanMatchesQuote_RejectsCharacterOffsets(t *testing.T) {
	quote := "написано на русском языке"

	byteStart := strings.Index(cyrillicBody, quote)
	charStart := utf8.RuneCountInString(cyrillicBody[:byteStart])
	charEnd := charStart + utf8.RuneCountInString(quote)

	// Positive control on the test itself: if the two units happened to agree
	// here, the assertion below would be vacuous.
	if charStart == byteStart {
		t.Fatalf("fixture is degenerate: character index %d equals byte offset %d, so this "+
			"test cannot distinguish the units", charStart, byteStart)
	}

	if SpanMatchesQuote(cyrillicBody, charStart, charEnd, quote) {
		t.Fatalf("character offsets [%d,%d) were accepted; the quote actually sits at [%d,%d), "+
			"and the slice at the accepted span reads %q",
			charStart, charEnd, byteStart, byteStart+len(quote),
			cyrillicBody[charStart:charEnd])
	}
}

// PR #621's actual model, not an approximation of it: character indices into the
// rendered text projection rather than into the markdown. Two errors compound —
// the units and the coordinate space — and neither shows up on ASCII.
func TestSpanMatchesQuote_RejectsCharacterOffsetsIntoRenderedText(t *testing.T) {
	rendered := renderedProjection(cyrillicBody)
	quote := "написано на русском языке"

	at := strings.Index(rendered, quote)
	if at < 0 {
		t.Fatalf("fixture is wrong: %q is not in the rendered projection", quote)
	}
	charStart := utf8.RuneCountInString(rendered[:at])
	charEnd := charStart + utf8.RuneCountInString(quote)

	if SpanMatchesQuote(cyrillicBody, charStart, charEnd, quote) {
		t.Fatalf("rendered-space character offsets [%d,%d) were accepted against the markdown",
			charStart, charEnd)
	}
}

// The case a naive body[start:end] == quote guard would break: the selection
// crossed inline markup, so the raw slice carries syntax the quote does not.
func TestSpanMatchesQuote_AcceptsASelectionThatCrossedMarkup(t *testing.T) {
	body := "Если прод отдаёт 500 — **откатите образ** первым делом, потом миграция.\n"
	quote := "откатите образ первым делом"

	start := strings.Index(body, "откатите")
	end := start + len("откатите образ** первым делом")

	if slice := body[start:end]; slice == quote {
		t.Fatalf("fixture is degenerate: the raw slice %q already equals the quote, so this "+
			"test would pass under a strict-equality guard", slice)
	}
	if !SpanMatchesQuote(body, start, end, quote) {
		t.Fatalf("a markup-crossing anchor was rejected: body[%d:%d] = %q, quote = %q",
			start, end, body[start:end], quote)
	}
}

func TestSpanMatchesQuote_AcceptsBothOccurrencesOfARepeatedQuote(t *testing.T) {
	body := "Сначала про API, потом ещё раз про API в конце.\n"
	quote := "про API"

	first := strings.Index(body, quote)
	second := strings.Index(body[first+1:], quote) + first + 1
	if first == second || second <= first {
		t.Fatalf("fixture is wrong: expected two occurrences, got %d and %d", first, second)
	}

	// The guard answers "is the span the caller named the quote they named", not
	// "which occurrence did they mean". Re-resolving here would reject a correct
	// anchor for being ambiguous, which is ResolveQuote's problem and not this
	// one's.
	for _, at := range []int{first, second} {
		if !SpanMatchesQuote(body, at, at+len(quote), quote) {
			t.Fatalf("occurrence at %d was rejected", at)
		}
	}
}

func TestSpanMatchesQuote_RejectsMalformedAndNearMissSpans(t *testing.T) {
	quote := "написано на русском языке"
	start := strings.Index(cyrillicBody, quote)
	end := start + len(quote)

	cases := []struct {
		name       string
		start, end int
		quote      string
	}{
		{"empty quote", start, end, ""},
		{"negative start", -1, end, quote},
		{"empty range", start, start, quote},
		{"inverted range", end, start, quote},
		{"end past the body", start, len(cyrillicBody) + 1, quote},
		{"start inside a character", start + 1, end, quote},
		{"end inside a character", start, end - 1, quote},
		{"shifted one character left", start - 2, end - 2, quote},
		{"quote that is not in the document", start, end, "какой-то другой текст"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if SpanMatchesQuote(cyrillicBody, tc.start, tc.end, tc.quote) {
				t.Fatalf("accepted [%d,%d) for %q", tc.start, tc.end, tc.quote)
			}
		})
	}
}

func TestSpanMatchesQuote_AcceptsTheAnchorResolveQuoteItselfProduces(t *testing.T) {
	// Whatever the two directions disagree about, they must not disagree about an
	// anchor one of them just produced.
	for _, quote := range []string{
		"написано на русском языке",
		"Заголовок документа",
		"второй абзац",
	} {
		anchor, err := ResolveQuote(cyrillicBody, quote, "", "")
		if err != nil {
			t.Fatalf("ResolveQuote(%q): %v", quote, err)
		}
		if !SpanMatchesQuote(cyrillicBody, anchor.Start, anchor.End, anchor.Exact) {
			t.Fatalf("ResolveQuote placed %q at [%d,%d) and SpanMatchesQuote rejected it",
				quote, anchor.Start, anchor.End)
		}
	}
}

func TestSpanMatchesQuote_AcceptsAQuoteWithUntrimmedEdges(t *testing.T) {
	quote := "написано на русском языке"
	start := strings.Index(cyrillicBody, quote)

	if !SpanMatchesQuote(cyrillicBody, start, start+len(quote), "  "+quote+"\n") {
		t.Fatal("a quote whose edges carry whitespace names the same range as its trimmed form")
	}
}
