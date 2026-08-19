// Package mdoc reads a markdown document body: its heading outline, the section
// under a heading, and where a quotation sits in it.
//
// It is a package rather than a handful of helpers in the service because every
// function here is a pure function of the markdown, with no repository, no
// storage and no request in sight — which is what makes them cheap to test
// exhaustively against the traps that actually bite (a `#` inside a code fence,
// a Cyrillic quote's byte offsets) rather than only against the happy path.
//
// Nothing here is stored. An outline is computed from the body on every read: a
// stored one is a second copy of the document's structure that drifts from the
// document the first time anyone edits a heading, and a stale table of contents
// is worse than none because it looks authoritative.
package mdoc

import (
	"fmt"
	"strings"
	"unicode"
)

// Heading is one entry of a document's outline.
//
// Start and End are BYTE offsets into the markdown, half-open [Start, End) —
// the same convention as document_comments.anchor_start and memory_chunks, so a
// caller can slice the body with them directly and does not have to know which
// of the two counting schemes this particular field uses.
type Heading struct {
	// Level is 1 for `#` through 6 for `######`.
	Level int `json:"level"`
	// Text is the heading as written, with the `#` markers and any closing run of
	// them removed. Inline markup is left in: it is what the document says.
	Text string `json:"text"`
	// Anchor addresses this heading uniquely within the document — see anchorize
	// for the scheme and what happens to duplicates.
	Anchor string `json:"anchor"`
	// Line is 1-based, for a human reading an error message.
	Line int `json:"line"`
	// Start is the byte offset of the first character of the heading line.
	Start int `json:"start"`
	// End is the byte offset one past the section this heading owns: everything
	// down to the next heading of the same or a higher level, or the end of the
	// document. Subsections are inside it; the next sibling is not.
	End int `json:"end"`
}

// Outline parses the ATX headings of a markdown document, in document order.
//
// ATX only (`# Heading`), not setext (`Heading` underlined with `===` or `---`).
// Setext's `---` is indistinguishable from a thematic break and from the closing
// fence of YAML frontmatter without deciding whether the line above is a
// paragraph, and every document this serves is written by an editor that emits
// ATX. Guessing wrong puts a phantom heading in the outline, which is the exact
// failure this function exists to avoid.
//
// Returns an empty slice, never nil, so a caller marshalling it gets `[]` rather
// than `null`.
func Outline(source string) []Heading {
	headings := make([]Heading, 0, 8)
	seen := make(map[string]int)

	for _, ln := range scanLines(source) {
		if ln.skip {
			continue
		}
		level, text, ok := parseATX(ln.text)
		if !ok {
			continue
		}
		headings = append(headings, Heading{
			Level:  level,
			Text:   text,
			Anchor: uniqueAnchor(text, seen),
			Line:   ln.number,
			Start:  ln.start,
		})
	}

	// A heading owns everything down to the next heading of the same or a higher
	// level. "Or higher" is what puts subsections inside their parent and keeps
	// the next sibling out: `## Setup` ends at the next `##` or the next `#`, and
	// a `###` inside it is part of it.
	for i := range headings {
		headings[i].End = len(source)
		for j := i + 1; j < len(headings); j++ {
			if headings[j].Level <= headings[i].Level {
				headings[i].End = headings[j].Start
				break
			}
		}
	}

	return headings
}

// line is one line of the source with the offsets and the parser state a heading
// scan needs.
type line struct {
	text   string
	start  int // byte offset of the first character
	number int // 1-based
	// skip is set for a line that cannot be a heading no matter what it looks
	// like: inside a fenced code block, or inside YAML frontmatter.
	skip bool
}

// scanLines splits the source into lines, marking the ones a heading cannot
// occur on.
//
// The two states it tracks are the two ways a `#` at the start of a line is not
// a heading, and both are real here rather than theoretical:
//
//   - A fenced code block. Every runbook in this system contains shell, and
//     `# rebuild the index` is a comment in it. A line-by-line scan with no fence
//     state puts that in the table of contents.
//   - YAML frontmatter. Every document in this workspace is required to carry it,
//     and `#` is YAML's comment marker, so a commented-out field at the top of the
//     file would become the document's first heading.
func scanLines(source string) []line {
	raw := strings.Split(source, "\n")
	lines := make([]line, 0, len(raw))

	offset := 0
	// fenceChar is 0 when not inside a fence; otherwise '`' or '~'.
	var fenceChar byte
	fenceLen := 0
	// Frontmatter only when the document opens with the fence, on line 1.
	inFrontmatter := len(raw) > 0 && trimCR(raw[0]) == "---"

	for i, r := range raw {
		text := trimCR(r)
		l := line{text: text, start: offset, number: i + 1}
		offset += len(r) + 1 // the \n that Split consumed

		switch {
		case inFrontmatter:
			l.skip = true
			// The opening --- is line 1 and does not close anything. YAML allows
			// either terminator.
			if i > 0 && (text == "---" || text == "...") {
				inFrontmatter = false
			}

		case fenceChar != 0:
			l.skip = true
			if closesFence(text, fenceChar, fenceLen) {
				fenceChar, fenceLen = 0, 0
			}

		default:
			if ch, n, ok := opensFence(text); ok {
				fenceChar, fenceLen = ch, n
				l.skip = true
			}
		}

		lines = append(lines, l)
	}

	return lines
}

func trimCR(s string) string { return strings.TrimSuffix(s, "\r") }

// leadingSpaces counts the indent, which decides whether a marker is markup at
// all: CommonMark allows a fence or an ATX heading up to three spaces in, and
// treats four as the start of an indented code block.
func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// opensFence reports whether the line opens a fenced code block, and with which
// character and how many of them.
func opensFence(text string) (fence byte, length int, ok bool) {
	indent := leadingSpaces(text)
	if indent > 3 {
		return 0, 0, false
	}
	rest := text[indent:]
	if rest == "" {
		return 0, 0, false
	}
	ch := rest[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(rest) && rest[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	// A backtick fence's info string may not itself contain a backtick — that is
	// what keeps inline code (`` `a` `` in a sentence) from opening a block.
	if ch == '`' && strings.ContainsRune(rest[n:], '`') {
		return 0, 0, false
	}
	return ch, n, true
}

// closesFence reports whether the line is a closing fence for an open one: the
// same character, at least as long, and nothing after it.
func closesFence(text string, ch byte, openLen int) bool {
	indent := leadingSpaces(text)
	if indent > 3 {
		return false
	}
	rest := text[indent:]
	n := 0
	for n < len(rest) && rest[n] == ch {
		n++
	}
	if n < openLen {
		return false
	}
	return strings.TrimSpace(rest[n:]) == ""
}

// parseATX pulls the level and the text out of an ATX heading line.
//
// `#hashtag` is not a heading: CommonMark requires whitespace (or the end of the
// line) after the marker, and without that rule every `#tag` in prose becomes an
// h1.
func parseATX(text string) (level int, heading string, ok bool) {
	indent := leadingSpaces(text)
	if indent > 3 {
		return 0, "", false
	}
	rest := text[indent:]

	for level < len(rest) && rest[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return 0, "", false
	}
	if level < len(rest) && rest[level] != ' ' && rest[level] != '\t' {
		return 0, "", false
	}

	body := strings.TrimSpace(rest[level:])
	// An optional closing run of #, which must be separated from the text — so
	// `## C#` keeps its sharp and `## Heading ##` does not keep its markers.
	if trimmed := strings.TrimRight(body, "#"); trimmed != body {
		if trimmed == "" || strings.HasSuffix(trimmed, " ") || strings.HasSuffix(trimmed, "\t") {
			body = strings.TrimSpace(trimmed)
		}
	}
	return level, body, true
}

// anchorize turns heading text into a URL-shaped fragment, GitHub's scheme:
// lower-cased, everything that is not a letter, a digit, a space, a hyphen or an
// underscore dropped, spaces collapsed into hyphens.
//
// unicode.IsLetter and IsDigit rather than an ASCII range, deliberately. Half the
// documents here are written in Russian, and an ASCII-only slugifier reduces
// every one of their headings to the empty string — so every heading in the
// document would collide with every other and none of them would be addressable.
//
// Dropping punctuation is also what makes inline markup disappear on its own:
// `## The **bold** word` anchors as `the-bold-word` without a markdown parser.
func anchorize(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		// A heading made entirely of punctuation, or of emoji. It still has to be
		// addressable, and the duplicate counter below is what keeps several of
		// them apart.
		return "section"
	}
	return slug
}

// uniqueAnchor gives every heading its own anchor even when two of them say the
// same thing.
//
// The scheme is GitHub's: the first occurrence keeps the bare slug, the next gets
// `-1`, then `-2`. A document with two `## Rollback` sections addresses them as
// `rollback` and `rollback-1`, which is stable as long as their order is — and
// their order is the document's order, which is the only thing about a repeated
// heading that distinguishes it at all.
func uniqueAnchor(text string, seen map[string]int) string {
	base := anchorize(text)
	n := seen[base]
	seen[base] = n + 1
	if n == 0 {
		return base
	}
	// Guard against a document that already contains the disambiguated form:
	// `## Rollback`, `## Rollback`, `## Rollback 1` must not all want `rollback-1`.
	for {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if _, taken := seen[candidate]; !taken {
			seen[candidate] = 1
			return candidate
		}
		n++
		seen[base] = n + 1
	}
}
