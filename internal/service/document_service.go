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
		// The first revision. Handed straight back to the caller, so a client that
		// creates a document and immediately edits it has a base version without a
		// second read.
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
		Body:      input.Body,
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
	body, err := s.downloadBody(ctx, doc.StorageKey)
	if err != nil {
		return nil, err
	}
	doc.Body = body

	return doc, nil
}

// DocumentVersionConflictError is a write refused because the document has moved
// on since the caller read it.
//
// A typed error rather than apierror.Conflict because the answer has to carry
// the version the document is actually at. "409, try again" sends the caller
// back to a read to find out what happened; "409, you were at 3 and it is at 7"
// tells them in the refusal, and is what lets a client decide between reloading,
// merging, or telling the user their edit collided with somebody else's.
type DocumentVersionConflictError struct {
	DocumentID     uuid.UUID
	BaseVersion    int64
	CurrentVersion int64
}

func (e *DocumentVersionConflictError) Error() string {
	return fmt.Sprintf("document %s is at version %d, not the version %d this write was built on",
		e.DocumentID, e.CurrentVersion, e.BaseVersion)
}

// Update applies a partial change to a document's title, place in the tree,
// sibling position and body.
//
// The write is conditional on input.BaseVersion, which is REQUIRED. An update
// arriving without one is refused rather than treated as an unconditional
// write: unconditional is exactly the behaviour that lost an edit on
// 2026-08-19, and a guard a caller turns off by leaving a field out protects
// only the callers who did not need protecting.
//
// The check lives here rather than in the HTTP handler because HTTP is not the
// only way in. The MCP server, an internal job, anything that grows a document
// write later — all of them arrive at this function, and a rule enforced one
// layer up is a rule that holds for exactly one caller.
func (s *documentService) Update(ctx context.Context, id, workspaceID uuid.UUID, input UpdateDocumentInput) (*domain.Document, error) {
	if input.BaseVersion == nil {
		return nil, apierror.ValidationError(map[string]string{
			"base_version": "base_version is required: read the document and send back the version it reported",
		})
	}

	// Cheap refusals before the row lock is taken. An oversized body or a missing
	// storage backend fails the same way whether or not anything is locked, and
	// holding a lock across a request that was never going to succeed makes every
	// other writer on that page wait for it.
	if input.Body != nil {
		if len(*input.Body) > maxDocumentBodyBytes {
			return nil, apierror.ValidationError(map[string]string{
				"body": fmt.Sprintf("body must be at most %d bytes", maxDocumentBodyBytes),
			})
		}
		if s.storage == nil {
			return nil, apierror.ServiceUnavailable("storage backend not configured")
		}
	}

	var writtenBody *string
	doc, err := s.documentRepo.MutateLocked(ctx, id, workspaceID, func(locked *domain.Document) error {
		// First thing under the lock, before any work that could be wasted and
		// before anything is written anywhere. Reading the version here rather
		// than from an earlier unlocked read is what closes the check-then-act
		// window: between an unlocked read and the write, another writer fits.
		if locked.Version != *input.BaseVersion {
			return &DocumentVersionConflictError{
				DocumentID:     locked.ID,
				BaseVersion:    *input.BaseVersion,
				CurrentVersion: locked.Version,
			}
		}

		if input.Title != nil {
			title := strings.TrimSpace(*input.Title)
			if title == "" {
				return apierror.ValidationError(map[string]string{
					"title": "title cannot be empty",
				})
			}
			locked.Title = title
		}

		switch {
		case input.ClearParent:
			locked.ParentID = nil
		case input.ParentID != nil:
			if *input.ParentID == locked.ID {
				return apierror.ValidationError(map[string]string{
					"parent_id": "a document cannot be its own parent",
				})
			}
			if perr := s.requireParentInProject(ctx, *input.ParentID, locked.ProjectID); perr != nil {
				return perr
			}
			// A document moved under one of its own descendants takes the whole
			// subtree out of the tree: the cycle is reachable from nothing that
			// walks down from the roots, so it disappears from every listing at
			// once.
			descendant, hasErr := s.documentRepo.HasAncestor(ctx, *input.ParentID, locked.ID)
			if hasErr != nil {
				return hasErr
			}
			if descendant {
				return apierror.ValidationError(map[string]string{
					"parent_id": "a document cannot be moved under one of its own descendants",
				})
			}
			parentID := *input.ParentID
			locked.ParentID = &parentID
		}

		if input.Position != nil {
			locked.Position = *input.Position
		}

		// The upload sits inside the callback, which is inside the row lock. It
		// used to sit before the row write on the reasoning that a row must not
		// advertise content that is not there yet — true for a create, which
		// names an object that does not exist. For an update the object already
		// exists and the danger is the other one: an unserialized upload is how
		// two writers erase each other, and only the lock stops that.
		if input.Body != nil {
			if upErr := s.storage.Upload(ctx, locked.StorageKey, strings.NewReader(*input.Body),
				int64(len(*input.Body)), documentContentType); upErr != nil {
				return apierror.InternalError("failed to upload document body to storage")
			}
			writtenBody = input.Body
		}

		// Stamped on every path that reaches the write, not per-field: a caller
		// who only moved the document in the tree still changed it, and "last
		// updated by" that skipped some kinds of change would name the wrong
		// person the rest of the time.
		updatedBy, updatedByType := input.UpdatedBy, input.UpdatedByType
		locked.UpdatedAt = timeNow()
		locked.UpdatedBy = &updatedBy
		locked.UpdatedByType = &updatedByType
		return nil
	})
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	// The repository re-reads the row, which has no body column; the body it now
	// holds is the one this call just uploaded.
	if writtenBody != nil {
		doc.Body = *writtenBody
		// Only when the body actually changed. A rename or a move must not rewrite
		// a megabyte of search text to store the same bytes back.
		//
		// OUTSIDE the lock, and it has to be: SetSearchText is an UPDATE on this
		// same documents row, issued on its own connection, so calling it from
		// inside the callback would have it wait on the row lock this
		// transaction is still holding — a self-deadlock lasting until something
		// times out. Keeping it here preserves the ordering the search index
		// depends on (upload, then row, then index) as well.
		s.indexBody(ctx, doc.ID, *writtenBody)
	}

	return doc, nil
}

// AppendBody adds text to the end of the document body.
//
// No base version, by design. An append does not claim to know what the document
// currently says and it deletes nothing, so there is no edit it can silently
// destroy — the property that makes a conditional write necessary is simply
// absent. Demanding one anyway would mean every appender first reads the whole
// document, and then fails whenever somebody else appended in between, which is
// a conflict manufactured by the guard rather than found by it.
//
// "Cannot conflict" is a claim about the result, not about the mechanism: two
// simultaneous appends still have to end up with both texts present, and getting
// there needs the same row lock everything else uses. The read-modify-write of
// the body happens inside it, so the second appender reads the first one's
// committed text rather than the text they both started from.
func (s *documentService) AppendBody(ctx context.Context, id, workspaceID uuid.UUID, input AppendDocumentInput) (*domain.Document, error) {
	if input.Text == "" {
		return nil, apierror.ValidationError(map[string]string{
			"text": "text is required",
		})
	}
	if s.storage == nil {
		return nil, apierror.ServiceUnavailable("storage backend not configured")
	}

	var writtenBody string
	doc, err := s.documentRepo.MutateLocked(ctx, id, workspaceID, func(locked *domain.Document) error {
		existing, readErr := s.downloadBody(ctx, locked.StorageKey)
		if readErr != nil {
			return readErr
		}

		joined := appendToBody(existing, input.Text)
		if len(joined) > maxDocumentBodyBytes {
			return apierror.ValidationError(map[string]string{
				"text": fmt.Sprintf("appending this text would take the body past %d bytes", maxDocumentBodyBytes),
			})
		}
		if upErr := s.storage.Upload(ctx, locked.StorageKey, strings.NewReader(joined),
			int64(len(joined)), documentContentType); upErr != nil {
			return apierror.InternalError("failed to upload document body to storage")
		}
		writtenBody = joined

		updatedBy, updatedByType := input.UpdatedBy, input.UpdatedByType
		locked.UpdatedAt = timeNow()
		locked.UpdatedBy = &updatedBy
		locked.UpdatedByType = &updatedByType
		return nil
	})
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	doc.Body = writtenBody
	// An append changes the body, so the index has to follow it or the added text
	// is unfindable until somebody happens to save the page some other way. Same
	// placement and the same reason as in Update: outside the lock, because
	// SetSearchText writes the row this transaction just held.
	s.indexBody(ctx, doc.ID, writtenBody)

	return doc, nil
}

// appendToBody joins an addition onto the end of a markdown body.
//
// Exactly one newline between them, and none added anywhere else. Markdown is
// line-sensitive — text run onto the end of the previous line becomes part of
// that paragraph, heading or list item rather than a new block — so a separator
// is needed, but a caller who ended their own text with a newline must not get a
// blank line they did not ask for. An empty document gets the addition verbatim:
// there is nothing to separate it from.
//
// The rule is deliberately simple enough to predict, because a test asserts the
// result byte for byte and so does anyone reading the page.
func appendToBody(existing, addition string) string {
	if existing == "" {
		return addition
	}
	if strings.HasSuffix(existing, "\n") {
		return existing + addition
	}
	return existing + "\n" + addition
}

// downloadBody fetches a document body from object storage.
//
// Shared by the single-document read and by append, which needs the current text
// to add to. A body that cannot be fetched is an error and never an empty
// string: an append that treated a failed read as "the document was empty" would
// replace the whole page with the text being appended.
func (s *documentService) downloadBody(ctx context.Context, storageKey string) (string, error) {
	rc, err := s.storage.Download(ctx, storageKey)
	if err != nil {
		return "", apierror.InternalError("failed to read document body from storage")
	}
	defer func() { _ = rc.Close() }()

	body, err := io.ReadAll(io.LimitReader(rc, maxDocumentBodyBytes))
	if err != nil {
		return "", apierror.InternalError("failed to read document body from storage")
	}
	return string(body), nil
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
