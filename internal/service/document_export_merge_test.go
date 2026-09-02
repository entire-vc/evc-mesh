package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func TestExportMerge(t *testing.T) {
	t.Run("heading levels are shifted by each document's depth, on a three-level tree", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Root", "# Root heading\n\nRoot prose.")
		child := f.createChild(t, root.ID, 0, "Child", "# Child heading\n\n## Child subheading\n\nChild prose.")
		grandchild := f.createChild(t, child.ID, 0, "Grandchild", "# Grandchild heading\n\nGrandchild prose.")

		merged, err := f.export.MergeForExport(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		body := merged.Body
		assert.Contains(t, body, "# Root heading", "depth 0: no shift")
		assert.Contains(t, body, "## Child heading", "depth 1: shifted by 1")
		assert.Contains(t, body, "### Child subheading", "depth 1: shifted by 1")
		assert.Contains(t, body, "### Grandchild heading", "depth 2: shifted by 2")
		_ = grandchild

		wantTOC := []struct {
			level int
			text  string
		}{
			{1, "Root heading"},
			{2, "Child heading"},
			{3, "Child subheading"},
			{3, "Grandchild heading"},
		}
		require.Len(t, merged.TOC, len(wantTOC))
		for i, want := range wantTOC {
			assert.Equal(t, want.level, merged.TOC[i].Level, "TOC[%d] level", i)
			assert.Equal(t, want.text, merged.TOC[i].Text, "TOC[%d] text", i)
		}
	})

	t.Run("the shift ceiling is h6 — a document six levels deep with its own h6 heading does not overflow", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "L0", "prose only, no heading")
		cur := root
		for i := 1; i <= 5; i++ {
			cur = f.createChild(t, cur.ID, 0, "L"+itoa(i), "prose only, no heading")
		}
		deepest := f.createChild(t, cur.ID, 0, "L6", "###### Already at the ceiling\n\nDeep prose.")

		merged, err := f.export.MergeForExport(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		assert.Contains(t, merged.Body, "###### Already at the ceiling")
		assert.NotContains(t, merged.Body, "#######",
			"6 (own level) + 6 (depth) = 12 must cap at h6, not overflow past it")

		var found bool
		for _, e := range merged.TOC {
			if e.Text == "Already at the ceiling" {
				found = true
				assert.Equal(t, 6, e.Level, "capped level must be reported as 6 in the TOC too, not 12")
			}
		}
		assert.True(t, found)
		_ = deepest
	})

	t.Run("a heading whose shift would exceed 6 from a shallower depth is also capped", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "L0", "prose only, no heading")
		cur := root
		for i := 1; i <= 4; i++ {
			cur = f.createChild(t, cur.ID, 0, "L"+itoa(i), "prose only, no heading")
		}
		// depth 5, an h2 heading: 2 + 5 = 7, must cap to 6.
		f.createChild(t, cur.ID, 0, "L5", "## Second-level heading\n\nprose.")

		merged, err := f.export.MergeForExport(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		assert.Contains(t, merged.Body, "###### Second-level heading")
	})

	t.Run("table of contents count and order match the tree's sections", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Root", "# Intro")
		a := f.createChild(t, root.ID, 0, "A", "# A heading")
		f.createChild(t, a.ID, 0, "A-child", "# A-child heading")
		f.createChild(t, root.ID, 1, "B", "# B heading")

		merged, err := f.export.MergeForExport(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		var texts []string
		for _, e := range merged.TOC {
			texts = append(texts, e.Text)
		}
		assert.Equal(t, []string{"Intro", "A heading", "A-child heading", "B heading"}, texts,
			"TOC must follow the same depth-first, position-ordered walk WalkExportTree already proves (Export 1/7)")
	})

	t.Run("the footer carries the exact Version of the root document, not merely a non-empty one", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Versioned", "v1 body")
		// Two updates bump Version to 3 (Create=1, then two writes).
		_, err := f.svc.Update(context.Background(), root.ID, f.wsID, UpdateDocumentInput{Body: strPtr("v2 body")})
		require.NoError(t, err)
		_, err = f.svc.Update(context.Background(), root.ID, f.wsID, UpdateDocumentInput{Body: strPtr("v3 body")})
		require.NoError(t, err)

		merged, err := f.export.MergeForExport(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		require.Equal(t, 3, merged.Version, "must be the document's real Version, not a placeholder")
		assert.Equal(t, "Versioned", merged.Title)

		footer := merged.Markdown()
		assert.Contains(t, footer, "Versioned — version 3 — exported "+merged.ExportedAt.Format("2006-01-02"))
	})

	t.Run("a code block is left exactly as written, including a # that looks like a heading", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Root", "# Real heading\n\n```bash\n# rebuild the index\necho hi\n```\n")
		child := f.createChild(t, root.ID, 0, "Child", "some prose only")
		_ = child

		merged, err := f.export.MergeForExport(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		assert.Contains(t, merged.Body, "```bash\n# rebuild the index\necho hi\n```",
			"the code fence's content, including its own #, must be byte-for-byte untouched")
		assert.Equal(t, 1, len(merged.TOC), "the # inside the fence must not be counted as a second heading")
	})

	t.Run("a mermaid block stays as literal source and gets a caption, not a rendered diagram", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Root", "# Diagram page\n\n```mermaid\ngraph TD; A-->B;\n```\n")

		merged, err := f.export.MergeForExport(context.Background(), root.ID, f.wsID)
		require.NoError(t, err)

		assert.Contains(t, merged.Body, "```mermaid\ngraph TD; A-->B;\n```",
			"the mermaid source itself must be untouched")
		idx := strings.Index(merged.Body, mermaidCaption)
		fenceIdx := strings.Index(merged.Body, "```mermaid")
		require.NotEqual(t, -1, idx, "the caption must be present")
		assert.Less(t, idx, fenceIdx, "the caption must sit immediately BEFORE the fence, not after or missing")
	})

	t.Run("another workspace's caller is refused before any merge happens", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Confidential", "# secret")

		merged, err := f.export.MergeForExport(context.Background(), root.ID, uuid.New())

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode())
		assert.Nil(t, merged)
	})
}

func itoa(i int) string {
	return string(rune('0' + i))
}
