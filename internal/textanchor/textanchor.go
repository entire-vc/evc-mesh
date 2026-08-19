// Package textanchor locates a quote inside a document body and reports where it
// sits, in the coordinate system the API stores.
//
// # The coordinate system, which is the whole point
//
// Every offset here is a UTF-8 BYTE offset into the raw markdown body, half-open
// [Start, End). That is what migrations/20260819100_create_document_comments.sql
// documents anchor_start/anchor_end to be, what memory_chunks uses, and what
// Postgres substring() and Go's len(string) agree with.
//
// Nothing else is interchangeable with it. A character offset, a UTF-16 index, or
// an offset into a rendered text projection is a different number for the same
// position — and on ASCII all four coincide, which is exactly what makes getting
// it wrong invisible until the document is written in Russian. Measured on a live
// body on 2026-08-19: a naive character index gave 475 where the correct byte
// answer was 853. A wrongly built anchor does not fail; it silently points at
// different words.
//
// # Why the server computes this
//
// A comment anchor is a quote plus the offsets of that quote. The frontend takes
// the offsets from a mouse selection, which is a thing it can measure. An agent
// has no selection, and an agent computing offsets from the text is the 475/853
// case above. So an agent sends the quote and the server finds it — that is this
// package.
//
// # One implementation, two callers
//
// Two pieces of work need the same primitive and must not grow two answers to it:
//
//   - computing an anchor from a quote (Resolve), for a caller with no selection;
//   - rejecting an anchor whose offsets do not point at their quote
//     (SpanMatchesQuote), for the anchors the frontend sends.
//
// Both are "given a body and a quote, where is it" read in opposite directions,
// and they are here together so that they cannot disagree about one document.
//
// # Where the resolution rules come from
//
// They are a port of the frontend's, not a second design:
//
//   - the ladder — identity (the quote) first, position only as a tie-break, and a
//     refusal rather than a guess at the bottom — is web/src/lib/docs/anchor.ts
//     (PR #621), the module D6 and D7 share;
//   - the markup-tolerant scan and the byte-offset conversion are
//     web/src/lib/doc-comments/{anchor,offsets}.ts (PR #619, merged).
//
// The grapheme rule below is the one part that is NOT a port of landed code: it
// is the server-side counterpart of PR #622, which is still open, so main today
// carries only snapToCodePoint. Read it as a sibling of that fix rather than a
// dependency on it — nothing here needs #622 to be true, and the tests stand on
// their own.
//
// One rule is deliberately stricter here than on the frontend, because the caller
// is different: a frontend selection carries a position, so a repeated quote can
// be disambiguated by "the one the mouse was on". An agent's quote carries no such
// hint, so several matches with nothing to choose between them is an error the
// caller has to fix, never a silent pick of the first one.
package textanchor

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// ContextLength is how much neighbouring text an anchor keeps either side of the
// quote, in runes. It matches CONTEXT_LENGTH in web/src/lib/doc-comments/anchor.ts
// so that an anchor built here and one built there are the same size of object.
const ContextLength = 48

// maxVerbatimCandidates and maxTolerantCandidates bound the scans.
//
// A pathological body — the same short phrase thousands of times — must not turn
// one comment into a quadratic walk. The numbers are the frontend's. Hitting
// either cap only ever makes an ambiguous quote look less ambiguous than it is,
// which is why AmbiguousError says so (AtLeast).
const (
	maxVerbatimCandidates = 500
	maxTolerantCandidates = 200
)

// tolerantScanMaxBody is the size above which the markup-tolerant fallback is
// skipped. Same value and same reason as TOLERANT_SCAN_MAX_SOURCE on the
// frontend: the fallback is O(body × quote), and a 5 MiB body would pay it on a
// quote that is simply not there.
const tolerantScanMaxBody = 200_000

// maxSkipRun is the longest run of markdown-only characters allowed between two
// characters of the quote. Enough for "**", a short backtick run or a link tail;
// short enough that the scan cannot wander across a paragraph and call it a match.
const maxSkipRun = 48

// graphemeWindow is how far back the boundary check resynchronises before
// segmenting.
//
// Grapheme rules are local, and this mirrors the frontend's WINDOW: segmenting a
// whole body to place one offset would be paid on every candidate of every
// comment. The honest limit: a cluster whose classification depends on more than
// graphemeWindow bytes of preceding text — a run of 100+ regional-indicator flags
// with no other character in it — can be misjudged. No document here contains one,
// and the failure mode is a rejected candidate, not an accepted wrong one.
const graphemeWindow = 256

// Span is a half-open [Start, End) range of UTF-8 byte offsets into a body.
type Span struct {
	Start int
	End   int
}

// Anchor is what a comment stores: the quote, its neighbours, and its position.
//
// Exact is the quote AS THE CALLER GAVE IT, which for a quote spanning inline
// markup is not body[Start:End] — the raw slice carries the markup ("**revert the
// image** first") and the quote is the text a reader sees ("revert the image
// first"). That asymmetry is deliberate and load-bearing: the quote's job is to
// be findable again in what the reader is looking at. Anything checking an anchor
// must use SpanMatchesQuote rather than string equality on the slice.
type Anchor struct {
	Exact  string
	Prefix string
	Suffix string
	Start  int
	End    int
}

// Context is what a caller offers to disambiguate a quote that occurs more than
// once: either the text either side of it, or one surrounding fragment that
// contains it.
//
// Both forms mean the same thing and only one may be given — a caller supplying
// both is describing two neighbourhoods, and picking one of them silently is how
// a caller comes to believe a constraint was applied that was not.
type Context struct {
	Prefix   string
	Suffix   string
	Fragment string
}

// IsZero reports whether no context was offered at all.
func (c Context) IsZero() bool {
	return c.Prefix == "" && c.Suffix == "" && c.Fragment == ""
}

// ErrEmptyQuote is returned for a quote with nothing in it. An empty needle
// matches at every position in the document, so it is not a degenerate search —
// it is a request that has to be refused.
var ErrEmptyQuote = errors.New("quote is empty")

// NotFoundError reports a quote that is not in the body at all.
type NotFoundError struct {
	Quote string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no such quote in the document: %q", truncateForMessage(e.Quote))
}

// AmbiguousError reports a quote that occurs more than once with nothing to
// choose between the occurrences.
//
// It carries the count because that is the number the caller needs in order to
// act: "this appears 4 times, narrow it" is a fixable answer, "ambiguous" is not.
type AmbiguousError struct {
	Quote   string
	Matches int
	// AtLeast is set when the scan stopped at its candidate cap, so Matches is a
	// floor rather than a total. Saying "4" when the truth is "thousands" would be
	// a number the caller cannot act on and cannot tell is wrong.
	AtLeast bool
}

func (e *AmbiguousError) Error() string {
	count := fmt.Sprintf("%d times", e.Matches)
	if e.AtLeast {
		count = fmt.Sprintf("at least %d times", e.Matches)
	}
	return fmt.Sprintf("the quote %q occurs %s in the document; narrow it with surrounding context",
		truncateForMessage(e.Quote), count)
}

// ContextError reports a context that cannot be used: both forms given at once,
// or a fragment that does not contain the quote it is supposed to surround.
type ContextError struct {
	Reason string
}

func (e *ContextError) Error() string { return e.Reason }

// truncateForMessage keeps an error message readable when the quote is long, and
// cuts on a rune boundary so the message itself is not mojibake.
func truncateForMessage(quote string) string {
	const limit = 60
	if len(quote) <= limit {
		return quote
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(quote[cut]) {
		cut--
	}
	return quote[:cut] + "…"
}

// Neighbours returns the prefix and suffix this context specifies for quote.
func (c Context) Neighbours(quote string) (prefix, suffix string, err error) {
	hasSides := c.Prefix != "" || c.Suffix != ""
	if c.Fragment != "" && hasSides {
		return "", "", &ContextError{
			Reason: "give either a surrounding fragment or a prefix/suffix pair, not both",
		}
	}
	if c.Fragment == "" {
		return c.Prefix, c.Suffix, nil
	}

	at := strings.Index(c.Fragment, quote)
	if at < 0 {
		return "", "", &ContextError{
			Reason: "the surrounding fragment does not contain the quote",
		}
	}
	return c.Fragment[:at], c.Fragment[at+len(quote):], nil
}

// FindQuote returns every place in body where quote sits, as byte spans.
//
// This is the primitive the two callers share. Verbatim occurrences first,
// because that is the case for all plain prose and it is exact; the
// markup-tolerant scan runs only when there are none, for a quote that was taken
// from rendered text and crossed a bold run, a link or a soft line break.
//
// Candidates whose ends do not fall on grapheme-cluster boundaries are dropped —
// see IsGraphemeBoundary for why that is a false match rather than an
// off-by-one.
func FindQuote(body, quote string) []Span {
	if quote == "" {
		return nil
	}
	if spans := verbatimSpans(body, quote); len(spans) > 0 {
		return spans
	}
	if len(body) > tolerantScanMaxBody {
		return nil
	}
	return tolerantSpans(body, quote)
}

// Locate picks the one span quote refers to, or says why it cannot.
//
// Zero matches and several-with-nothing-to-choose-between-them are both errors,
// and deliberately different ones: the first means the caller is quoting text
// that is not there, the second means the caller has to say which one. Neither
// resolves to "the first occurrence" — a comment landing on the wrong instance of
// a repeated phrase is exactly as wrong as one landing on the wrong document, and
// far harder to notice.
func Locate(body, quote string, cc Context) (Span, error) {
	if quote == "" {
		return Span{}, ErrEmptyQuote
	}

	prefix, suffix, err := cc.Neighbours(quote)
	if err != nil {
		return Span{}, err
	}

	spans := FindQuote(body, quote)
	switch len(spans) {
	case 0:
		return Span{}, &NotFoundError{Quote: quote}
	case 1:
		return spans[0], nil
	}

	ambiguous := &AmbiguousError{
		Quote:   quote,
		Matches: len(spans),
		AtLeast: len(spans) >= maxTolerantCandidates,
	}
	if prefix == "" && suffix == "" {
		return Span{}, ambiguous
	}

	// Context decides between repeats — the frontend's contextScore, which gives
	// partial credit so that a neighbourhood that survived a small edit still
	// counts for more than one that never matched.
	best, bestScore, tied := Span{}, 0, 0
	for _, span := range spans {
		score := contextScore(body, span, prefix, suffix)
		switch {
		case score > bestScore:
			best, bestScore, tied = span, score, 1
		case score == bestScore:
			tied++
		}
	}
	// A zero best score means the context agreed with none of them; a tie means it
	// agreed with several equally. Both leave the choice unmade, and making it
	// anyway is the silent-wrong-text bug this package exists to prevent.
	if bestScore == 0 || tied > 1 {
		return Span{}, ambiguous
	}
	return best, nil
}

// Resolve builds the complete anchor for quote, with its neighbours read out of
// the body.
//
// Prefix and Suffix come from the raw markdown rather than from a rendered
// projection, because rendering markdown is not this package's job and a server
// that guessed at it would be a second renderer to keep in step with the real
// one. In plain prose — nearly all of it — the two are the same characters; where
// they differ, the source form is still a correct neighbourhood of the same
// position, and it is Exact that carries identity.
func Resolve(body, quote string, cc Context) (Anchor, error) {
	span, err := Locate(body, quote, cc)
	if err != nil {
		return Anchor{}, err
	}
	return Anchor{
		Exact:  quote,
		Prefix: runesBefore(body, span.Start, ContextLength),
		Suffix: runesAfter(body, span.End, ContextLength),
		Start:  span.Start,
		End:    span.End,
	}, nil
}

// SpanMatchesQuote reports whether [span.Start, span.End) in body really is where
// quote sits — the guard direction of the same primitive.
//
// It is deliberately NOT body[Start:End] == quote. That equality is false for
// every legitimate anchor whose selection crossed inline markup, because the raw
// slice keeps the markup and the quote does not. It is also deliberately not a
// UTF-8 validity check: a character offset almost always lands on a valid UTF-8
// boundary too, just not on the right one, so a check that only looks at the
// encoding passes the exact bug it was written to catch. This ties the offsets to
// the quote.
func SpanMatchesQuote(body string, span Span, quote string) bool {
	if quote == "" || span.Start < 0 || span.End <= span.Start || span.End > len(body) {
		return false
	}
	if !IsGraphemeBoundary(body, span.Start) || !IsGraphemeBoundary(body, span.End) {
		return false
	}
	if body[span.Start:span.End] == quote {
		return true
	}
	return tolerantMatchAt(body, quote, span.Start) == span.End
}

// IsGraphemeBoundary reports whether offset falls between two grapheme clusters
// of body.
//
// A grapheme cluster is what a reader calls a character: "é" written as e + U+0301
// is two code points and one of them, and 👩‍👧 is three. An offset inside one is a
// perfectly valid UTF-8 boundary that nonetheless cuts a character in half — a
// range starting there begins with a bare combining acute, which renders attached
// to whatever precedes it, or with a zero-width joiner and half a family.
//
// The frontend meets this as an offset arriving from arithmetic and snaps it back
// (snapToGrapheme, proposed in PR #622 — open at the time of writing; main still
// has only the code-point snap). Here the offset arrives from a match, so the
// same fact means something stronger: a match starting mid-cluster is not an
// off-by-one to be nudged, it is a hit on text the caller did not quote. Callers
// therefore drop such candidates instead of moving them.
//
// uniseg rather than a hand-rolled rule for the reason #622 gives about
// Intl.Segmenter: the rules are Unicode data, and a wrong grapheme rule is harder
// to notice than a missing one.
func IsGraphemeBoundary(body string, offset int) bool {
	if offset <= 0 {
		return offset == 0
	}
	if offset >= len(body) {
		return offset == len(body)
	}
	if !utf8.RuneStart(body[offset]) {
		return false
	}

	from := offset - graphemeWindow
	if from < 0 {
		from = 0
	}
	for from < offset && !utf8.RuneStart(body[from]) {
		from++
	}

	pos, rest, state := from, body[from:], -1
	for pos < offset && rest != "" {
		var cluster string
		cluster, rest, _, state = uniseg.StepString(rest, state)
		pos += len(cluster)
	}
	return pos == offset
}

// verbatimSpans returns every literal occurrence of quote in body, overlapping
// ones included, capped.
func verbatimSpans(body, quote string) []Span {
	var spans []Span
	for from := 0; from+len(quote) <= len(body); {
		at := strings.Index(body[from:], quote)
		if at < 0 {
			break
		}
		start := from + at
		span := Span{Start: start, End: start + len(quote)}
		if onGraphemeBoundaries(body, span) {
			spans = append(spans, span)
			if len(spans) >= maxVerbatimCandidates {
				break
			}
		}
		from = start + 1
	}
	return spans
}

// tolerantSpans finds the quote in a body that carries markdown syntax the quote
// does not — a selection made in rendered text, matched against its source.
func tolerantSpans(body, quote string) []Span {
	first, _ := utf8.DecodeRuneInString(quote)

	var spans []Span
	for start := 0; start < len(body); {
		r, size := utf8.DecodeRuneInString(body[start:])
		// Only start where the first rune could match — literally, or as the head
		// of a whitespace run.
		if r == first || (unicode.IsSpace(first) && unicode.IsSpace(r)) {
			if end := tolerantMatchAt(body, quote, start); end >= 0 {
				span := Span{Start: start, End: end}
				if onGraphemeBoundaries(body, span) {
					spans = append(spans, span)
					if len(spans) >= maxTolerantCandidates {
						break
					}
				}
			}
		}
		start += size
	}
	return spans
}

// markupBytes are the ASCII characters the markdown may carry that the rendered
// text does not. Skipping them is what lets a selection of "the bold word" match
// "the **bold** word".
var markupBytes = map[byte]bool{
	'*': true, '_': true, '`': true, '~': true, '\\': true,
	'[': true, '#': true, '>': true, '|': true,
}

// linkTail is a link's tail — "](https://example.com \"title\")" — skipped as one
// unit, because its innards are not markup characters and would otherwise stop
// the scan dead.
var linkTail = regexp.MustCompile(`^\]\([^()\s]*(?:\s+"[^"]*")?\)`)

// tolerantMatchAt tries to match quote starting exactly at from in body, allowing
// body to carry markdown syntax the quote does not and allowing whitespace runs
// to differ in length — a markdown soft line break renders as one space.
//
// Returns the end byte offset in body, or -1.
func tolerantMatchAt(body, quote string, from int) int {
	i, j := 0, from
	for i < len(quote) {
		if j >= len(body) {
			return -1
		}

		qr, qsize := utf8.DecodeRuneInString(quote[i:])
		br, bsize := utf8.DecodeRuneInString(body[j:])

		if qr == br {
			i += qsize
			j += bsize
			continue
		}

		// Whitespace is compared as runs: any run matches any run.
		if unicode.IsSpace(qr) && unicode.IsSpace(br) {
			i = skipSpace(quote, i)
			j = skipSpace(body, j)
			continue
		}

		// Skip a run of body-only markdown before giving up on this start.
		skipped := 0
		for j < len(body) && skipped < maxSkipRun {
			if tail := linkTail.FindStringIndex(body[j:]); tail != nil {
				j += tail[1]
				skipped += tail[1]
				continue
			}
			if !markupBytes[body[j]] {
				break
			}
			j++
			skipped++
		}
		if skipped == 0 {
			return -1
		}
	}
	return j
}

// skipSpace advances past a run of whitespace starting at i.
func skipSpace(s string, i int) int {
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	return i
}

// contextScore is how well the text either side of span agrees with the context
// the caller gave. Higher is better; zero means nothing around it agrees.
//
// Partial credit — the length of the agreeing run rather than a yes/no — is the
// frontend's rule, and it is what keeps a neighbourhood that survived a small
// edit ahead of one that never matched at all.
//
// One thing is counted differently here than on the frontend, and it is the
// difference between a hint and a decision. An agreement made ENTIRELY of
// whitespace is worth nothing: every sentence in prose ends with a space, so a
// shared trailing space is a coincidence every candidate in the document
// supplies. The frontend can afford to count it, because it also holds the
// position the mouse was at and only uses the score to break ties. Here the score
// IS the decision, and letting one space decide it would be picking an occurrence
// at random while reporting confidence. Measured on "раз здесь. раз здесь." with
// a context that matches neither: unfiltered, the shared space picked the second
// one.
func contextScore(body string, span Span, prefix, suffix string) int {
	beforeFrom := span.Start - len(prefix)
	if beforeFrom < 0 {
		beforeFrom = 0
	}
	afterTo := span.End + len(suffix)
	if afterTo > len(body) {
		afterTo = len(body)
	}
	return substantive(commonSuffix(body[beforeFrom:span.Start], prefix)) +
		substantive(commonPrefix(body[span.End:afterTo], suffix))
}

// substantive is the length in runes of an agreeing run, or 0 when the run says
// nothing — see contextScore.
func substantive(agreed string) int {
	if strings.TrimSpace(agreed) == "" {
		return 0
	}
	return utf8.RuneCountInString(agreed)
}

// commonSuffix is the longest run of whole runes a and b end with in common.
func commonSuffix(a, b string) string {
	n := 0
	for len(a)-n > 0 && len(b)-n > 0 {
		ar, asize := utf8.DecodeLastRuneInString(a[:len(a)-n])
		br, _ := utf8.DecodeLastRuneInString(b[:len(b)-n])
		if ar != br {
			break
		}
		n += asize
	}
	return a[len(a)-n:]
}

// commonPrefix is the longest run of whole runes a and b start with in common.
func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) {
		ar, asize := utf8.DecodeRuneInString(a[n:])
		br, _ := utf8.DecodeRuneInString(b[n:])
		if ar != br {
			break
		}
		n += asize
	}
	return a[:n]
}

// onGraphemeBoundaries reports whether both ends of span sit between clusters.
func onGraphemeBoundaries(body string, span Span) bool {
	return IsGraphemeBoundary(body, span.Start) && IsGraphemeBoundary(body, span.End)
}

// runesBefore returns up to n runes of body ending at offset.
func runesBefore(body string, offset, n int) string {
	start := offset
	for i := 0; i < n && start > 0; i++ {
		_, size := utf8.DecodeLastRuneInString(body[:start])
		start -= size
	}
	return body[start:offset]
}

// runesAfter returns up to n runes of body starting at offset.
func runesAfter(body string, offset, n int) string {
	end := offset
	for i := 0; i < n && end < len(body); i++ {
		_, size := utf8.DecodeRuneInString(body[end:])
		end += size
	}
	return body[offset:end]
}
