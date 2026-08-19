package service

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// documentContentType is what the body is stored and served as.
const documentContentType = "text/markdown"

// maxDocumentBodyBytes caps a single document body. The body is read into memory
// to be hashed into an object of known length, so it needs a ceiling that is not
// "whatever the client sent".
const maxDocumentBodyBytes = 5 << 20 // 5 MiB

// DocumentStore is the slice of object storage a document body needs.
//
// It is deliberately narrower than StorageClient: a document body is rendered by
// the API, never handed to the browser as a presigned URL, so GetPresignedURL is
// not part of the contract and the service cannot grow a dependency on it by
// accident. *storage.S3Client satisfies both.
type DocumentStore interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
	// Download fetches the markdown. Caller must close the returned ReadCloser.
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

type documentService struct {
	documentRepo repository.DocumentRepository
	storage      DocumentStore
	projectRepo  repository.ProjectRepository
}

// NewDocumentService returns a DocumentService backed by the given repositories
// and object storage. A nil store is allowed and every call that needs a body
// answers 503 — same arrangement as the artifact service, so a deployment
// without object storage still serves the rest of the API.
func NewDocumentService(
	documentRepo repository.DocumentRepository,
	store DocumentStore,
	projectRepo repository.ProjectRepository,
) DocumentService {
	return &documentService{
		documentRepo: documentRepo,
		storage:      store,
		projectRepo:  projectRepo,
	}
}

// documentStorageKey is the object key for a document body:
//
//	documents/<projectID>/<documentID>.md
//
// Project-scoped so that a project's bodies can be listed, copied or dropped with
// one prefix, and keyed on the immutable document id rather than on the title or
// slug — both of which are editable, and a key that moves when a document is
// renamed would orphan the object it used to name. The .md suffix is there for
// anyone reading the bucket by hand.
//
// It does NOT repeat the file name the way the artifact key does
// (<taskID>/<artifactID>/<name>/<name>); that duplication is a quirk of the
// artifact path, not a convention to copy.
func documentStorageKey(projectID, documentID uuid.UUID) string {
	return fmt.Sprintf("documents/%s/%s.md", projectID, documentID)
}

// Create stores the markdown body and records the document.
func (s *documentService) Create(ctx context.Context, input CreateDocumentInput) (*domain.Document, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return nil, apierror.ValidationError(map[string]string{
			"title": "title is required",
		})
	}
	if len(input.Body) > maxDocumentBodyBytes {
		return nil, apierror.ValidationError(map[string]string{
			"body": fmt.Sprintf("body must be at most %d bytes", maxDocumentBodyBytes),
		})
	}
	if s.storage == nil {
		return nil, apierror.ServiceUnavailable("storage backend not configured; set S3_ENDPOINT, S3_ACCESS_KEY_ID, S3_SECRET_ACCESS_KEY, S3_BUCKET")
	}

	project, err := s.projectRepo.GetByID(ctx, input.ProjectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, apierror.NotFound("Project")
	}

	id := uuid.New()

	if input.ParentID != nil {
		if perr := s.requireParentInProject(ctx, *input.ParentID, input.ProjectID); perr != nil {
			return nil, perr
		}
	}

	slug := slugify(input.Slug)
	if slug == "" {
		slug = slugify(title)
	}
	if slug == "" {
		// A title with no ASCII letters or digits at all slugifies to nothing.
		// Falling back to the id keeps the row addressable instead of refusing a
		// document whose title happens to be written in another script.
		slug = "doc-" + id.String()[:8]
	}

	storageKey := documentStorageKey(input.ProjectID, id)
	if err = s.storage.Upload(ctx, storageKey, strings.NewReader(input.Body), int64(len(input.Body)), documentContentType); err != nil {
		return nil, apierror.InternalError("failed to upload document body to storage")
	}

	now := timeNow()
	// The creator is also the last editor: writing the document IS its most recent
	// change, and updated_at already says so. Leaving the pair NULL until somebody
	// edits would make "never edited" and "predates the column" the same value, and
	// the read model has to be able to tell those apart — see the migration.
	createdBy, createdByType := input.CreatedBy, input.CreatedByType
	doc := &domain.Document{
		ID:            id,
		ProjectID:     input.ProjectID,
		ParentID:      input.ParentID,
		Slug:          slug,
		Title:         title,
		StorageKey:    storageKey,
		Position:      input.Position,
		CreatedBy:     createdBy,
		CreatedByType: createdByType,
		UpdatedBy:     &createdBy,
		UpdatedByType: &createdByType,
		CreatedAt:     now,
		UpdatedAt:     now,
		Body:          input.Body,
	}

	if err = s.documentRepo.Create(ctx, doc); err != nil {
		// Best-effort cleanup: the row is what makes the object reachable, so an
		// object with no row is unreferenced garbage.
		_ = s.storage.Delete(ctx, storageKey)
		return nil, err
	}

	s.indexBody(ctx, doc.ID, input.Body)

	return doc, nil
}

// GetByIDInWorkspace returns the document with its markdown body, and only when
// it belongs to workspaceID.
//
// The body is always fetched: this is the single-document read, and a caller that
// wanted metadata alone would have used the list. A body that cannot be fetched
// is an error rather than an empty string — an editor that renders "" and then
// saves would overwrite the real document with nothing.
func (s *documentService) GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Document, error) {
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	if s.storage == nil {
		return nil, apierror.ServiceUnavailable("storage backend not configured")
	}
	rc, err := s.storage.Download(ctx, doc.StorageKey)
	if err != nil {
		return nil, apierror.InternalError("failed to read document body from storage")
	}
	defer func() { _ = rc.Close() }()

	body, err := io.ReadAll(io.LimitReader(rc, maxDocumentBodyBytes))
	if err != nil {
		return nil, apierror.InternalError("failed to read document body from storage")
	}
	doc.Body = string(body)

	return doc, nil
}

// Update applies a partial change to a document's title, place in the tree,
// sibling position and body.
func (s *documentService) Update(ctx context.Context, id, workspaceID uuid.UUID, input UpdateDocumentInput) (*domain.Document, error) {
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	// Set when this call replaces the body, so the index is rewritten only then.
	var indexedBody *string

	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" {
			return nil, apierror.ValidationError(map[string]string{
				"title": "title cannot be empty",
			})
		}
		doc.Title = title
	}

	switch {
	case input.ClearParent:
		doc.ParentID = nil
	case input.ParentID != nil:
		if *input.ParentID == doc.ID {
			return nil, apierror.ValidationError(map[string]string{
				"parent_id": "a document cannot be its own parent",
			})
		}
		if perr := s.requireParentInProject(ctx, *input.ParentID, doc.ProjectID); perr != nil {
			return nil, perr
		}
		// A document moved under one of its own descendants takes the whole
		// subtree out of the tree: the cycle is reachable from nothing that walks
		// down from the roots, so it disappears from every listing at once.
		descendant, hasErr := s.documentRepo.HasAncestor(ctx, *input.ParentID, doc.ID)
		if hasErr != nil {
			return nil, hasErr
		}
		if descendant {
			return nil, apierror.ValidationError(map[string]string{
				"parent_id": "a document cannot be moved under one of its own descendants",
			})
		}
		parentID := *input.ParentID
		doc.ParentID = &parentID
	}

	if input.Position != nil {
		doc.Position = *input.Position
	}

	// The body goes first: the row is the record that the object exists, so
	// writing the row before the object would advertise content that is not
	// there yet if the upload then fails.
	if input.Body != nil {
		if len(*input.Body) > maxDocumentBodyBytes {
			return nil, apierror.ValidationError(map[string]string{
				"body": fmt.Sprintf("body must be at most %d bytes", maxDocumentBodyBytes),
			})
		}
		if s.storage == nil {
			return nil, apierror.ServiceUnavailable("storage backend not configured")
		}
		if err = s.storage.Upload(ctx, doc.StorageKey, strings.NewReader(*input.Body), int64(len(*input.Body)), documentContentType); err != nil {
			return nil, apierror.InternalError("failed to upload document body to storage")
		}
		doc.Body = *input.Body
		indexedBody = input.Body
	}

	// Stamped on every path that reaches the write, not per-field: a caller who
	// only moved the document in the tree still changed it, and "last updated by"
	// that skipped some kinds of change would name the wrong person the rest of
	// the time.
	updatedBy, updatedByType := input.UpdatedBy, input.UpdatedByType
	doc.UpdatedAt = timeNow()
	doc.UpdatedBy = &updatedBy
	doc.UpdatedByType = &updatedByType
	if upErr := s.documentRepo.Update(ctx, doc); upErr != nil {
		return nil, upErr
	}

	// Only when the body actually changed. A rename or a move must not rewrite a
	// megabyte of search text to store the same bytes back.
	if indexedBody != nil {
		s.indexBody(ctx, doc.ID, *indexedBody)
	}

	// Re-read so the caller gets the resolved display names alongside the ids —
	// the same enrich-after-write the comment service does. A failure here is not
	// fatal: the write succeeded, and answering 500 would tell the caller their
	// edit was lost when it was not.
	if enriched, getErr := s.documentRepo.GetByIDInWorkspace(ctx, doc.ID, workspaceID); getErr == nil && enriched != nil {
		enriched.Body = doc.Body
		return enriched, nil
	}

	return doc, nil
}

// Delete soft-deletes the document and its descendants.
//
// The stored object is deliberately left in place: the delete is reversible by
// design (deleted_at, not DELETE), and dropping the body would make a restored
// document an empty one — a silent data loss that the row still claims to have
// content for.
func (s *documentService) Delete(ctx context.Context, id, workspaceID, deletedBy uuid.UUID, deletedByType domain.ActorType) error {
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return err
	}
	if doc == nil {
		return apierror.NotFound("Document")
	}
	return s.documentRepo.SoftDelete(ctx, id, timeNow(), deletedBy, deletedByType)
}

// ListByProject returns a paginated list of the project's live documents.
// Bodies are not fetched — see GetByIDInWorkspace.
func (s *documentService) ListByProject(ctx context.Context, projectID uuid.UUID, pg pagination.Params) (*pagination.Page[domain.Document], error) {
	pg.Normalize()
	return s.documentRepo.ListByProject(ctx, projectID, pg)
}

// maxSearchResults caps a page of hits. A picker shows a handful; a caller that
// wants the whole project has ListByProject.
const maxSearchResults = 50

// defaultSearchResults is what a caller gets for asking without saying.
const defaultSearchResults = 20

func (s *documentService) Search(
	ctx context.Context,
	projectID, workspaceID uuid.UUID,
	query string,
	limit int,
) ([]domain.DocumentSearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		// Not "everything". An empty query through a search endpoint is a caller
		// bug or an unguarded keystroke, and answering it with the project's whole
		// document set is how a search box becomes a full-table scan on every
		// backspace.
		return nil, apierror.ValidationError(map[string]string{
			"q": "q is required",
		})
	}
	if limit <= 0 {
		limit = defaultSearchResults
	}
	if limit > maxSearchResults {
		limit = maxSearchResults
	}
	return s.documentRepo.SearchInProject(ctx, projectID, workspaceID, q, limit)
}

// indexBody records the text the search index is built from.
//
// Deliberately non-fatal. The body is already in object storage by the time this
// runs, and that is the copy that matters: failing the caller's save because the
// INDEX could not be updated would trade a working document for a working search
// box. The document stays findable by title, and the next save reindexes it.
//
// The ordering is the invariant worth keeping: upload, then row, then index. It
// means search can lag the body, and can never describe a document whose stored
// text says something else.
func (s *documentService) indexBody(ctx context.Context, documentID uuid.UUID, body string) {
	_ = s.documentRepo.SetSearchText(ctx, documentID, body)
}

// requireParentInProject refuses a parent that does not exist or belongs to a
// different project. Without the project check, a caller could hang their
// document off another tenant's document and inherit its place in that tree.
func (s *documentService) requireParentInProject(ctx context.Context, parentID, projectID uuid.UUID) error {
	parent, err := s.documentRepo.GetByID(ctx, parentID)
	if err != nil {
		return err
	}
	if parent == nil || parent.ProjectID != projectID {
		return apierror.ValidationError(map[string]string{
			"parent_id": "parent document not found in this project",
		})
	}
	return nil
}
