package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
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

	// watch is optional. Without it documents behave exactly as they did before
	// subscriptions existed: nothing is recorded, nothing is announced. It is a
	// dependency of the write paths rather than the other way round because a
	// document must remain editable on a deployment where notifications are not
	// wired up at all.
	watch DocumentWatchService
}

// DocumentServiceOption configures optional collaborators.
type DocumentServiceOption func(*documentService)

// WithDocumentWatch wires the subscription service into the document write
// paths: the author is subscribed on create, every edit folds into a pending
// change-notice, and a delete tells the watchers before their rows go with it.
func WithDocumentWatch(w DocumentWatchService) DocumentServiceOption {
	return func(s *documentService) { s.watch = w }
}

// NewDocumentService returns a DocumentService backed by the given repositories
// and object storage. A nil store is allowed and every call that needs a body
// answers 503 — same arrangement as the artifact service, so a deployment
// without object storage still serves the rest of the API.
func NewDocumentService(
	documentRepo repository.DocumentRepository,
	store DocumentStore,
	projectRepo repository.ProjectRepository,
	opts ...DocumentServiceOption,
) DocumentService {
	svc := &documentService{
		documentRepo: documentRepo,
		storage:      store,
		projectRepo:  projectRepo,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
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
		ID:         id,
		ProjectID:  input.ProjectID,
		ParentID:   input.ParentID,
		Slug:       slug,
		Title:      title,
		StorageKey: storageKey,
		Position:   input.Position,
		// The first revision. A document that came back as version 0 would be
		// refused by its own first conditional write.
		Version:       1,
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

	// You should not learn about your own page last. Auto-subscribing the author
	// is what makes the feature useful without anybody having to discover a
	// button; it is safe to make automatic only because unsubscribing leaves a
	// tombstone that the automatic path will not overwrite.
	if s.watch != nil {
		s.watch.AutoSubscribe(ctx, doc.ID, createdBy, string(createdByType), domain.WatchSourceAuthor)
	}

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

// maxAppendRetries bounds the re-read loop an append does when another write
// lands between reading the document and claiming its version. Three is enough
// for any realistic contention on one page; an unbounded loop under a hot writer
// is a request that never returns.
const maxAppendRetries = 3

// Update applies a partial change to a document's title, place in the tree,
// sibling position and body.
//
// ## Conditional writes, and what an absent base_version means
//
// A document is a shared mutable object with no history: the body is one object
// in storage, overwritten in place. Unconditional write over that is how two
// agents overwrote each other's work on 2026-08-19, and in prose the loss is
// silent — it does not look wrong the way a missing form field does.
//
// So a caller may send BaseVersion and get a conflict instead of a silent
// overwrite. The decision that matters is what an ABSENT BaseVersion means, and
// it means an unconditional write — the behaviour every existing caller already
// has. The alternative, making it mandatory, was rejected:
//
//   - PATCH /documents/:doc_id is live and the editor's autosave uses it. Making
//     the field required turns a shipped endpoint into a 400 for every current
//     client the moment this deploys, to fix a race those clients are not in:
//     the editor is one human in one tab, and its own last-write-wins is a
//     property of the UI, not an accident.
//   - The callers this protects are the agents, and the agent-facing surface does
//     not exist yet. It can be built to always send base_version, which is a
//     stronger guarantee than a required field enforced against a client that
//     would only send back whatever it last read anyway.
//   - The enforcement lives here, in the service, and not in the handler, so
//     every future caller — MCP included — inherits it without re-implementing
//     the compare.
//
// The cost is stated plainly: a caller who does not ask is not protected. The
// follow-up that closes it is a frontend change — send the version from the last
// read as base_version and reload on 409 — which is small precisely because
// version is now on every document the API returns.
//
// ## Why the row is written before the body
//
// Create uploads the body first, so that the row never advertises content that is
// not there. This path reverses that, on purpose. The conditional UPDATE is the
// compare-and-swap: it has to be the thing that decides whether the write happens
// at all, and anything written before it is written by a caller who might still
// lose. Uploading first would mean a refused write had already overwritten the
// body it was refused for — which is exactly the data loss the version exists to
// prevent, now with a 409 on top of it.
//
// The failure this ordering leaves is a body upload that fails after the row was
// written: the version has moved but the stored markdown has not. That is a 500
// the caller sees and retries, and every conditional writer is forced to re-read
// — the safe direction. The other ordering's failure is silent and permanent.
func (s *documentService) Update(ctx context.Context, id, workspaceID uuid.UUID, input UpdateDocumentInput) (*domain.Document, error) {
	if input.Body != nil && input.AppendBody != nil {
		return nil, apierror.ValidationError(map[string]string{
			"append_body": "body and append_body cannot be sent together; replace or append, not both",
		})
	}

	for attempt := 0; ; attempt++ {
		doc, err := s.updateOnce(ctx, id, workspaceID, input)
		if err == nil {
			return doc, nil
		}
		// An append is a read-modify-write, so a version it loses is not a conflict
		// to report — it is a stale read to redo. Re-running it re-downloads the
		// body the winner wrote and appends to that instead, which is what the
		// caller asked for. A caller who sent BaseVersion asked to be told instead,
		// and updateOnce has already refused before reaching here.
		if input.AppendBody == nil || input.BaseVersion != nil || attempt >= maxAppendRetries {
			return nil, err
		}
		var conflict *DocumentVersionConflictError
		if !errors.As(err, &conflict) {
			return nil, err
		}
	}
}

// updateOnce is one attempt at Update: read, validate, claim the version, write
// the body. Split out so that an append can retry the whole read-modify-write
// rather than half of it.
func (s *documentService) updateOnce(ctx context.Context, id, workspaceID uuid.UUID, input UpdateDocumentInput) (*domain.Document, error) {
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	// Refuse a stale base_version against what we just read, before anything is
	// validated or fetched. The repository re-checks it inside the UPDATE, which is
	// what makes it race-free; this check is what makes the ordinary case cheap and
	// what stops an append's retry loop from spinning on a caller who asked to be
	// told about conflicts.
	if input.BaseVersion != nil && *input.BaseVersion != doc.Version {
		return nil, &DocumentVersionConflictError{CurrentVersion: doc.Version}
	}

	// Read before anything mutates doc, so the notice can name the span it
	// covers even when several writes fold into one.
	versionBefore := doc.Version

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

	// Work out the markdown to store, if any, before anything is written. An
	// append has to read the current body to produce it, and a body that cannot be
	// read is a body that must not be appended to — concatenating onto "" would
	// replace the document with its own last paragraph.
	newBody, err := s.resolveBodyWrite(ctx, doc, input)
	if err != nil {
		return nil, err
	}

	// The version tracks the document's CONTENT. A move in the tree changes where
	// the page is filed, not a word of it, and bumping there would 409 every
	// editor in the project during a reorganisation.
	contentChanged := input.Title != nil || newBody != nil

	// Stamped on every path that reaches the write, not per-field: a caller who
	// only moved the document in the tree still changed it, and "last updated by"
	// that skipped some kinds of change would name the wrong person the rest of
	// the time.
	updatedBy, updatedByType := input.UpdatedBy, input.UpdatedByType
	doc.UpdatedAt = timeNow()
	doc.UpdatedBy = &updatedBy
	doc.UpdatedByType = &updatedByType

	// An append is always conditional on the version it read, even when the caller
	// sent no base_version: that is what makes two concurrent appends serialise
	// into a retry instead of one of them disappearing. A replacement is
	// conditional only when the caller asked for it — see the doc comment.
	expected := input.BaseVersion
	if input.AppendBody != nil {
		seen := doc.Version
		expected = &seen
	}

	newVersion, upErr := s.documentRepo.Update(ctx, doc, expected, contentChanged)
	if errors.Is(upErr, repository.ErrDocumentVersionMismatch) {
		return nil, &DocumentVersionConflictError{CurrentVersion: newVersion}
	}
	if upErr != nil {
		return nil, upErr
	}
	doc.Version = newVersion

	// The row is written, so this write has won. Only now is it safe to overwrite
	// the body: a refused write must leave the previous markdown intact.
	if newBody != nil {
		if err = s.storage.Upload(ctx, doc.StorageKey, strings.NewReader(*newBody), int64(len(*newBody)), documentContentType); err != nil {
			return nil, apierror.InternalError("failed to upload document body to storage")
		}
		doc.Body = *newBody
	}

	// Only when the body actually changed. A rename or a move must not rewrite a
	// megabyte of search text to store the same bytes back. An append counts: the
	// document it produced is a different document to search.
	if newBody != nil {
		s.indexBody(ctx, doc.ID, *newBody)
	}

	// Fold this write into the pending change-notice for its author. One row
	// UPDATE, no delivery: the editor autosaves every two seconds, and the whole
	// point of the notice is that a hundred of these produce one notification
	// once the author stops typing. A move in the tree records nothing —
	// contentChanged is false for it, and it does not even bump the version.
	if s.watch != nil && contentChanged {
		s.watch.RecordChange(ctx, RecordDocumentChangeInput{
			Document:     doc,
			WorkspaceID:  workspaceID,
			ActorID:      updatedBy,
			ActorKind:    string(updatedByType),
			FromVersion:  versionBefore,
			ToVersion:    doc.Version,
			TitleChanged: input.Title != nil,
			BodyChanged:  newBody != nil,
		})
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

// resolveBodyWrite returns the markdown this update should store, or nil when it
// does not touch the body. It performs no writes.
func (s *documentService) resolveBodyWrite(ctx context.Context, doc *domain.Document, input UpdateDocumentInput) (*string, error) {
	if input.Body == nil && input.AppendBody == nil {
		return nil, nil
	}
	if s.storage == nil {
		return nil, apierror.ServiceUnavailable("storage backend not configured")
	}

	if input.Body != nil {
		if len(*input.Body) > maxDocumentBodyBytes {
			return nil, apierror.ValidationError(map[string]string{
				"body": fmt.Sprintf("body must be at most %d bytes", maxDocumentBodyBytes),
			})
		}
		return input.Body, nil
	}

	current, err := s.downloadBody(ctx, doc.StorageKey)
	if err != nil {
		return nil, err
	}
	if len(current)+len(*input.AppendBody) > maxDocumentBodyBytes {
		return nil, apierror.ValidationError(map[string]string{
			"append_body": fmt.Sprintf("body must be at most %d bytes; the document is already %d",
				maxDocumentBodyBytes, len(current)),
		})
	}
	appended := current + *input.AppendBody
	return &appended, nil
}

// downloadBody fetches a document's markdown from object storage.
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

// DocumentVersionConflictError is returned by DocumentService.Update when a
// conditional write's base_version no longer matches the stored one. Nothing was
// written: not the row, and not the markdown in object storage.
//
// It carries the version the document is actually at so the caller can re-read
// that exact revision and retry, rather than guessing at a number or polling.
type DocumentVersionConflictError struct {
	CurrentVersion int `json:"current_version"`
}

func (e *DocumentVersionConflictError) Error() string {
	return fmt.Sprintf("document_version_conflict: document is at version %d", e.CurrentVersion)
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
	if delErr := s.documentRepo.SoftDelete(ctx, id, timeNow(), deletedBy, deletedByType); delErr != nil {
		return delErr
	}

	// Announced after the delete succeeded, never before: a notification saying
	// a page is gone, sent for a delete that then failed, is worse than no
	// notification at all.
	//
	// Only this document, not the descendants it took with it. A subtree delete
	// notifying every watcher of every page under it is a fan-out nobody asked
	// for, and the notification for the parent is the one that explains what
	// happened.
	if s.watch != nil {
		s.watch.NotifyDeleted(ctx, doc, workspaceID)
	}
	return nil
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

// Outline returns the document's heading structure.
func (s *documentService) Outline(ctx context.Context, id, workspaceID uuid.UUID) (*DocumentOutline, error) {
	doc, err := s.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	return &DocumentOutline{
		DocumentID: doc.ID,
		Title:      doc.Title,
		Version:    doc.Version,
		Outline:    mdoc.Outline(doc.Body),
	}, nil
}

// Section returns one heading of a document and the markdown it owns.
func (s *documentService) Section(ctx context.Context, id, workspaceID uuid.UUID, ref string) (*DocumentSection, error) {
	doc, err := s.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}

	section, err := mdoc.FindSection(doc.Body, ref)
	if err != nil {
		return nil, sectionLookupError(err)
	}

	return &DocumentSection{
		DocumentID: doc.ID,
		Version:    doc.Version,
		Heading:    section.Heading,
		Content:    section.Content,
	}, nil
}

// sectionLookupError maps a heading lookup failure onto the HTTP shape it should
// have. Both answers carry the anchors, because a caller that named the wrong
// heading needs the list far more than it needs the status code.
func sectionLookupError(err error) error {
	var notFound *mdoc.HeadingNotFoundError
	if errors.As(err, &notFound) {
		return apierror.NotFoundWithDetails("Heading", err.Error())
	}
	var ambiguous *mdoc.AmbiguousHeadingError
	if errors.As(err, &ambiguous) {
		// 400, not 404: the heading exists, more than once, and it is the request
		// that is under-specified. Answering "not found" would send the caller
		// looking for a heading that is right there.
		return apierror.BadRequestWithDetails("Ambiguous heading", err.Error())
	}
	return err
}

// GetByPath resolves a slug path within a project to the document it names, with
// its body.
//
// ## What a path is, and what happens when a document moves
//
// A path is a lookup key derived from where a document currently sits, not an
// identifier for it. Move `architecture/adr/adr-004` under `decisions/` and the
// old path resolves to nothing; worse, a later document that takes the slug
// `adr-004` in the old place will answer to it. The path is not versioned, not
// aliased and not redirected.
//
// That is accepted rather than fixed, for two reasons. The document's uuid is
// still the stable identifier and every path lookup returns it, so an agent that
// needs a reference to survive a reorganisation stores the id — the same
// discipline a link in prose needs. And the alternative is a redirect table with
// its own history and its own staleness, bought to make a convenience addressing
// scheme behave like an identity it was never going to have. Filesystems and
// wikis behave exactly this way and nobody is surprised by it.
func (s *documentService) GetByPath(ctx context.Context, projectID uuid.UUID, path string) (*domain.Document, error) {
	segments := splitDocumentPath(path)
	if len(segments) == 0 {
		return nil, apierror.ValidationError(map[string]string{
			"path": "path is required, as a slash-separated slug path — e.g. architecture/adr/adr-004",
		})
	}

	doc, resolved, err := s.documentRepo.GetByPathInProject(ctx, projectID, segments)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		// Naming the segment that failed is the difference between an error a
		// caller can act on and one that only says no. "adr/adr-4 does not exist
		// under architecture" sends them to look at the tree; "not found" sends
		// them to re-read their own code.
		return nil, apierror.NotFoundWithDetails("Document", fmt.Sprintf(
			"path resolves as far as %q; there is no live document with slug %q under it",
			strings.Join(segments[:resolved], "/"), segments[resolved]))
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

// splitDocumentPath turns "/architecture/adr/adr-004/" into its slugs, dropping
// the empty segments a leading, trailing or doubled slash produces. A path is
// typed by hand and by agents; refusing it over a stray slash would be pedantry
// with no safety behind it.
func splitDocumentPath(path string) []string {
	parts := strings.Split(path, "/")
	segments := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			segments = append(segments, p)
		}
	}
	return segments
}

// ResolveAnchor locates a quotation in the document's current body and returns
// the anchor for it — see pkg/mdoc for why this is computed here and not by the
// caller.
func (s *documentService) ResolveAnchor(ctx context.Context, id, workspaceID uuid.UUID, input ResolveAnchorInput) (*mdoc.Anchor, error) {
	doc, err := s.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}

	anchor, err := mdoc.ResolveQuote(doc.Body, input.Quote, input.Prefix, input.Suffix)
	if err != nil {
		return nil, anchorResolveError(err)
	}
	return anchor, nil
}

// anchorResolveError maps a resolution failure onto its HTTP shape. The ambiguous
// case is passed through untouched: the handler renders the match count, and
// flattening it into a string here would throw away the one number the caller
// needs to decide what to do next.
func anchorResolveError(err error) error {
	var empty *mdoc.EmptyQuoteError
	if errors.As(err, &empty) {
		return apierror.ValidationError(map[string]string{"quote": "quote is required"})
	}
	var tooLong *mdoc.QuoteTooLongError
	if errors.As(err, &tooLong) {
		return apierror.ValidationError(map[string]string{"quote": err.Error()})
	}
	var notFound *mdoc.QuoteNotFoundError
	if errors.As(err, &notFound) {
		// 400 rather than 404: the document was found, and it is the quote — a
		// value in the request — that does not match anything in it.
		return apierror.BadRequestWithDetails("No such quote in this document", err.Error())
	}
	return err
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
