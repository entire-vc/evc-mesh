package mdoc

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Resolving a quotation to a comment anchor, server-side.
//
// ## Why this is not the caller's job
//
// A document comment's anchor is a pair of byte offsets into the markdown plus
// the quote and its neighbours. The web client computes them from a mouse
// selection, where the offsets fall out of the selection itself. An agent has no
// selection: it has a sentence, and it would have to compute the offsets from the
// text — which is exactly where it gets them wrong. Measured on 2026-08-19: a
// Cyrillic quote sitting at byte 853 is reported at 475 by a naive character
// index, because Go, Postgres and this API count bytes while a character count
// counts characters, and Russian is two bytes a letter. An anchor computed that
// way points confidently at somebody else's sentence.
//
// ## Why it mirrors the frontend rather than inventing its own rules
//
// The resolution rules already exist, in web/src/lib/doc-comments/anchor.ts, and
// two independent implementations of "find this quote" drift. When they drift, an
// agent's comment and a human's comment on the same sentence land in different
// places, and the disagreement is invisible until somebody notices two
// highlights where there should be one. So the scan order (verbatim first, then
// markup-tolerant), the scoring (how much of the remembered context agrees) and
// every constant below are the same as that file's, and
// TestResolveQuote_MatchesFrontendFixture pins them to values produced by
// running the real TypeScript.
//
// ## The one deliberate difference
//
// The frontend, faced with several equally good matches, picks one: it has a
// hint — where the selection actually was — and using it is strictly better than
// refusing to place a highlight the reader just made. The server has no hint, so
// where the frontend tie-breaks, this refuses and says how many matches there
// were. Silently picking the first occurrence of "the API" out of eleven is the
// one failure mode that produces a confidently wrong anchor with nothing to
// notice, and an agent can always send more context.

const (
	// contextLength is how much neighbouring text an anchor keeps either side of
	// the quote. Same value as CONTEXT_LENGTH in anchor.ts, counted in the same
	// units (code points; identical to its UTF-16 units for everything below the
	// astral planes, which is all prose).
	contextLength = 48

	// maxQuoteBytes is the longest quote this will resolve. The API's anchor
	// column refuses more.
	maxQuoteBytes = 2000

	// tolerantScanMaxSource is the size above which the markup-tolerant fallback
	// is skipped — it is O(source x quote) in the worst case. Same as
	// TOLERANT_SCAN_MAX_SOURCE.
	tolerantScanMaxSource = 200_000

	// maxSkipRun is the longest run of markdown-only characters allowed between
	// two characters of the quote: enough for `**`, a backtick run or a short link
	// tail, short enough that the scan cannot wander across a paragraph and call
	// it a match. Same as MAX_SKIP_RUN.
	maxSkipRun = 48

	// maxCandidates and maxTolerantCandidates bound a pathological document — the
	// same phrase thousands of times — from turning one request into a quadratic
	// scan. Same as the caps in allOccurrences and locateQuoteInSource.
	maxCandidates         = 500
	maxTolerantCandidates = 200
)

// markupChars are the characters the markdown may carry that the rendered text
// does not. Skipping them is what lets a quote of "the bold word" match
// `the **bold** word`. Same set as MARKUP_CHARS.
var markupChars = map[byte]bool{
	'*': true, '_': true, '`': true, '~': true, '\\': true,
	'[': true, '#': true, '>': true, '|': true,
}

// linkTail matches a link's tail — `](https://example.com "title")` — which is
// skipped as one unit. Same expression as LINK_TAIL.
var linkTail = regexp.MustCompile(`^\]\([^()\s]*(?:\s+"[^"]*")?\)`)

// Anchor is a resolved comment anchor: a W3C Web Annotation selector pair, the
// shape document_comments stores.
//
// Start and End are BYTE offsets into the markdown, half-open [Start, End) —
// the units the anchor_start/anchor_end columns are documented in. Slicing the
// body with them is the whole point, and it is what the Cyrillic test asserts.
type Anchor struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

// QuoteNotFoundError reports a quote that does not occur in the document.
type QuoteNotFoundError struct{ Quote string }

func (e *QuoteNotFoundError) Error() string {
	return "no such quote in this document: the text was not found, either verbatim or with " +
		"markdown syntax between its characters. Quote the document exactly as it reads, " +
		"including punctuation."
}

// AmbiguousQuoteError reports a quote that occurs more than once and could not be
// narrowed to one by the context supplied. Matches is how many times it occurs,
// which is the number that tells a caller whether to add context or pick a
// different sentence.
type AmbiguousQuoteError struct {
	Quote   string
	Matches int
}

func (e *AmbiguousQuoteError) Error() string {
	return fmt.Sprintf("this quote occurs %d times in the document; send the text immediately "+
		"before it as prefix and/or after it as suffix so exactly one of them can be identified",
		e.Matches)
}

// EmptyQuoteError reports a quote that is blank once trimmed.
type EmptyQuoteError struct{}

func (e *EmptyQuoteError) Error() string { return "quote is empty" }

// QuoteTooLongError reports a quote past the anchor column's limit.
type QuoteTooLongError struct{ Bytes int }

func (e *QuoteTooLongError) Error() string {
	return fmt.Sprintf("quote is %d bytes; at most %d are stored. A quote is an identifier, not a "+
		"copy of the document — one sentence is enough", e.Bytes, maxQuoteBytes)
}

// ResolveQuote finds a quotation in a document's markdown and returns the anchor
// for it.
//
// prefix and suffix are optional and are used only to disambiguate: they are the
// text the caller saw immediately before and after the quote. They are not
// echoed back — the anchor's Prefix and Suffix are taken from the document at
// the match, because the anchor's job is to describe where the text is in this
// document, not to record what the caller believed. A caller that sent a slightly
// wrong neighbourhood still gets an anchor that will re-find itself later.
//
// It is refused rather than guessed at when the quote occurs more than once and
// the context does not settle it. See the note at the top of this file.
func ResolveQuote(source, quote, prefix, suffix string) (*Anchor, error) {
	exact := strings.TrimSpace(quote)
	if exact == "" {
		return nil, &EmptyQuoteError{}
	}
	if len(exact) > maxQuoteBytes {
		return nil, &QuoteTooLongError{Bytes: len(exact)}
	}

	candidates := verbatimOccurrences(source, exact)
	if len(candidates) == 0 && len(source) <= tolerantScanMaxSource {
		// The fallback for a quote taken from the rendered page: its characters
		// exist in the markdown with syntax interleaved.
		candidates = tolerantOccurrences(source, exact)
	}

	switch len(candidates) {
	case 0:
		return nil, &QuoteNotFoundError{Quote: exact}
	case 1:
		return anchorAt(source, exact, candidates[0]), nil
	}

	// Several. Let the context narrow them, exactly the way the frontend scores
	// them — but require it to leave one standing rather than picking a winner.
	if prefix != "" || suffix != "" {
		if best, unique := bestByContext(source, candidates, prefix, suffix); unique {
			return anchorAt(source, exact, best), nil
		}
	}
	return nil, &AmbiguousQuoteError{Quote: exact, Matches: len(candidates)}
}

// span is a half-open byte range in the source.
type span struct{ start, end int }

// verbatimOccurrences returns every literal occurrence of needle. Byte offsets
// fall out of it directly, which is why this is the exact path and the tolerant
// scan below is the approximate one.
func verbatimOccurrences(source, needle string) []span {
	var spans []span
	from := 0
	for {
		at := strings.Index(source[from:], needle)
		if at < 0 {
			break
		}
		start := from + at
		spans = append(spans, span{start, start + len(needle)})
		from = start + 1
		if len(spans) >= maxCandidates {
			break
		}
	}
	return spans
}

// tolerantOccurrences finds the needle allowing the source to carry markdown
// syntax the needle does not, and allowing whitespace runs to differ (a markdown
// soft line break renders as one space).
//
// Ported from locateQuoteInSource's fallback scan. It works in runes rather than
// UTF-16 code units, which agrees with the TypeScript for every character in the
// Basic Multilingual Plane — all prose, Cyrillic included. The two would count an
// astral-plane character (an emoji) differently, and the only thing that count
// feeds is maxSkipRun, a limit on how much markdown may sit between two letters
// of a quote; an emoji is not markdown syntax and does not appear in such a run.
func tolerantOccurrences(source, needle string) []span {
	src, srcOffsets := runesWithOffsets(source)
	ndl := []rune(needle)
	if len(ndl) == 0 {
		return nil
	}

	var spans []span
	first := ndl[0]
	for start := 0; start < len(src); start++ {
		// Only start where the first character could match — literally, or as the
		// head of a whitespace run.
		if src[start] != first && (!unicode.IsSpace(first) || !unicode.IsSpace(src[start])) {
			continue
		}
		if end, ok := tolerantMatchAt(source, src, srcOffsets, ndl, start); ok {
			spans = append(spans, span{srcOffsets[start], end})
			if len(spans) >= maxTolerantCandidates {
				break
			}
		}
	}
	return spans
}

// tolerantMatchAt tries to match needle starting exactly at rune index from.
// Returns the BYTE offset one past the match.
func tolerantMatchAt(source string, src []rune, offsets []int, needle []rune, from int) (int, bool) {
	i, j := 0, from

	for i < len(needle) {
		if j >= len(src) {
			return 0, false
		}

		nc, sc := needle[i], src[j]
		if nc == sc {
			i++
			j++
			continue
		}

		// Whitespace is compared as runs: any run matches any run.
		if unicode.IsSpace(nc) && unicode.IsSpace(sc) {
			for i < len(needle) && unicode.IsSpace(needle[i]) {
				i++
			}
			for j < len(src) && unicode.IsSpace(src[j]) {
				j++
			}
			continue
		}

		// Skip a run of source-only markdown before giving up on this start.
		skipped := 0
		for j < len(src) && skipped < maxSkipRun {
			if tail := linkTail.FindString(source[offsets[j]:]); tail != "" {
				n := len([]rune(tail))
				j += n
				skipped += n
				continue
			}
			r := src[j]
			if r > 0x7f || !markupChars[byte(r)] {
				break
			}
			j++
			skipped++
		}
		if skipped == 0 {
			return 0, false
		}
	}

	return offsets[j], true
}

// runesWithOffsets decodes the source once into runes and the byte offset each
// rune starts at, with a final entry for the end of the string so that offsets[j]
// is valid for j == len(runes).
func runesWithOffsets(s string) (runes []rune, offsets []int) {
	runes = make([]rune, 0, len(s))
	offsets = make([]int, 0, len(s)+1)
	for i, r := range s {
		runes = append(runes, r)
		offsets = append(offsets, i)
	}
	offsets = append(offsets, len(s))
	return runes, offsets
}

// bestByContext scores each candidate on how much of the supplied neighbourhood
// agrees with the document around it, and reports the best one — and whether it
// was the outright best. A tie is not a winner here, which is the difference
// between this and the frontend's bestCandidate.
func bestByContext(source string, candidates []span, prefix, suffix string) (span, bool) {
	best := candidates[0]
	bestScore := -1
	tied := false

	for _, c := range candidates {
		score := contextScore(source, c, prefix, suffix)
		switch {
		case score > bestScore:
			best, bestScore, tied = c, score, false
		case score == bestScore:
			tied = true
		}
	}
	// A best score of zero means "the quote is here but nothing around it agrees"
	// for every candidate, which is not a match on context at all.
	return best, !tied && bestScore > 0
}

// contextScore is how well the document around a candidate agrees with the
// remembered neighbourhood: the longest common suffix of what precedes it with
// prefix, plus the longest common prefix of what follows it with suffix. Same
// measure as contextScore in anchor.ts.
func contextScore(source string, c span, prefix, suffix string) int {
	before := runesBefore(source, c.start, len([]rune(prefix)))
	after := runesAfter(source, c.end, len([]rune(suffix)))
	return commonSuffixLen(before, prefix) + commonPrefixLen(after, suffix)
}

// runesBefore returns up to n runes of source ending at byte offset at.
func runesBefore(source string, at, n int) string {
	if n <= 0 || at <= 0 {
		return ""
	}
	head := []rune(source[:at])
	if len(head) > n {
		head = head[len(head)-n:]
	}
	return string(head)
}

// runesAfter returns up to n runes of source starting at byte offset at.
func runesAfter(source string, at, n int) string {
	if n <= 0 || at >= len(source) {
		return ""
	}
	tail := []rune(source[at:])
	if len(tail) > n {
		tail = tail[:n]
	}
	return string(tail)
}

func commonSuffixLen(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[len(ra)-1-n] == rb[len(rb)-1-n] {
		n++
	}
	return n
}

func commonPrefixLen(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return n
}

// anchorAt builds the anchor for a located span, taking the neighbourhood from
// the document rather than from the caller.
func anchorAt(source, exact string, at span) *Anchor {
	return &Anchor{
		Exact:  exact,
		Prefix: runesBefore(source, at.start, contextLength),
		Suffix: runesAfter(source, at.end, contextLength),
		Start:  at.start,
		End:    at.end,
	}
}
