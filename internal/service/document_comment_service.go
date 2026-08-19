package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// maxCommentBodyBytes caps one comment at 16 KiB.
//
// A margin note is a sentence or two; the cap exists so a single row cannot be
// used to store a document inside the comment table, not to police length.
const maxCommentBodyBytes = 16 << 10

// maxAnchorFieldLen bounds each stored anchor string.
//
// The client writes at most 96 characters of quote and 32 of context either side
// (EXACT_CAP / CONTEXT_CAP in web/src/lib/docs/anchor.ts). This is deliberately
// looser — the server must not silently truncate an anchor into one that matches
// something else, so it refuses an oversized one instead, and the headroom means
// a client-side cap change is not a coordinated deploy.
const maxAnchorFieldLen = 4096

type documentCommentService struct {
	commentRepo  repository.DocumentCommentRepository
	documentRepo repository.DocumentRepository
}

// NewDocumentCommentService returns a DocumentCommentService backed by the given
// repositories.
func NewDocumentCommentService(
	commentRepo repository.DocumentCommentRepository,
	documentRepo repository.DocumentRepository,
) DocumentCommentService {
	return &documentCommentService{commentRepo: commentRepo, documentRepo: documentRepo}
}

// validateBody trims and bounds a comment body.
func validateBody(body string) (string, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return "", apierror.ValidationError(map[string]string{"body": "body is required"})
	}
	if len(trimmed) > maxCommentBodyBytes {
		return "", apierror.ValidationError(map[string]string{
			"body": fmt.Sprintf("body must be at most %d bytes", maxCommentBodyBytes),
		})
	}
	return trimmed, nil
}

// validateAnchor bounds an anchor before it is stored.
//
// The range checks mirror the CHECK constraints rather than trusting them: a
// constraint violation surfaces as a 500, and "you sent an empty range" deserves
// a field-level validation error that says so.
func validateAnchor(a *domain.DocumentAnchor) error {
	if a.Start < 0 {
		return apierror.ValidationError(map[string]string{"anchor.start": "must not be negative"})
	}
	// Half-open and non-empty. An empty range is not a comment on anything, and it
	// would match at every position in the document.
	if a.End <= a.Start {
		return apierror.ValidationError(map[string]string{"anchor.end": "must be greater than anchor.start"})
	}
	for field, value := range map[string]string{
		"anchor.exact":  a.Exact,
		"anchor.prefix": a.Prefix,
		"anchor.suffix": a.Suffix,
	} {
		if len(value) > maxAnchorFieldLen {
			return apierror.ValidationError(map[string]string{
				field: fmt.Sprintf("must be at most %d bytes", maxAnchorFieldLen),
			})
		}
	}
	// An anchor whose quote is empty cannot be resolved by quote, which is the
	// only step that survives an edit above it — it would fall straight through to
	// the context match on every read. Refusing it here is the difference between
	// a comment that can lose its place and one that never had one.
	if strings.TrimSpace(a.Exact) == "" {
		return apierror.ValidationError(map[string]string{"anchor.exact": "must not be blank"})
	}
	return nil
}

// requireDocument loads the document and refuses anything outside the caller's
// workspace. Every entry point goes through it: without it the service would act
// on whatever document id it was handed, including another tenant's or a deleted
// one — which would put live comments on a page its owner believes is gone.
func (s *documentCommentService) requireDocument(ctx context.Context, documentID, workspaceID uuid.UUID) error {
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, documentID, workspaceID)
	if err != nil {
		return err
	}
	if doc == nil {
		return apierror.NotFound("Document")
	}
	return nil
}

// requireComment loads a comment scoped to the caller's workspace.
func (s *documentCommentService) requireComment(ctx context.Context, id, workspaceID uuid.UUID) (*domain.DocumentComment, error) {
	c, err := s.commentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, apierror.NotFound("Comment")
	}
	return c, nil
}

func (s *documentCommentService) Create(ctx context.Context, input CreateDocumentCommentInput) (*domain.DocumentComment, error) {
	body, err := validateBody(input.Body)
	if err != nil {
		return nil, err
	}

	// Exactly one of the two. Checked here rather than left to
	// ck_document_comments_root_has_anchor so the caller gets a validation error
	// naming the field instead of a 500 from a violated constraint.
	switch {
	case input.ParentID == nil && input.Anchor == nil:
		return nil, apierror.ValidationError(map[string]string{
			"anchor": "a thread root must carry an anchor",
		})
	case input.ParentID != nil && input.Anchor != nil:
		return nil, apierror.ValidationError(map[string]string{
			"anchor": "a reply inherits its thread's anchor and must not carry one",
		})
	}

	if err := s.requireDocument(ctx, input.DocumentID, input.WorkspaceID); err != nil {
		return nil, err
	}

	if input.Anchor != nil {
		if err := validateAnchor(input.Anchor); err != nil {
			return nil, err
		}
	}

	if input.ParentID != nil {
		// The parent is loaded, not trusted: a reply must join a thread on THIS
		// document, or a caller could graft a reply from one page onto another —
		// where it would render under a root that never said what it answers.
		parent, err := s.requireComment(ctx, *input.ParentID, input.WorkspaceID)
		if err != nil {
			return nil, err
		}
		if parent.DocumentID != input.DocumentID {
			return nil, apierror.ValidationError(map[string]string{
				"parent_comment_id": "parent comment belongs to another document",
			})
		}
	}

	now := time.Now().UTC()
	c := &domain.DocumentComment{
		ID:         uuid.New(),
		DocumentID: input.DocumentID,
		ParentID:   input.ParentID,
		Body:       body,
		Anchor:     input.Anchor,
		AuthorID:   input.AuthorID,
		AuthorType: input.AuthorType,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.commentRepo.Create(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *documentCommentService) ListByDocument(ctx context.Context, documentID, workspaceID uuid.UUID) ([]domain.DocumentComment, error) {
	if err := s.requireDocument(ctx, documentID, workspaceID); err != nil {
		return nil, err
	}
	return s.commentRepo.ListByDocument(ctx, documentID)
}

func (s *documentCommentService) UpdateBody(ctx context.Context, id, workspaceID uuid.UUID, body string, editorID uuid.UUID) (*domain.DocumentComment, error) {
	trimmed, err := validateBody(body)
	if err != nil {
		return nil, err
	}
	c, err := s.requireComment(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	// A comment is attributed speech. Letting anyone else rewrite it would put
	// words in its author's mouth under their name, which no amount of audit trail
	// undoes.
	if c.AuthorID != editorID {
		return nil, apierror.Forbidden("only the author can edit a comment")
	}

	now := time.Now().UTC()
	if err := s.commentRepo.UpdateBody(ctx, id, trimmed, now); err != nil {
		return nil, err
	}
	c.Body = trimmed
	c.UpdatedAt = now
	return c, nil
}

func (s *documentCommentService) SetResolved(ctx context.Context, id, workspaceID uuid.UUID, resolved bool, actorID uuid.UUID, actorType domain.ActorType) (*domain.DocumentComment, error) {
	c, err := s.requireComment(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	// Resolving is a property of the thread, so a reply has nothing to resolve.
	// Said here as a validation error naming the reason; the repository's WHERE clause is the
	// backstop that keeps a race from writing one anyway.
	if !c.IsRoot() {
		return nil, apierror.ValidationError(map[string]string{
			"id": "only a thread root can be resolved",
		})
	}

	now := time.Now().UTC()
	var (
		by     *uuid.UUID
		byType *domain.ActorType
	)
	if resolved {
		by, byType = &actorID, &actorType
	}
	if err := s.commentRepo.SetResolved(ctx, id, by, byType, now); err != nil {
		return nil, err
	}

	c.ResolvedBy, c.ResolvedByType, c.UpdatedAt = by, byType, now
	if resolved {
		c.ResolvedAt = &now
	} else {
		c.ResolvedAt = nil
	}
	return c, nil
}

func (s *documentCommentService) Delete(ctx context.Context, id, workspaceID, actorID uuid.UUID) error {
	c, err := s.requireComment(ctx, id, workspaceID)
	if err != nil {
		return err
	}
	if c.AuthorID != actorID {
		return apierror.Forbidden("only the author can delete a comment")
	}
	return s.commentRepo.SoftDelete(ctx, id, time.Now().UTC())
}
