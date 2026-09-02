package service

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// exportFixture wraps documentFixture with the attachment repository and the
// export service under test.
type exportFixture struct {
	*documentFixture
	attachments *MockDocumentAttachmentRepository
	export      *documentExportService
}

func setupExportService(t *testing.T) *exportFixture {
	t.Helper()
	docFixture := setupDocumentService(t)
	attachments := NewMockDocumentAttachmentRepository()
	return &exportFixture{
		documentFixture: docFixture,
		attachments:     attachments,
		export:          NewDocumentExportService(docFixture.svc, attachments, docFixture.storage).(*documentExportService),
	}
}

// addAttachment uploads bytes for doc via the mock storage and records the
// attachment row, wiring the tenancy the mock's ListByDocument doesn't itself
// check but GetByIDInWorkspace-style callers elsewhere in the app rely on.
func (f *exportFixture) addAttachment(t *testing.T, doc *domain.Document, name string, data []byte) *domain.DocumentAttachment {
	t.Helper()
	att := &domain.DocumentAttachment{
		ID:             uuid.New(),
		DocumentID:     doc.ID,
		Name:           name,
		MimeType:       "application/octet-stream",
		SizeBytes:      int64(len(data)),
		StorageKey:     "documents/attachments/" + uuid.NewString(),
		UploadedBy:     uuid.New(),
		UploadedByType: domain.ActorTypeUser,
		CreatedAt:      frozenTime,
	}
	require.NoError(t, f.attachments.Create(context.Background(), att))
	require.NoError(t, f.storage.Upload(context.Background(), att.StorageKey, bytes.NewReader(data), int64(len(data)), att.MimeType))
	return att
}

func attachmentLink(attID uuid.UUID) string {
	return "/api/v1/document-attachments/" + attID.String() + "/download?disposition=inline"
}

func TestDocumentExportRender_Self(t *testing.T) {
	t.Run("returns the body verbatim, filename and content type set", func(t *testing.T) {
		f := setupExportService(t)
		doc := f.create(t, "Runbook", "# Runbook\n\nSteps here.")

		data, filename, contentType, err := f.export.ExportMarkdown(context.Background(), doc.ID, f.wsID, ExportScopeSelf)

		require.NoError(t, err)
		assert.Equal(t, "# Runbook\n\nSteps here.", string(data))
		assert.Equal(t, doc.Slug+"-"+exportDateTag()+".md", filename)
		assert.Equal(t, "text/markdown", contentType)
	})

	t.Run("another workspace's caller is refused", func(t *testing.T) {
		f := setupExportService(t)
		doc := f.create(t, "Confidential", "secret")

		_, _, _, err := f.export.ExportMarkdown(context.Background(), doc.ID, uuid.New(), ExportScopeSelf)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode())
	})
}

// zipEntries extracts a zip's files as path -> content, for assertions that
// compare the archive structure by name rather than by "looks about right".
func zipEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	out := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		content, err := io.ReadAll(rc)
		require.NoError(t, err)
		_ = rc.Close()
		out[f.Name] = string(content)
	}
	return out
}

func TestDocumentExportRender_Tree(t *testing.T) {
	t.Run("archive structure mirrors the Mesh tree exactly, by name", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "root body")
		setup := f.createChild(t, root.ID, 0, "Setup", "setup body")
		docker := f.createChild(t, setup.ID, 0, "Docker", "docker body")
		faq := f.createChild(t, root.ID, 1, "FAQ", "faq body")

		data, filename, contentType, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScopeTree)

		require.NoError(t, err)
		assert.Equal(t, root.Slug+"-"+exportDateTag()+".zip", filename)
		assert.Equal(t, "application/zip", contentType)

		entries := zipEntries(t, data)
		wantPaths := map[string]string{
			root.Slug + ".md":                                        "root body",
			root.Slug + "/" + setup.Slug + ".md":                     "setup body",
			root.Slug + "/" + setup.Slug + "/" + docker.Slug + ".md": "docker body",
			root.Slug + "/" + faq.Slug + ".md":                       "faq body",
		}
		gotPaths := make(map[string]string, len(entries))
		for p, c := range entries {
			gotPaths[p] = c
		}
		assert.Equal(t, wantPaths, gotPaths, "archive entries must match the Mesh tree by name, not just by count")
	})

	t.Run("a document with no children still exports as a one-entry archive", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Lonely", "just this")

		data, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScopeTree)

		require.NoError(t, err)
		entries := zipEntries(t, data)
		assert.Equal(t, map[string]string{root.Slug + ".md": "just this"}, entries)
	})

	t.Run("another workspace's caller is refused, no file of theirs enters an archive", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Confidential", "secret")
		f.createChild(t, root.ID, 0, "Also confidential", "secret too")

		data, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, uuid.New(), ExportScopeTree)

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 404, apiErr.StatusCode())
		assert.Nil(t, data)
	})

	t.Run("an unsupported scope is a validation error, not a silent default", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Doc", "body")

		_, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScope("branch"))

		var apiErr *apierror.Error
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, 400, apiErr.StatusCode())
	})
}

func TestDocumentExportRender_TreeAttachments(t *testing.T) {
	t.Run("a linked attachment is bundled and its link rewritten to a resolving relative path", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "intro")
		att := f.addAttachment(t, root, "diagram.png", []byte("fake-png-bytes"))
		doc, err := f.svc.Update(context.Background(), root.ID, f.wsID, UpdateDocumentInput{
			Body: strPtr("See the diagram: ![diagram](" + attachmentLink(att.ID) + ")"),
		})
		require.NoError(t, err)
		_ = doc

		data, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScopeTree)
		require.NoError(t, err)

		entries := zipEntries(t, data)
		mdPath := root.Slug + ".md"
		attPath := "_attachments/" + root.Slug + "/diagram.png"

		require.Contains(t, entries, mdPath)
		require.Contains(t, entries, attPath, "the referenced attachment must be bundled at the expected relative path")
		assert.Equal(t, "fake-png-bytes", entries[attPath])

		assert.NotContains(t, entries[mdPath], "/api/v1/document-attachments/",
			"a rewritten body must not still contain the app-only API path — that is the broken link this feature exists to fix")
		assert.Contains(t, entries[mdPath], "("+attPath+")",
			"the rewritten link must point at the bundled file's own path (root.md sits at the archive top, so no ../ prefix)")
	})

	t.Run("nested document's attachment link gets the right ../ depth back to _attachments", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "intro")
		setup := f.createChild(t, root.ID, 0, "Setup", "placeholder")
		att := f.addAttachment(t, setup, "screenshot.png", []byte("png-bytes"))
		_, err := f.svc.Update(context.Background(), setup.ID, f.wsID, UpdateDocumentInput{
			Body: strPtr("![shot](" + attachmentLink(att.ID) + ")"),
		})
		require.NoError(t, err)

		data, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScopeTree)
		require.NoError(t, err)

		entries := zipEntries(t, data)
		mdPath := root.Slug + "/" + setup.Slug + ".md"
		attPath := "_attachments/" + root.Slug + "/" + setup.Slug + "/screenshot.png"

		require.Contains(t, entries, attPath)
		assert.Contains(t, entries[mdPath], "(../"+attPath+")",
			"setup.md lives one directory down from the archive root, so its relative link needs exactly one ../")
	})

	t.Run("an uploaded but never-linked attachment is not bundled", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "no links here")
		f.addAttachment(t, root, "orphan.png", []byte("unused"))

		data, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScopeTree)
		require.NoError(t, err)

		entries := zipEntries(t, data)
		for path := range entries {
			assert.False(t, strings.Contains(path, "orphan"), "an attachment never referenced by the body should not appear in the archive: %s", path)
		}
	})

	t.Run("a link to another document's attachment is neither rewritten nor bundled", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "intro")
		// root needs at least one attachment OF ITS OWN so the walk actually
		// reaches the per-attachment ownership check below — with zero owned
		// attachments the function returns early before ever looking at the
		// foreign id, which would make this test pass without exercising the
		// guard it exists to prove.
		f.addAttachment(t, root, "legit.png", []byte("root's own file"))
		other := f.create(t, "Unrelated", "unrelated body")
		foreignAtt := f.addAttachment(t, other, "not-yours.png", []byte("belongs to another document"))

		link := attachmentLink(foreignAtt.ID)
		_, err := f.svc.Update(context.Background(), root.ID, f.wsID, UpdateDocumentInput{
			Body: strPtr("![x](" + link + ")"),
		})
		require.NoError(t, err)

		data, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScopeTree)
		require.NoError(t, err)

		entries := zipEntries(t, data)
		mdPath := root.Slug + ".md"
		assert.Contains(t, entries[mdPath], link,
			"a link to an attachment this document does not own must be left exactly as-is, not rewritten to a path that would leak another document's file")
		for path := range entries {
			assert.NotContains(t, path, "not-yours", "another document's attachment must never be bundled into this export: %s", path)
		}
	})

	t.Run("two attachments with the same filename are deduplicated, not collided", func(t *testing.T) {
		f := setupExportService(t)
		root := f.create(t, "Guide", "intro")
		att1 := f.addAttachment(t, root, "image.png", []byte("first"))
		att2 := f.addAttachment(t, root, "image.png", []byte("second"))
		_, err := f.svc.Update(context.Background(), root.ID, f.wsID, UpdateDocumentInput{
			Body: strPtr("![a](" + attachmentLink(att1.ID) + ") and ![b](" + attachmentLink(att2.ID) + ")"),
		})
		require.NoError(t, err)

		data, _, _, err := f.export.ExportMarkdown(context.Background(), root.ID, f.wsID, ExportScopeTree)
		require.NoError(t, err)

		entries := zipEntries(t, data)
		assert.Equal(t, "first", entries["_attachments/"+root.Slug+"/image.png"])
		assert.Equal(t, "second", entries["_attachments/"+root.Slug+"/image-2.png"])
	})
}
