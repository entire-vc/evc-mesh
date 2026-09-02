package service

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// pythonDocxAvailable reports whether `python3 -c "import docx"` succeeds —
// the actual independent-parser check the task's own acceptance criteria
// names (point 2), distinct from merely having python3 on PATH. CI installs
// python-docx specifically so this does not skip there (.gitlab-ci.yml, test
// job, same pattern as poppler-utils for the PDF export tests).
func pythonDocxAvailable(t *testing.T) bool {
	t.Helper()
	return exec.Command("python3", "-c", "import docx").Run() == nil
}

// extractDOCXParagraphs renders data to a temp .docx and parses it with
// python-docx — a library this codebase did not write and does not control,
// which is the whole point: it proves the archive is a real, spec-compliant
// OOXML document an independent reader can open, not just bytes our own
// writer and a hand-rolled unzip agree on.
func extractDOCXParagraphs(t *testing.T, data []byte) []string {
	t.Helper()
	if !pythonDocxAvailable(t) {
		t.Skip("python3 python-docx not available — pip install python-docx to run this locally")
	}

	dir := t.TempDir()
	docxPath := filepath.Join(dir, "out.docx")
	require.NoError(t, os.WriteFile(docxPath, data, 0o644))

	const script = `
import sys
import docx
d = docx.Document(sys.argv[1])
for p in d.paragraphs:
    print(p.text)
`
	cmd := exec.Command("python3", "-c", script, docxPath)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	require.NoError(t, cmd.Run(), "python-docx failed to parse: %s", stderr.String())
	if out.Len() == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}

// corruptZipEntry rebuilds data's zip with name's content truncated at its
// midpoint — guaranteed to leave an unclosed XML element, not just different
// bytes that might still happen to parse. Every other entry is copied
// through unchanged, so a failure can only be attributed to the one entry
// under test.
func corruptZipEntry(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)

	found := false
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		content, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())

		if f.Name == name {
			found = true
			content = content[:len(content)/2]
		}
		w, err := zw.Create(f.Name)
		require.NoError(t, err)
		_, err = w.Write(content)
		require.NoError(t, err)
	}
	require.True(t, found, "entry %q not present in archive — negative control would be vacuous", name)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestExportDOCX(t *testing.T) {
	t.Run("archive contains the parts a real .docx needs — AC point 1", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "# Heading\n\nProse.")

		data, filename, contentType, err := f.export.ExportDOCX(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)
		assert.Equal(t, "guide-"+exportDateTag()+".docx", filename)
		assert.Equal(t, docxContentType, contentType)

		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		require.NoError(t, err)
		var names []string
		for _, f := range zr.File {
			names = append(names, f.Name)
		}
		assert.Contains(t, names, "word/document.xml")
		assert.Contains(t, names, "[Content_Types].xml")
		assert.Contains(t, names, "_rels/.rels")
		assert.Contains(t, names, "docProps/core.xml")
	})

	t.Run("an independent parser reads it back, not just the zip listing — AC point 2", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "# Heading one\n\nSome prose.\n\n```bash\necho hi\n```\n")

		data, _, _, err := f.export.ExportDOCX(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		paras := extractDOCXParagraphs(t, data)
		assert.Contains(t, paras, "Heading one")
		assert.Contains(t, paras, "Some prose.")
		assert.Contains(t, paras, "echo hi", "the code block's own text must survive as real paragraph text, not markup")
	})

	t.Run("extracts real Cyrillic text through the independent parser — AC point 3", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "# "+cyrillicFixture+"\n\nProse follows.")

		data, _, _, err := f.export.ExportDOCX(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		paras := extractDOCXParagraphs(t, data)
		assert.Contains(t, paras, cyrillicFixture, "the exact Cyrillic fixture string must come back out, not a mangled substitute")
	})

	t.Run("code indentation survives the round trip — xml:space=preserve is load-bearing", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "# H\n\n```bash\n    indented line\n```\n")

		data, _, _, err := f.export.ExportDOCX(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		paras := extractDOCXParagraphs(t, data)
		assert.Contains(t, paras, "    indented line", "leading whitespace inside a code block must not be collapsed by the XML reader")
	})

	t.Run("negative control: a corrupted word/document.xml fails the same probe — AC point 4", func(t *testing.T) {
		if !pythonDocxAvailable(t) {
			t.Skip("python3 python-docx not available — pip install python-docx to run this locally")
		}
		f := setupExportService(t)
		root := f.create(t, "Guide", "# Heading\n\nProse.")

		data, _, _, err := f.export.ExportDOCX(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		corrupted := corruptZipEntry(t, data, "word/document.xml")

		dir := t.TempDir()
		docxPath := filepath.Join(dir, "corrupted.docx")
		require.NoError(t, os.WriteFile(docxPath, corrupted, 0o644))
		cmd := exec.Command("python3", "-c", "import sys, docx; docx.Document(sys.argv[1])", docxPath)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err = cmd.Run()
		assert.Error(t, err, "a truncated word/document.xml must not parse as a valid docx — if it does, this probe proves nothing")
	})

	t.Run("mermaid gets the caption Export 3/7 decided on, extractable in the DOCX too", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Diagram page", "# Diagram\n\n```mermaid\ngraph TD; A-->B;\n```\n")

		data, _, _, err := f.export.ExportDOCX(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		paras := extractDOCXParagraphs(t, data)
		found := false
		for _, p := range paras {
			if strings.Contains(p, "Diagram source") {
				found = true
			}
		}
		assert.True(t, found, "the mermaid caption from Export 3/7 must survive into the rendered DOCX")
	})

	t.Run("another workspace's caller is refused before any rendering happens", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Confidential", "# secret")

		data, filename, contentType, err := f.export.ExportDOCX(context.Background(), root.ID, uuid.New())

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode())
		assert.Nil(t, data)
		assert.Empty(t, filename)
		assert.Empty(t, contentType)
	})
}

func TestDOCXHeadingStyleID(t *testing.T) {
	assert.Equal(t, "Heading1", docxHeadingStyleID(1))
	assert.Equal(t, "Heading6", docxHeadingStyleID(6))
	assert.Equal(t, "Heading1", docxHeadingStyleID(0), "below h1 clamps to h1")
	assert.Equal(t, "Heading6", docxHeadingStyleID(9), "above h6 clamps to h6")
}

// The two renderers must agree on relative heading weight — this is the
// contract docxHeadingHalfPoints's own doc comment claims.
func TestDOCXHeadingHalfPoints_AgreesWithPDFSizeLadder(t *testing.T) {
	for level := 1; level <= 6; level++ {
		want := int(pdfHeadingSize(level) * 2)
		got := docxHeadingHalfPoints(level)
		assert.Equal(t, want, atoiT(t, got), "level %d half-point size must track pdfHeadingSize", level)
	}
}

func atoiT(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		require.True(t, c >= '0' && c <= '9', "not a plain integer: %q", s)
		n = n*10 + int(c-'0')
	}
	return n
}
