package service

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// maxAttachmentBytes caps a single upload at 25 MiB.
//
// Larger than the 5 MiB document-body cap because this is where screenshots and
// PDFs land, and smaller than "no limit" because the object is written into a
// bucket the tenant does not pay for per-request.
const maxAttachmentBytes = 25 << 20 // 25 MiB

type documentAttachmentService struct {
	attachmentRepo repository.DocumentAttachmentRepository
	documentRepo   repository.DocumentRepository
	storage        StorageClient
}

// NewDocumentAttachmentService returns a DocumentAttachmentService backed by the
// given repositories and object storage.
//
// It takes the full StorageClient, not the narrower DocumentStore a document body
// uses: an attachment IS handed to the browser as a presigned URL, because an
// <img> tag cannot attach an Authorization header and so cannot fetch through the
// API the way a rendered body does. GetPresignedURL is therefore part of the
// contract here on purpose.
//
// A nil store is allowed and every call that needs the object answers 503 — same
// arrangement as the artifact and document services, so a deployment without
// object storage still serves the rest of the API.
func NewDocumentAttachmentService(
	attachmentRepo repository.DocumentAttachmentRepository,
	documentRepo repository.DocumentRepository,
	store StorageClient,
) DocumentAttachmentService {
	return &documentAttachmentService{
		attachmentRepo: attachmentRepo,
		documentRepo:   documentRepo,
		storage:        store,
	}
}

// documentAttachmentStorageKey is the object key for an attachment:
//
//	documents/<projectID>/<documentID>/attachments/<attachmentID><ext>
//
// Nested under the document's own prefix so a project's or a document's objects
// can be listed, copied or dropped with one prefix, and keyed on the immutable
// attachment id rather than on the file name — the name is display metadata, and
// a key that moved when a file was renamed would orphan the object it used to
// name. ext is there for anyone reading the bucket by hand and may be empty.
//
// It does NOT repeat the file name the way the artifact key does
// (<taskID>/<artifactID>/<name>/<name>); that duplication is a quirk of the
// artifact path, not a convention to copy.
func documentAttachmentStorageKey(projectID, documentID, attachmentID uuid.UUID, name string) string {
	// Lowercased so the same file uploaded as .PNG and .png does not produce two
	// spellings of one key shape. The extension is decoration on an id-keyed path,
	// so nothing depends on it round-tripping exactly.
	ext := strings.ToLower(filepath.Ext(name))
	return fmt.Sprintf("documents/%s/%s/attachments/%s%s", projectID, documentID, attachmentID, ext)
}

// Upload stores the file and records the attachment.
func (s *documentAttachmentService) Upload(ctx context.Context, input UploadDocumentAttachmentInput) (*domain.DocumentAttachment, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, apierror.ValidationError(map[string]string{
			"name": "name is required",
		})
	}
	if input.Size > maxAttachmentBytes {
		return nil, apierror.ValidationError(map[string]string{
			"file": fmt.Sprintf("file must be at most %d bytes", maxAttachmentBytes),
		})
	}
	if s.storage == nil {
		return nil, apierror.ServiceUnavailable("storage backend not configured; set S3_ENDPOINT, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_BUCKET")
	}

	// The document is loaded for two reasons, and both are load-bearing: it is
	// where project_id for the key comes from, and it is the ownership check.
	// Without it a caller could hang a file off another tenant's document — or a
	// deleted one, which would resurrect content in a page its owner believes is
	// gone.
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, input.DocumentID, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	id := uuid.New()
	storageKey := documentAttachmentStorageKey(doc.ProjectID, doc.ID, id, name)

	// input.Size is the client's claim, and the check above trusted it. This is
	// the check that does not: the reader is capped at one byte past the limit, so
	// a body longer than the cap arrives as cap+1 bytes and is refused, whatever
	// Content-Length said.
	//
	// Both checks are kept. The declared-size check is what refuses a 4 GiB upload
	// before a single byte is streamed to storage; the limited reader is what makes
	// the cap true rather than advisory. Neither alone is enough — the first can be
	// lied to, and the second only fires after the transfer.
	limited := io.LimitReader(input.Reader, maxAttachmentBytes+1)
	counting := &countingReader{r: limited}

	if err = s.storage.Upload(ctx, storageKey, counting, input.Size, input.MimeType); err != nil {
		return nil, apierror.InternalError("failed to upload attachment to storage")
	}
	if counting.n > maxAttachmentBytes {
		// The object is already written, so it has to come back out: nothing
		// references it, and leaving it would let a caller fill the bucket with
		// oversize objects the API refuses to acknowledge.
		_ = s.storage.Delete(ctx, storageKey)
		return nil, apierror.ValidationError(map[string]string{
			"file": fmt.Sprintf("file must be at most %d bytes", maxAttachmentBytes),
		})
	}

	att := &domain.DocumentAttachment{
		ID:         id,
		DocumentID: doc.ID,
		Name:       name,
		MimeType:   input.MimeType,
		// The counted length, not the declared one: it is what is actually in the
		// bucket, and a wrong size_bytes here would be believed by everything
		// downstream that reads it.
		SizeBytes:      counting.n,
		StorageKey:     storageKey,
		UploadedBy:     input.UploadedBy,
		UploadedByType: input.UploadedByType,
		CreatedAt:      timeNow(),
	}

	if err = s.attachmentRepo.Create(ctx, att); err != nil {
		// Best-effort cleanup: the row is what makes the object reachable, so an
		// object with no row is unreferenced garbage.
		_ = s.storage.Delete(ctx, storageKey)
		return nil, err
	}

	return att, nil
}

// GetDownloadURL returns a presigned URL for the attachment, and only when it
// belongs to workspaceID.
//
// The URL expires (presignedURLExpiry, 1h), which is exactly why nothing writes
// one into a markdown body: the stored reference is the stable API path, and this
// is what resolves it to a fresh signature at render time.
func (s *documentAttachmentService) GetDownloadURL(ctx context.Context, id, workspaceID uuid.UUID, inline bool) (string, error) {
	att, err := s.attachmentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return "", err
	}
	if att == nil {
		return "", apierror.NotFound("Attachment")
	}

	if s.storage == nil {
		return "", apierror.ServiceUnavailable("storage backend not configured")
	}

	// Same argument semantics as artifactService.GetDownloadURL: a non-empty
	// filename sets Content-Disposition: attachment, which forces a download.
	// inline drops it so the browser renders the response using its Content-Type —
	// without this an <img> pointed at the URL downloads a file instead of showing
	// a picture.
	filename := att.Name
	if inline {
		filename = ""
	}

	url, err := s.storage.GetPresignedURL(ctx, att.StorageKey, presignedURLExpiry, att.MimeType, filename)
	if err != nil {
		return "", apierror.InternalError("failed to generate download URL")
	}

	return url, nil
}

// ListByDocument returns a paginated list of the document's live attachments.
func (s *documentAttachmentService) ListByDocument(ctx context.Context, documentID, workspaceID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.DocumentAttachment], error) {
	// The document is checked first so that a listing for another tenant's
	// document is a 404 rather than an empty page — an empty page is an answer,
	// and answering at all confirms which ids exist.
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, documentID, workspaceID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	pg.Normalize()
	return s.attachmentRepo.ListByDocument(ctx, documentID, pg)
}

// Delete soft-deletes the attachment.
//
// The stored object is deliberately left in place, for the same reason
// documentService.Delete leaves a body behind: the delete is reversible by design
// (deleted_at, not DELETE), and dropping the bytes would make a restored
// attachment a permanently broken image that the row still claims to have.
func (s *documentAttachmentService) Delete(ctx context.Context, id, workspaceID uuid.UUID) error {
	att, err := s.attachmentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return err
	}
	if att == nil {
		return apierror.NotFound("Attachment")
	}
	return s.attachmentRepo.SoftDelete(ctx, id, timeNow())
}

// countingReader reports how many bytes were actually read through it. The
// storage client is what consumes the reader, so this is the only place the real
// length is observable — Content-Length is the client's claim, not a measurement.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
