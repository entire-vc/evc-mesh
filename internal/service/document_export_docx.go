// DOCX rendering has the same hard constraint as PDF (see
// document_export_pdf.go's own header): pure Go, zero external binaries.
// There is no pandoc on the mesh-vm prod host (verified alongside headless
// Chrome for the PDF card — `which pandoc` is empty) and mesh-api ships as a
// bare Go binary under systemd, not a container, so pandoc would be a brand
// new permanent package on a production VM outside the deploy pipeline.
//
// That rules out the other tempting shortcut too: writing a `.doc` file that
// is actually HTML with a Word-compatible MIME wrapper. Word opens that with
// a "file may be corrupted" recovery prompt — to a user it looks exactly like
// the product is broken, and the parent card (#2a467980) names this failure
// mode explicitly as the one NOT to ship.
//
// So this generates real OOXML directly: a .docx is a zip archive of a
// handful of XML parts (word/document.xml, [Content_Types].xml,
// docProps/core.xml, the _rels/ relationship files, word/styles.xml) — all of
// it stdlib (archive/zip, encoding/xml), no third-party dependency.
//
// Unlike PDF, this needs no embedded font for Cyrillic. A PDF is a fixed
// visual format: whatever glyphs aren't embedded in the file (or present on
// whatever machine renders it) simply don't draw. A .docx instead names
// fonts by family ("Calibri", "Courier New") and lets the reader's own
// installed fonts supply the glyphs — and every mainstream OS-shipped default
// font already carries full Cyrillic coverage. The word/styles.xml below
// still names those fonts explicitly (rather than leaving Word to guess a
// default), because an explicit choice here is what test point 3 (extracting
// the Cyrillic fixture back out through an independent parser) is actually
// confirming survived the round trip.
package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"math"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
)

const docxContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func (s *documentExportService) ExportDOCX(ctx context.Context, rootID, workspaceID uuid.UUID, scope ExportScope) (data []byte, filename, contentType string, err error) {
	merged, err := s.MergeForExport(ctx, rootID, workspaceID, scope)
	if err != nil {
		return nil, "", "", err
	}

	data, err = renderDOCX(merged)
	if err != nil {
		return nil, "", "", err
	}
	filename = fmt.Sprintf("%s-%s.docx", mdoc.Slugify(merged.Title), exportDateTag())
	return data, filename, docxContentType, nil
}

// renderDOCX lays MergedExportDoc out as a single .docx package. It reuses
// splitMergedBodyIntoPDFBlocks (document_export_pdf.go) for the same reason
// the PDF renderer's own next-touch note anticipated: the fence-aware
// heading/code/prose/blank classification is entirely format-agnostic, and a
// second consumer is exactly the point at which duplicating that regex-based
// fence tracker stops being the cheaper option. It also reuses
// pdfHeadingSize's point-size ladder (converted to OOXML half-points) so the
// two renderers agree on relative heading weight instead of drifting apart.
func renderDOCX(merged *MergedExportDoc) ([]byte, error) {
	body := docxBody{}

	titleRun := docxRunPr{RFonts: &docxFonts{Ascii: docxFontSans, HAnsi: docxFontSans}}
	body.Paragraphs = append(body.Paragraphs, docxParagraph{
		PPr:  &docxParagraphPr{PStyle: &docxStyleRef{Val: "Title"}},
		Runs: []docxRun{docxTextRun(merged.Title, &titleRun)},
	})

	if len(merged.TOC) > 0 {
		body.Paragraphs = append(body.Paragraphs, docxParagraph{
			PPr:  &docxParagraphPr{PStyle: &docxStyleRef{Val: "Heading2"}},
			Runs: []docxRun{docxTextRun("Table of contents", nil)},
		})
		for _, e := range merged.TOC {
			indentTwips := max(e.Level-1, 0) * 360 // 360 twips ≈ one indent step, same visual ladder as the PDF TOC's 4-space indent
			body.Paragraphs = append(body.Paragraphs, docxParagraph{
				PPr:  &docxParagraphPr{Ind: &docxIndent{Left: fmt.Sprintf("%d", indentTwips)}},
				Runs: []docxRun{docxTextRun("- "+e.Text, nil)},
			})
		}
	}

	for _, block := range splitMergedBodyIntoPDFBlocks(merged.Body) {
		switch block.kind {
		case pdfBlockHeading:
			body.Paragraphs = append(body.Paragraphs, docxParagraph{
				PPr:  &docxParagraphPr{PStyle: &docxStyleRef{Val: docxHeadingStyleID(block.level)}},
				Runs: []docxRun{docxTextRun(block.lines[0], nil)},
			})
		case pdfBlockCode:
			codeRunPr := docxRunPr{RFonts: &docxFonts{Ascii: docxFontMono, HAnsi: docxFontMono}}
			for _, ln := range block.lines {
				body.Paragraphs = append(body.Paragraphs, docxParagraph{
					PPr:  &docxParagraphPr{PStyle: &docxStyleRef{Val: "Code"}},
					Runs: []docxRun{docxTextRun(ln, &codeRunPr)},
				})
			}
		case pdfBlockProse:
			body.Paragraphs = append(body.Paragraphs, docxParagraph{
				Runs: []docxRun{docxTextRun(block.lines[0], nil)},
			})
		case pdfBlockBlank:
			body.Paragraphs = append(body.Paragraphs, docxParagraph{})
		}
	}

	footerRunPr := docxRunPr{Italic: &docxEmpty{}}
	footer := fmt.Sprintf("%s — version %d — exported %s", merged.Title, merged.Version, merged.ExportedAt.Format("2006-01-02"))
	body.Paragraphs = append(body.Paragraphs, docxParagraph{Runs: []docxRun{docxTextRun(footer, &footerRunPr)}})

	body.SectPr = &docxSectPr{
		PgSz:  docxPgSz{W: "11906", H: "16838"}, // A4 in twips, matching the PDF renderer's own page choice
		PgMar: docxPgMar{Top: "1134", Right: "1134", Bottom: "1134", Left: "1134"},
	}

	doc := docxDocument{XMLNSW: ooxmlWordprocessingNS, Body: body}
	docXML, err := xml.Marshal(doc)
	if err != nil {
		return nil, apierror.InternalError("failed to marshal document.xml")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := map[string][]byte{
		"[Content_Types].xml":          []byte(ooxmlContentTypesXML),
		"_rels/.rels":                  []byte(ooxmlRootRelsXML),
		"docProps/core.xml":            []byte(renderDocxCoreProps(merged)),
		"docProps/app.xml":             []byte(ooxmlAppPropsXML),
		"word/document.xml":            append([]byte(xml.Header), docXML...),
		"word/_rels/document.xml.rels": []byte(ooxmlDocumentRelsXML),
		"word/styles.xml":              []byte(ooxmlStylesXML),
	}
	// Sorted so the archive's own entry order is deterministic — the same
	// MergedExportDoc always produces byte-identical output, which is what
	// the negative-control test (corrupt one entry, prove the parse fails)
	// relies on to target a specific, stable entry.
	for _, name := range []string{
		"[Content_Types].xml", "_rels/.rels", "docProps/core.xml", "docProps/app.xml",
		"word/document.xml", "word/_rels/document.xml.rels", "word/styles.xml",
	} {
		if err := writeZipEntry(zw, name, parts[name]); err != nil {
			_ = zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, apierror.InternalError("failed to build docx archive")
	}
	return buf.Bytes(), nil
}

// docxHeadingStyleID maps a shifted heading level (1-6, see MergeForExport)
// to the OOXML built-in style id Word recognizes for that outline level —
// "Heading1".."Heading6" are reserved ids the Normal.dotm template (and every
// styles.xml that declares them, including this one) binds to Word's actual
// Style pane entries, not arbitrary strings.
func docxHeadingStyleID(level int) string {
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	return fmt.Sprintf("Heading%d", level)
}

// docxHeadingHalfPoints converts pdfHeadingSize's point value (document_export_pdf.go)
// into OOXML's w:sz unit, half-points — so heading N is visually the same
// relative weight in both renderers instead of two independently-chosen
// ladders drifting apart over time.
func docxHeadingHalfPoints(level int) string {
	return fmt.Sprintf("%d", int(math.Round(pdfHeadingSize(level)*2)))
}

func renderDocxCoreProps(merged *MergedExportDoc) string {
	created := merged.ExportedAt.UTC().Format("2006-01-02T15:04:05Z")
	var b bytes.Buffer
	b.WriteString(xml.Header)
	fmt.Fprintf(&b, `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`)
	fmt.Fprintf(&b, "<dc:title>%s</dc:title>", xmlEscapeText(merged.Title))
	b.WriteString(`<dc:creator>Mesh</dc:creator>`)
	fmt.Fprintf(&b, "<cp:revision>%d</cp:revision>", merged.Version)
	fmt.Fprintf(&b, `<dcterms:created xsi:type="dcterms:W3CDTF">%s</dcterms:created>`, created)
	fmt.Fprintf(&b, `<dcterms:modified xsi:type="dcterms:W3CDTF">%s</dcterms:modified>`, created)
	b.WriteString(`</cp:coreProperties>`)
	return b.String()
}

// xmlEscapeText escapes text for direct interpolation into a hand-written
// XML string (docProps/core.xml is built this way, not via encoding/xml,
// because its namespace-heavy root element doesn't benefit from the same
// struct treatment word/document.xml gets). word/document.xml itself never
// goes through this — it's built via encoding/xml.Marshal, which already
// escapes CharData correctly.
func xmlEscapeText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

const ooxmlWordprocessingNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

const (
	docxFontSans = "Calibri"
	docxFontMono = "Courier New"
)

// docxEmpty marshals as a self-closing element with no attributes or content
// — OOXML's convention for a boolean-style toggle property (w:b, w:i, ...):
// the element's mere presence means true.
type docxEmpty struct{}

type docxDocument struct {
	XMLName xml.Name `xml:"w:document"`
	XMLNSW  string   `xml:"xmlns:w,attr"`
	Body    docxBody `xml:"w:body"`
}

type docxBody struct {
	Paragraphs []docxParagraph `xml:"w:p"`
	SectPr     *docxSectPr     `xml:"w:sectPr,omitempty"`
}

type docxParagraph struct {
	PPr  *docxParagraphPr `xml:"w:pPr,omitempty"`
	Runs []docxRun        `xml:"w:r"`
}

type docxParagraphPr struct {
	PStyle *docxStyleRef `xml:"w:pStyle,omitempty"`
	Ind    *docxIndent   `xml:"w:ind,omitempty"`
}

type docxStyleRef struct {
	Val string `xml:"w:val,attr"`
}

type docxIndent struct {
	Left string `xml:"w:left,attr"`
}

type docxRun struct {
	RPr  *docxRunPr `xml:"w:rPr,omitempty"`
	Text docxText   `xml:"w:t"`
}

type docxRunPr struct {
	RFonts *docxFonts `xml:"w:rFonts,omitempty"`
	Italic *docxEmpty `xml:"w:i,omitempty"`
}

type docxFonts struct {
	Ascii string `xml:"w:ascii,attr"`
	HAnsi string `xml:"w:hAnsi,attr"`
}

// docxText's xml:space="preserve" is load-bearing, not decorative: code
// blocks carry meaningful leading-space indentation, and without this
// attribute Word (and any spec-compliant OOXML consumer) is entitled to
// collapse it, which is how "no reflow" would quietly become "reflow via
// whitespace loss" for exactly the content type that most needs literal
// preservation.
type docxText struct {
	Space string `xml:"xml:space,attr"`
	Value string `xml:",chardata"`
}

func docxTextRun(text string, rPr *docxRunPr) docxRun {
	return docxRun{RPr: rPr, Text: docxText{Space: "preserve", Value: text}}
}

type docxSectPr struct {
	PgSz  docxPgSz  `xml:"w:pgSz"`
	PgMar docxPgMar `xml:"w:pgMar"`
}

type docxPgSz struct {
	W string `xml:"w:w,attr"`
	H string `xml:"w:h,attr"`
}

type docxPgMar struct {
	Top    string `xml:"w:top,attr"`
	Right  string `xml:"w:right,attr"`
	Bottom string `xml:"w:bottom,attr"`
	Left   string `xml:"w:left,attr"`
}

const ooxmlContentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
  <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
  <Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
</Types>`

const ooxmlRootRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>`

const ooxmlDocumentRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

const ooxmlAppPropsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties">
  <Application>evc-mesh</Application>
</Properties>`

// ooxmlStylesXML declares every style id referenced from renderDOCX: Normal
// (the required default paragraph style), Title, Heading1-6 (Word's own
// reserved outline-level ids — see docxHeadingStyleID), and Code (the
// monospace paragraph style code blocks use). Every one of them names
// docxFontSans/docxFontMono explicitly rather than leaving font choice to
// whatever Word's own built-in default happens to be — see this file's own
// header comment for why an explicit, Cyrillic-capable font family is what
// the round-trip test is actually confirming survived.
var ooxmlStylesXML = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="%[1]s">
  <w:docDefaults>
    <w:rPrDefault><w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:sz w:val="22"/></w:rPr></w:rPrDefault>
  </w:docDefaults>
  <w:style w:type="paragraph" w:default="1" w:styleId="Normal">
    <w:name w:val="Normal"/>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Title">
    <w:name w:val="Title"/>
    <w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:after="240"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:b/><w:sz w:val="44"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading1">
    <w:name w:val="heading 1"/><w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:before="240" w:after="120"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:b/><w:sz w:val="%[3]s"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading2">
    <w:name w:val="heading 2"/><w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:before="200" w:after="100"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:b/><w:sz w:val="%[4]s"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading3">
    <w:name w:val="heading 3"/><w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:before="160" w:after="80"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:b/><w:sz w:val="%[5]s"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading4">
    <w:name w:val="heading 4"/><w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:before="160" w:after="80"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:b/><w:sz w:val="%[6]s"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading5">
    <w:name w:val="heading 5"/><w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:before="120" w:after="60"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:b/><w:sz w:val="%[7]s"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading6">
    <w:name w:val="heading 6"/><w:basedOn w:val="Normal"/>
    <w:pPr><w:spacing w:before="120" w:after="60"/></w:pPr>
    <w:rPr><w:rFonts w:ascii="%[2]s" w:hAnsi="%[2]s"/><w:b/><w:sz w:val="%[8]s"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Code">
    <w:name w:val="Code"/>
    <w:basedOn w:val="Normal"/>
    <w:rPr><w:rFonts w:ascii="%[9]s" w:hAnsi="%[9]s"/><w:sz w:val="18"/></w:rPr>
  </w:style>
</w:styles>`,
	ooxmlWordprocessingNS, docxFontSans,
	docxHeadingHalfPoints(1), docxHeadingHalfPoints(2), docxHeadingHalfPoints(3),
	docxHeadingHalfPoints(4), docxHeadingHalfPoints(5), docxHeadingHalfPoints(6),
	docxFontMono,
)
