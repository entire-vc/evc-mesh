package mdoc

import (
	"fmt"
	"sort"
	"strings"
)

// Section is one heading and the markdown it owns.
type Section struct {
	Heading Heading `json:"heading"`
	// Content is the markdown from the heading line through to the character
	// before the next heading of the same or a higher level — the heading itself
	// included, so the text reads as a document rather than as a fragment.
	Content string `json:"content"`
}

// HeadingNotFoundError reports a reference that names no heading in the
// document. It carries what is there, because an agent that asked for the wrong
// heading needs the list far more than it needs to be told "no".
type HeadingNotFoundError struct {
	Ref       string
	Available []string
}

func (e *HeadingNotFoundError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("no heading %q: this document has no headings", e.Ref)
	}
	return fmt.Sprintf("no heading %q in this document; available anchors: %s",
		e.Ref, strings.Join(e.Available, ", "))
}

// AmbiguousHeadingError reports heading TEXT that occurs more than once. The
// anchors it carries are what distinguish them, and asking for one of those is
// the fix.
type AmbiguousHeadingError struct {
	Ref     string
	Anchors []string
}

func (e *AmbiguousHeadingError) Error() string {
	return fmt.Sprintf("%q names %d headings in this document; address one of them by anchor: %s",
		e.Ref, len(e.Anchors), strings.Join(e.Anchors, ", "))
}

// FindSection returns the section a reference names.
//
// ref is matched against the anchor first and the heading text second. Both are
// accepted because both are things a caller legitimately has: an agent that read
// the outline has anchors, and an agent working from a human's instruction has
// the words. The anchor is tried first because it is the unambiguous one — that
// is what it is for — so a document containing a heading whose text happens to
// equal another heading's anchor still resolves the way the outline says it will.
//
// Duplicate heading TEXT is refused rather than guessed at: two
// `## Rollback, step by step` sections are both real answers to those words, and
// returning the first would be a coin toss dressed up as a result. The error
// names the anchors that separate them.
//
// The one place that is not refused is where the duplicated text happens to
// equal its own first anchor — two `## Rollback` headings, anchored `rollback`
// and `rollback-1`. There, "rollback" is not ambiguous: the anchor scheme
// defines it as the first, the outline says so, and refusing a caller who copied
// an anchor out of the outline would be perverse. Ask for `rollback-1` to get
// the other one.
func FindSection(source, ref string) (*Section, error) {
	headings := Outline(source)

	needle := strings.TrimSpace(ref)
	if needle == "" {
		return nil, &HeadingNotFoundError{Ref: ref, Available: anchorsOf(headings)}
	}

	for i := range headings {
		if headings[i].Anchor == strings.ToLower(needle) {
			return sectionOf(source, headings[i]), nil
		}
	}

	var byText []Heading
	for i := range headings {
		if strings.EqualFold(strings.TrimSpace(headings[i].Text), needle) {
			byText = append(byText, headings[i])
		}
	}
	switch len(byText) {
	case 1:
		return sectionOf(source, byText[0]), nil
	case 0:
		return nil, &HeadingNotFoundError{Ref: ref, Available: anchorsOf(headings)}
	default:
		anchors := make([]string, len(byText))
		for i := range byText {
			anchors[i] = byText[i].Anchor
		}
		return nil, &AmbiguousHeadingError{Ref: ref, Anchors: anchors}
	}
}

func sectionOf(source string, h Heading) *Section {
	return &Section{Heading: h, Content: source[h.Start:h.End]}
}

// anchorsOf lists the document's anchors for an error message, sorted so the
// message is stable and skimmable rather than in document order — a reader of
// "no such heading" is looking one up, not reading the document.
func anchorsOf(headings []Heading) []string {
	if len(headings) == 0 {
		return nil
	}
	out := make([]string, len(headings))
	for i := range headings {
		out[i] = headings[i].Anchor
	}
	sort.Strings(out)
	return out
}
