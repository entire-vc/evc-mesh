package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// cyrillicFixture is real, meaningful Cyrillic prose — not a token — so a
// mangled or substituted glyph is visible in the extracted text rather than
// hiding behind a short string that happens to survive a partial failure.
const cyrillicFixture = "Инструкция по восстановлению доступа"

// extractPDFText renders bytes to a temp file and shells out to pdftotext —
// the literal command the task's own acceptance criterion names. Skips (not
// fails) when pdftotext isn't on PATH, matching this codebase's convention
// for a real-external-tool check (see internal/repository/postgres/
// *_db_test.go's skip-if-unreachable pattern) — CI installs poppler-utils
// specifically so this does not skip there (.gitlab-ci.yml, test job).
func extractPDFText(t *testing.T, data []byte) string {
	t.Helper()
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not on PATH — install poppler-utils to run this locally")
	}

	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "out.pdf")
	require.NoError(t, os.WriteFile(pdfPath, data, 0o644))

	cmd := exec.Command("pdftotext", pdfPath, "-")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	require.NoError(t, cmd.Run(), "pdftotext failed: %s", stderr.String())
	return out.String()
}

func TestExportPDF(t *testing.T) {
	t.Run("extracts real Cyrillic text — proof it is a selectable font, not a rasterized box", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "# "+cyrillicFixture+"\n\nProse follows.\n\n```bash\n# rebuild\necho hi\n```\n")

		data, filename, contentType, err := f.export.ExportPDF(context.Background(), root.ID, f.wsID)

		require.NoError(t, err)
		assert.Equal(t, "guide-"+exportDateTag()+".pdf", filename)
		assert.Equal(t, "application/pdf", contentType)

		text := extractPDFText(t, data)
		assert.Contains(t, text, cyrillicFixture, "the exact Cyrillic fixture string must come back out, not a mangled substitute")
		assert.Contains(t, text, "rebuild", "the code block's own text must be extractable too")
	})

	t.Run("negative control: the SAME fixture through a core (non-embedded) font does NOT extract Cyrillic", func(t *testing.T) {
		// This is the proof the positive test above is discriminating and not
		// vacuous (the task's own AC demands it, verbatim: "проба, ни разу не
		// видевшая красного, ничего не проверяет"). It builds an equivalent
		// PDF using fpdf's built-in core font (WinAnsi-encoded, no Cyrillic
		// glyphs at all) instead of the embedded DejaVu TTF, and asserts the
		// fixture text is ABSENT from the extraction — the exact failure mode
		// an unembedded/system-dependent font would produce in production.
		pdf := fpdf.New("P", "mm", "A4", "")
		pdf.AddPage()
		pdf.SetFont("Arial", "", 14) // core font: no AddUTF8FontFromBytes, no Cyrillic
		pdf.MultiCell(0, 8, cyrillicFixture, "", "L", false)
		require.NoError(t, pdf.Error())

		var buf bytes.Buffer
		require.NoError(t, pdf.Output(&buf))

		text := extractPDFText(t, buf.Bytes())
		assert.NotContains(t, text, cyrillicFixture,
			"a core font has no Cyrillic glyphs — if this fixture DOES come back out, the extraction check proves nothing")
	})

	t.Run("mermaid gets the caption Export 3/7 decided on, extractable in the PDF too", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Diagram page", "# Diagram\n\n```mermaid\ngraph TD; A-->B;\n```\n")

		data, _, _, err := f.export.ExportPDF(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		text := extractPDFText(t, data)
		assert.Contains(t, text, "Diagram source", "the mermaid caption from Export 3/7 must survive into the rendered PDF")
	})

	t.Run("another workspace's caller is refused before any rendering happens", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Confidential", "# secret")

		data, filename, contentType, err := f.export.ExportPDF(context.Background(), root.ID, uuid.New())

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode())
		assert.Nil(t, data)
		assert.Empty(t, filename)
		assert.Empty(t, contentType)
	})
}

func TestSplitMergedBodyIntoPDFBlocks(t *testing.T) {
	t.Run("a fenced code block becomes one code block, its own # not read as a heading", func(t *testing.T) {
		body := "# Real heading\n\nprose\n\n```bash\n# not a heading\necho hi\n```\n\nmore prose"

		blocks := splitMergedBodyIntoPDFBlocks(body)

		var kinds []pdfBlockKind
		var codeLines []string
		for _, b := range blocks {
			kinds = append(kinds, b.kind)
			if b.kind == pdfBlockCode {
				codeLines = append(codeLines, b.lines...)
			}
		}
		assert.Contains(t, kinds, pdfBlockHeading)
		assert.Contains(t, kinds, pdfBlockCode)
		assert.Equal(t, []string{"# not a heading", "echo hi"}, codeLines,
			"the fence's own # must be preserved as code text, not consumed as a heading")

		headingCount := 0
		for _, b := range blocks {
			if b.kind == pdfBlockHeading {
				headingCount++
			}
		}
		assert.Equal(t, 1, headingCount, "the # inside the fence must not be double-counted as a second heading")
	})

	t.Run("code lines preserve order and are not reflowed into one paragraph", func(t *testing.T) {
		body := "```\nline one\nline two\nline three\n```"

		blocks := splitMergedBodyIntoPDFBlocks(body)

		require.Len(t, blocks, 1)
		require.Equal(t, pdfBlockCode, blocks[0].kind)
		assert.Equal(t, []string{"line one", "line two", "line three"}, blocks[0].lines)
	})

	t.Run("an unterminated fence still ships its collected content instead of losing it", func(t *testing.T) {
		body := "```\norphaned code\nno closing fence"

		blocks := splitMergedBodyIntoPDFBlocks(body)

		require.Len(t, blocks, 1)
		require.Equal(t, pdfBlockCode, blocks[0].kind)
		assert.Equal(t, []string{"orphaned code", "no closing fence"}, blocks[0].lines)
	})

	t.Run("heading levels from mdoc.Outline are carried through unchanged", func(t *testing.T) {
		body := "# One\n\n## Two\n\nprose"

		blocks := splitMergedBodyIntoPDFBlocks(body)

		// "# One", "", "## Two", "", "prose" — 5 lines, 5 blocks.
		require.Len(t, blocks, 5)
		assert.Equal(t, pdfBlockHeading, blocks[0].kind)
		assert.Equal(t, 1, blocks[0].level)
		assert.Equal(t, "One", blocks[0].lines[0])
	})
}

func TestPDFFenceHelpers(t *testing.T) {
	t.Run("opens/closes fence agree on backtick and tilde fences", func(t *testing.T) {
		ch, n, ok := pdfOpensFence("```bash")
		require.True(t, ok)
		assert.Equal(t, byte('`'), ch)
		assert.Equal(t, 3, n)
		assert.True(t, pdfClosesFence("```", ch, n))
		assert.False(t, pdfClosesFence("``", ch, n), "two backticks must not close a three-backtick fence")

		ch, n, ok = pdfOpensFence("~~~~")
		require.True(t, ok)
		assert.Equal(t, byte('~'), ch)
		assert.Equal(t, 4, n)
	})

	t.Run("a line that is not a fence is reported as such", func(t *testing.T) {
		_, _, ok := pdfOpensFence("just prose")
		assert.False(t, ok)
		_, _, ok = pdfOpensFence("`inline code`, not a fence")
		assert.False(t, ok)
	})
}

func TestPDFHeadingSize(t *testing.T) {
	t.Run("size strictly decreases from h1 to h6, and out-of-range levels clamp", func(t *testing.T) {
		var prev float64 = 1 << 30
		for level := 1; level <= 6; level++ {
			size := pdfHeadingSize(level)
			assert.Less(t, size, prev, "level %d must be smaller than level %d", level, level-1)
			prev = size
		}
		assert.Equal(t, pdfHeadingSize(1), pdfHeadingSize(0), "below h1 clamps to h1")
		assert.Equal(t, pdfHeadingSize(6), pdfHeadingSize(9), "above h6 clamps to h6")
	})
}

// A quick sanity check that strings.Repeat-based TOC indentation actually
// widens with level — not part of the task's own AC, but cheap insurance
// against an off-by-one that would silently flatten the PDF's TOC.
func TestExportPDF_TOCIndentGrowsWithLevel(t *testing.T) {
	f := setupExportService(t)
	root := f.create(t, "Root", "# Top\n\n## Nested\n")

	data, _, _, err := f.export.ExportPDF(context.Background(), root.ID, f.wsID)
	require.NoError(t, err)

	text := extractPDFText(t, data)
	assert.True(t, strings.Contains(text, "Top") && strings.Contains(text, "Nested"))
}
