// Package markdown extracts the outline of a markdown document: its headings,
// the anchors that address them, and the byte range of the section each one
// opens.
//
// It exists so that a caller can read one section of a document instead of the
// whole thing. A document is an order of magnitude larger than a task, and an
// agent that reads a whole page to quote one paragraph carries that page for the
// rest of its run.
//
// Nothing here is stored. The outline is derived from the body on every read, so
// it cannot disagree with the body it describes — a persisted table of contents
// goes stale on the first edit and does it silently, which is the worst way for
// an index to be wrong.
//
// This is deliberately not a markdown parser. It recognises ATX headings and the
// constructs that can hide one (fenced code, indented code, YAML frontmatter),
// and treats everything else as opaque text. That is the whole job: we are
// slicing a document, not rendering it.
package markdown

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Heading is one entry in a document's table of contents.
type Heading struct {
	// Level is 1..6, from the number of leading '#'.
	Level int `json:"level"`
	// Text is the heading as written, with the '#' run and any closing '#'
	// sequence removed. Inline markup is left alone.
	Text string `json:"text"`
	// Anchor addresses this heading and no other in the document.
	//
	// It is the slug of Text when that slug is unique. When several headings
	// share a slug, EVERY one of them is suffixed by its ordinal —
	// `linux-1`, `linux-2` — and the bare `linux` addresses none of them.
	//
	// The alternative, GitHub's, leaves the first occurrence unsuffixed. That
	// makes the bare slug both a valid anchor and an ambiguous name, so the
	// reference a caller would most naturally write silently resolves to
	// whichever heading happens to come first. Here it resolves to nothing and
	// the error names the anchors that work. See Anchorize.
	Anchor string `json:"anchor"`
	// Path is the chain of un-suffixed slugs from the outermost enclosing
	// heading down to this one. Two headings with the same text under
	// different parents have different Paths, which is what makes a repeated
	// heading addressable without knowing its ordinal.
	//
	// Two headings with the same text under the SAME parent share a Path.
	// That is not resolved by inventing a winner: Section refuses the
	// ambiguous reference and names the anchors that would resolve it.
	Path []string `json:"path"`
	// Line is the 1-based line number of the heading line.
	Line int `json:"line"`

	// start is the byte offset of the heading line in the body; end is the
	// offset just past the section it opens. Unexported: they are an artifact
	// of one particular body, and a caller holding them against a different
	// (or edited) body would slice nonsense.
	start int
	end   int
}

// PathString renders Path in the form Section accepts: `installation/linux`.
func (h Heading) PathString() string { return strings.Join(h.Path, "/") }

// atxRe matches an ATX heading line: up to three leading spaces, one to six
// '#', then either end-of-line or whitespace and the text.
//
// The whitespace is required by CommonMark and matters here: without it
// `#hashtag` and every `#123` issue reference in prose would open a section.
var atxRe = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)

// closingHashRe strips the optional closing '#' run: `## Title ##` is `Title`.
var closingHashRe = regexp.MustCompile(`[ \t]+#+[ \t]*$`)

// fenceOpenRe matches an opening code fence and captures the run of fence
// characters, whose kind and length decide what can close it.
var fenceOpenRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// hyphenRunRe collapses the hyphen runs Anchorize produces from punctuation and
// repeated spaces.
var hyphenRunRe = regexp.MustCompile(`-{2,}`)

// Anchorize turns heading text into an anchor slug.
//
// Letters and digits are kept — by Unicode category, not by an ASCII range.
// That distinction is the whole point: the ASCII-only slugifiers already in this
// repo (service.slugify, teamrelay.slugify) reduce a Cyrillic heading to the
// empty string, so every Russian heading in a document would collide on one
// anchor and none of them would be addressable. Anchors here are an addressing
// scheme, and an addressing scheme that only works for one alphabet is broken
// for the documents this fleet actually writes.
//
// Everything else becomes a hyphen, runs of hyphens collapse to one, and the
// result is trimmed. Underscores survive, as they do on GitHub.
//
// Unlike GitHub we collapse hyphen runs, so `Раздел  два` is `раздел-два` rather
// than `раздел--два`. These anchors address sections through this API; they are
// not a rendering contract with anyone else's slugger.
func Anchorize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(hyphenRunRe.ReplaceAllString(b.String(), "-"), "-")
}

// TOC returns the document's headings in document order.
//
// Computed every call. There is no cache and no stored copy — see the package
// comment for why that is a feature.
func TOC(body string) []Heading {
	headings := scan(body)
	// Second pass: a section ends where the next heading of the same or higher
	// rank begins. Anything in between — including every deeper heading — is
	// inside it, which is what makes subsections part of their parent.
	for i := range headings {
		headings[i].end = len(body)
		for j := i + 1; j < len(headings); j++ {
			if headings[j].Level <= headings[i].Level {
				headings[i].end = headings[j].start
				break
			}
		}
	}
	return headings
}

// scan walks the body line by line and collects the headings, skipping the
// constructs that can contain a line which merely looks like one.
func scan(body string) []Heading {
	var (
		out []Heading
		// open is the stack of enclosing headings, which yields Path.
		open []Heading

		inFence   bool
		fenceChar byte
		fenceLen  int
	)

	lines := splitLinesWithOffsets(body)
	start := 0

	// YAML frontmatter: a `---` on the very first line opens a metadata block
	// that is not document text. A `# comment` inside it is a YAML comment, and
	// promoting it to a heading would put the document's own metadata in its
	// table of contents.
	if len(lines) > 0 && strings.TrimRight(lines[0].text, " \t") == "---" {
		for i := 1; i < len(lines); i++ {
			t := strings.TrimRight(lines[i].text, " \t")
			if t == "---" || t == "..." {
				start = i + 1
				break
			}
		}
	}

	for i := start; i < len(lines); i++ {
		line := lines[i]

		if inFence {
			if isFenceClose(line.text, fenceChar, fenceLen) {
				inFence = false
			}
			continue
		}
		if m := fenceOpenRe.FindStringSubmatch(line.text); m != nil {
			// An info string containing a backtick does not open a backtick
			// fence (CommonMark 4.5) — without that rule an inline `a ` b`
			// would swallow the rest of the document.
			if m[1][0] == '`' && strings.Contains(m[2], "`") {
				continue
			}
			inFence, fenceChar, fenceLen = true, m[1][0], len(m[1])
			continue
		}

		// Four or more leading spaces is an indented code block, so the '#'
		// is code. atxRe allows at most three, so this is already handled —
		// stated here because it is the other place a heading can hide.
		m := atxRe.FindStringSubmatch(line.text)
		if m == nil {
			continue
		}

		text := strings.TrimSpace(closingHashRe.ReplaceAllString(m[2], ""))
		h := Heading{
			Level: len(m[1]),
			Text:  text,
			Line:  i + 1,
			start: line.offset,
		}

		base := Anchorize(text)
		if base == "" {
			// A heading of only punctuation or emoji slugifies to nothing.
			// Falling back to its position keeps it addressable rather than
			// silently unreachable; the ordinal is stable for a given body.
			base = "section-" + strconv.Itoa(len(out)+1)
		}

		for len(open) > 0 && open[len(open)-1].Level >= h.Level {
			open = open[:len(open)-1]
		}
		path := make([]string, 0, len(open)+1)
		for _, a := range open {
			path = append(path, a.Path[len(a.Path)-1])
		}
		path = append(path, base)
		h.Path = path

		open = append(open, h)
		out = append(out, h)
	}

	assignAnchors(out)
	return out
}

// assignAnchors gives every heading a reference that resolves to it alone.
//
// It runs after the whole body is scanned because whether a slug needs a suffix
// depends on headings that may come later — the decision cannot be made while
// walking forward.
func assignAnchors(hs []Heading) {
	counts := make(map[string]int, len(hs))
	for _, h := range hs {
		counts[baseOf(h)]++
	}

	ordinal := make(map[string]int, len(hs))
	used := make(map[string]bool, len(hs))
	for i := range hs {
		base := baseOf(hs[i])
		anchor := base
		if counts[base] > 1 {
			ordinal[base]++
			anchor = base + "-" + strconv.Itoa(ordinal[base])
		}
		// A suffixed anchor can still collide with a heading whose own text
		// slugifies to it — `Linux` twice alongside a literal `Linux 1`. Rare,
		// but a duplicate anchor would put us back to first-match, so keep
		// stepping until the reference is genuinely unique.
		for n := 2; used[anchor]; n++ {
			anchor = base + "-" + strconv.Itoa(ordinal[base]) + "-" + strconv.Itoa(n)
		}
		used[anchor] = true
		hs[i].Anchor = anchor
	}
}

func baseOf(h Heading) string { return h.Path[len(h.Path)-1] }

func isFenceClose(line string, char byte, minLen int) bool {
	t := strings.TrimRight(strings.TrimLeft(line, " "), " \t")
	if len(line)-len(strings.TrimLeft(line, " ")) > 3 || len(t) < minLen {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != char {
			return false
		}
	}
	return true
}

type lineSpan struct {
	text   string
	offset int
}

func splitLinesWithOffsets(body string) []lineSpan {
	var out []lineSpan
	off := 0
	for off <= len(body) {
		nl := strings.IndexByte(body[off:], '\n')
		if nl < 0 {
			out = append(out, lineSpan{text: body[off:], offset: off})
			break
		}
		out = append(out, lineSpan{text: body[off : off+nl], offset: off})
		off += nl + 1
	}
	return out
}

// NotFoundError is returned when a reference matches no heading.
type NotFoundError struct {
	Ref string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no section matches %q", e.Ref)
}

// AmbiguousError is returned when a reference matches more than one heading.
//
// This is a refusal, not a failure to try. The alternative — returning the first
// match — is the failure mode this type exists to prevent: the caller gets a
// plausible section, has no way to tell it was the wrong one, and the mistake
// only surfaces when somebody notices the quote does not match the document.
// Anchors carries the references that each resolve to exactly one heading.
type AmbiguousError struct {
	Ref     string
	Anchors []string
	Paths   []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches %d headings; address one of them directly: %s",
		e.Ref, len(e.Anchors), strings.Join(e.Anchors, ", "))
}

// Section returns the heading addressed by ref and the markdown it covers.
//
// The content runs from the heading line, inclusive, to the next heading of the
// same or higher rank, exclusive. Subsections are inside it; the sibling that
// follows is not.
//
// ref is either an anchor (`linux-1`) or a path of heading slugs
// (`installation/linux`). A path matches the tail of a heading's Path, so a
// caller can name just enough ancestors to be unambiguous instead of spelling
// out the chain from the root. Segments are slugified before comparison, so
// `Установка/Linux` and `установка/linux` are the same reference.
//
// A ref matching several headings returns *AmbiguousError with the anchors that
// resolve it, never a first match.
func Section(body, ref string) (Heading, string, error) {
	headings := TOC(body)

	segs := splitRef(ref)
	if len(segs) == 0 {
		return Heading{}, "", &NotFoundError{Ref: ref}
	}

	var matches []Heading
	for _, h := range headings {
		if matchesPath(h, segs) {
			matches = append(matches, h)
		}
	}

	switch len(matches) {
	case 0:
		// An anchor is unique by construction, so it is the fallback rather
		// than the first thing tried: reaching for it earlier would let a bare
		// slug that names several headings resolve to one of them.
		if len(segs) == 1 {
			for _, h := range headings {
				if h.Anchor == segs[0] {
					return h, body[h.start:h.end], nil
				}
			}
		}
		return Heading{}, "", &NotFoundError{Ref: ref}
	case 1:
		h := matches[0]
		return h, body[h.start:h.end], nil
	default:
		err := &AmbiguousError{Ref: ref}
		for _, h := range matches {
			err.Anchors = append(err.Anchors, h.Anchor)
			err.Paths = append(err.Paths, h.PathString())
		}
		return Heading{}, "", err
	}
}

// matchesPath reports whether segs is a tail of h.Path. The last segment may
// also be given as h.Anchor, so `installation/linux-1` addresses the second
// `Linux` under `Installation` — the path names the branch, the suffix picks the
// one of the siblings.
func matchesPath(h Heading, segs []string) bool {
	if len(segs) > len(h.Path) {
		return false
	}
	off := len(h.Path) - len(segs)
	for i, s := range segs {
		want := h.Path[off+i]
		if s == want {
			continue
		}
		if i == len(segs)-1 && s == h.Anchor {
			continue
		}
		return false
	}
	return true
}

// splitRef normalises a reference into slugified segments.
func splitRef(ref string) []string {
	var out []string
	for _, part := range strings.Split(ref, "/") {
		if s := Anchorize(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}
