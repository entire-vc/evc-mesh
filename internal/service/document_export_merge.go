package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

// mermaidCaption is inserted immediately above every mermaid code fence. See
// MergedExportDoc's doc comment for why a mermaid block ships as captioned
// source rather than a rendered diagram.
const mermaidCaption = "_Diagram source (mermaid — not rendered):_"

// mermaidFenceOpenRE matches a fenced code block's opening line whose info
// string is exactly "mermaid" (any case) — the same fence shapes
// pkg/mdoc.Outline recognizes (up to 3 leading spaces, 3+ backticks or
// tildes), narrowed to the one info string this cares about.
// (?m) is load-bearing: without it, ^ and $ anchor to the whole input rather
// than each line, which would make the whole-body fast-path pre-check in
// captionMermaidBlocks below silently never match a multi-line body — the
// per-line loop happens to still work without it (each line has no embedded
// newline of its own), but the pre-check would then always skip.
var mermaidFenceOpenRE = regexp.MustCompile(`(?m)^ {0,3}(?:` + "`{3,}|~{3,}" + `)[ \t]*[Mm][Ee][Rr][Mm][Aa][Ii][Dd][ \t]*$`)

func (s *documentExportService) MergeForExport(ctx context.Context, rootID, workspaceID uuid.UUID) (*MergedExportDoc, error) {
	root, err := s.documents.GetByIDInWorkspace(ctx, rootID, workspaceID)
	if err != nil {
		return nil, err
	}

	docs, err := s.documents.WalkExportTree(ctx, rootID, workspaceID)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]domain.Document, len(docs))
	for _, d := range docs {
		byID[d.ID] = d
	}

	var toc []TOCEntry
	sections := make([]string, 0, len(docs))
	for _, doc := range docs {
		depth := len(documentZipDirChain(byID, doc))
		shiftedBody, headings := shiftHeadings(doc.Body, depth)
		for _, h := range headings {
			toc = append(toc, TOCEntry{Level: h.Level, Text: h.Text, Anchor: h.Anchor, DocumentID: doc.ID})
		}
		sections = append(sections, captionMermaidBlocks(shiftedBody))
	}

	return &MergedExportDoc{
		Title:      root.Title,
		Version:    root.Version,
		ExportedAt: timeNow(),
		TOC:        toc,
		Body:       strings.Join(sections, "\n\n"),
	}, nil
}

// Markdown assembles TOC, Body and the colophon into one ready-to-read
// markdown file — the shape a plain-markdown consumer, or a test asserting
// on the whole document, wants. A format-specific renderer (PDF/DOCX) uses
// the structured fields instead of parsing this back out; this exists so
// "one intermediate document" has a literal one-document form, not only a
// struct.
func (m *MergedExportDoc) Markdown() string {
	var b strings.Builder
	b.WriteString("## Table of contents\n\n")
	for _, e := range m.TOC {
		b.WriteString(strings.Repeat("  ", max(e.Level-1, 0)))
		b.WriteString("- [")
		b.WriteString(e.Text)
		b.WriteString("](#")
		b.WriteString(e.Anchor)
		b.WriteString(")\n")
	}
	b.WriteString("\n---\n\n")
	b.WriteString(m.Body)
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "%s — version %d — exported %s\n", m.Title, m.Version, m.ExportedAt.Format("2006-01-02"))
	return b.String()
}

// shiftHeadings shifts every ATX heading in body down by depth levels
// (capped at h6) and returns the rewritten body alongside the shifted
// headings, in document order.
//
// It rewrites exactly the lines pkg/mdoc.Outline identified as real
// headings — never a line inside a fenced code block or YAML frontmatter,
// which is what keeps `# comment` inside a shell example from being mistaken
// for structure and moved. Everything on a heading line past its leading
// `#` run (the space, the text, any optional closing `#`s) is left
// byte-for-byte as written; only the run's length changes.
func shiftHeadings(body string, depth int) (shiftedBody string, headings []mdoc.Heading) {
	original := mdoc.Outline(body)
	if len(original) == 0 {
		return body, nil
	}

	lines := strings.Split(body, "\n")
	headings = make([]mdoc.Heading, len(original))
	for i, h := range original {
		h.Level = min(h.Level+depth, 6)
		lines[h.Line-1] = shiftHeadingMarker(lines[h.Line-1], h.Level)
		headings[i] = h
	}
	return strings.Join(lines, "\n"), headings
}

// shiftHeadingMarker replaces a heading line's opening `#` run with one of
// newLevel `#`s, keeping the leading indent and everything from the first
// non-`#` character onward — the space, the text, any decorative closing
// `#`s — exactly as written.
func shiftHeadingMarker(line string, newLevel int) string {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	j := i
	for j < len(line) && line[j] == '#' {
		j++
	}
	return line[:i] + strings.Repeat("#", newLevel) + line[j:]
}

// captionMermaidBlocks inserts mermaidCaption immediately before every
// mermaid fence opening line. It does not otherwise touch the block: the
// fenced content ships as-is, source and all.
func captionMermaidBlocks(body string) string {
	if !mermaidFenceOpenRE.MatchString(body) {
		// The common case: no mermaid fence anywhere, so no split+rejoin.
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if mermaidFenceOpenRE.MatchString(ln) {
			out = append(out, mermaidCaption)
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}
