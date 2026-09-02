// PDF rendering has one hard constraint, set by the parent card (#2a467980):
// pure Go, zero external binaries. There is no headless Chrome on the mesh-vm
// prod host (verified: `ssh mesh-vm 'which chromium chromium-browser
// google-chrome'` — empty) and mesh-api ships as a bare Go binary under
// systemd, not a container, so any external renderer would be a brand new
// permanent package on a production VM outside the deploy pipeline, with its
// own update cadence and its own attack surface — a browser rendering
// arbitrary document content is not a small addition.
//
// That constraint is also what closes the Cyrillic requirement BY
// CONSTRUCTION rather than by hoping the serving host has the right fonts
// installed: github.com/go-pdf/fpdf (MIT) can embed a TTF's bytes directly
// into the PDF via AddUTF8FontFromBytes, and the two fonts embedded here
// (internal/service/assets/fonts/, DejaVu Sans + DejaVu Sans Mono, Bitstream
// Vera-derived license, LICENSE_DEJAVU alongside them) carry full Cyrillic
// coverage — confirmed both by fontconfig's charset dump (page U+0400 reads
// all bits set for DejaVuSans.ttf) and by this file's own test, which
// extracts real Cyrillic text back out of a rendered PDF with pdftotext.
package service

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

//go:embed assets/fonts/DejaVuSans.ttf
var pdfFontSansTTF []byte

//go:embed assets/fonts/DejaVuSansMono.ttf
var pdfFontMonoTTF []byte

const (
	pdfFontSans = "DejaVuSans"
	pdfFontMono = "DejaVuSansMono"
)

func (s *documentExportService) ExportPDF(ctx context.Context, rootID, workspaceID uuid.UUID, scope ExportScope) (data []byte, filename, contentType string, err error) {
	merged, err := s.MergeForExport(ctx, rootID, workspaceID, scope)
	if err != nil {
		return nil, "", "", err
	}

	data, err = renderPDF(merged, pdfFontSansTTF, pdfFontMonoTTF)
	if err != nil {
		return nil, "", "", err
	}
	filename = fmt.Sprintf("%s-%s.pdf", mdoc.Slugify(merged.Title), exportDateTag())
	return data, filename, "application/pdf", nil
}

// renderPDF lays MergedExportDoc out as a single PDF. Takes the font bytes as
// parameters (rather than reading the package vars directly) so the negative
// control — proving the acceptance test actually fails without embedded
// Cyrillic glyphs — can call it with a non-Cyrillic-capable substitute
// without touching the embedded asset used in production.
func renderPDF(merged *MergedExportDoc, sansTTF, monoTTF []byte) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(true, 20)
	pdf.SetMargins(20, 20, 20)
	pdf.AddUTF8FontFromBytes(pdfFontSans, "", sansTTF)
	pdf.AddUTF8FontFromBytes(pdfFontMono, "", monoTTF)
	pdf.AddPage()

	pdf.SetFont(pdfFontSans, "", 20)
	pdf.MultiCell(0, 10, merged.Title, "", "L", false)
	pdf.Ln(4)

	if len(merged.TOC) > 0 {
		pdf.SetFont(pdfFontSans, "", 14)
		pdf.MultiCell(0, 8, "Table of contents", "", "L", false)
		pdf.SetFont(pdfFontSans, "", 11)
		for _, e := range merged.TOC {
			indent := strings.Repeat("    ", max(e.Level-1, 0))
			pdf.MultiCell(0, 6, indent+"- "+e.Text, "", "L", false)
		}
		pdf.Ln(6)
	}

	for _, block := range splitMergedBodyIntoPDFBlocks(merged.Body) {
		switch block.kind {
		case pdfBlockHeading:
			size := pdfHeadingSize(block.level)
			pdf.SetFont(pdfFontSans, "", size)
			pdf.MultiCell(0, size*0.6, block.lines[0], "", "L", false)
			pdf.Ln(2)
		case pdfBlockCode:
			pdf.SetFont(pdfFontMono, "", 9)
			for _, ln := range block.lines {
				pdf.MultiCell(0, 5, ln, "", "L", false)
			}
			pdf.Ln(2)
		case pdfBlockProse:
			pdf.SetFont(pdfFontSans, "", 11)
			pdf.MultiCell(0, 6, block.lines[0], "", "L", false)
		case pdfBlockBlank:
			pdf.Ln(4)
		}
	}

	pdf.Ln(6)
	pdf.SetFont(pdfFontSans, "", 9)
	footer := fmt.Sprintf("%s — version %d — exported %s", merged.Title, merged.Version, merged.ExportedAt.Format("2006-01-02"))
	pdf.MultiCell(0, 5, footer, "", "L", false)

	if err := pdf.Error(); err != nil {
		return nil, apierror.InternalError("failed to lay out PDF: " + err.Error())
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, apierror.InternalError("failed to render PDF")
	}
	return buf.Bytes(), nil
}

// pdfHeadingSize maps a shifted heading level (1-6) to a point size, largest
// at h1 and shrinking toward body text size at h6 — the level itself is
// already the ONLY signal a reader has for hierarchy here (see the "no bold
// weight embedded" note in splitMergedBodyIntoPDFBlocks), so the steps are
// kept large enough to actually read as a hierarchy at a glance.
func pdfHeadingSize(level int) float64 {
	sizes := [7]float64{0, 18, 16, 14, 13, 12, 11.5}
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	return sizes[level]
}

type pdfBlockKind int

const (
	pdfBlockProse pdfBlockKind = iota
	pdfBlockHeading
	pdfBlockCode
	pdfBlockBlank
)

type pdfBlock struct {
	kind  pdfBlockKind
	level int // only for pdfBlockHeading
	lines []string
}

// splitMergedBodyIntoPDFBlocks walks a merged export's body and classifies
// every line, the same way document_export_merge.go's own heading shift
// does: pkg/mdoc.Outline decides which lines are real headings (already
// fence- and frontmatter-aware), and a small local fence tracker — mirroring
// mdoc's own opensFence/closesFence rules rather than depending on mdoc to
// export them for one caller — finds the code blocks (mermaid fences
// included; MergeForExport has already inserted their caption as a plain
// prose line ahead of the fence, so it renders like any other paragraph).
//
// Everything inside a fence is collected into one pdfBlockCode block and
// rendered in the monospace font, line breaks preserved exactly — no
// reflow, since reflowing code is how code stops running.
//
// Despite the pdf-prefixed name, this is also the DOCX renderer's block
// classifier (document_export_docx.go) — Export 4/7's own next-touch note
// flagged the fence-tracking logic as worth sharing "when a second consumer
// shows up", and DOCX is that second consumer. The classification itself
// (heading/code/prose/blank) has no PDF-specific behavior in it; only the
// caller decides what to do with each block kind.
func splitMergedBodyIntoPDFBlocks(body string) []pdfBlock {
	headingByLine := make(map[int]mdoc.Heading)
	for _, h := range mdoc.Outline(body) {
		headingByLine[h.Line] = h
	}

	lines := strings.Split(body, "\n")
	var blocks []pdfBlock
	var fenceChar byte
	var fenceLen int
	var codeLines []string

	flushCode := func() {
		if len(codeLines) > 0 {
			blocks = append(blocks, pdfBlock{kind: pdfBlockCode, lines: codeLines})
			codeLines = nil
		}
	}

	for i, raw := range lines {
		lineNo := i + 1

		if fenceChar != 0 {
			if pdfClosesFence(raw, fenceChar, fenceLen) {
				fenceChar, fenceLen = 0, 0
				flushCode()
				continue
			}
			codeLines = append(codeLines, raw)
			continue
		}

		if ch, n, ok := pdfOpensFence(raw); ok {
			fenceChar, fenceLen = ch, n
			continue
		}

		if h, ok := headingByLine[lineNo]; ok {
			blocks = append(blocks, pdfBlock{kind: pdfBlockHeading, level: h.Level, lines: []string{h.Text}})
			continue
		}

		if strings.TrimSpace(raw) == "" {
			blocks = append(blocks, pdfBlock{kind: pdfBlockBlank})
			continue
		}

		blocks = append(blocks, pdfBlock{kind: pdfBlockProse, lines: []string{raw}})
	}
	// An unterminated fence (malformed input) still ships what it collected
	// rather than losing it — a code block missing its closing marker should
	// not make the rest of its own content vanish from the export.
	flushCode()

	return blocks
}

// pdfOpensFence and pdfClosesFence mirror pkg/mdoc's own unexported
// opensFence/closesFence: up to 3 leading spaces, then 3 or more of the same
// fence character (backtick or tilde). Duplicated rather than exported from
// mdoc because this is the only caller outside that package, and the rule
// itself is a stable, small piece of CommonMark syntax, not something likely
// to drift between the two call sites.
func pdfOpensFence(text string) (fence byte, length int, ok bool) {
	indent := 0
	for indent < len(text) && text[indent] == ' ' {
		indent++
	}
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
	if ch == '`' && strings.ContainsRune(rest[n:], '`') {
		return 0, 0, false
	}
	return ch, n, true
}

func pdfClosesFence(text string, ch byte, openLen int) bool {
	indent := 0
	for indent < len(text) && text[indent] == ' ' {
		indent++
	}
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
