package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/textanchor"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// maxDocumentCommentBodyBytes caps a single comment. Generous enough for a
// paragraph of review feedback with a code block in it, small enough that the
// column is not a place to paste a document.
const maxDocumentCommentBodyBytes = 16 << 10 // 16 KiB

// maxAnchorTextBytes caps each of the anchor's three text fields.
//
// The quote is an identifier, not content: it exists so the range can be found
// again, and a few hundred characters of it are already more than enough to be
// unique in a 5 MiB body. A cap is what stops a client from storing the whole
// document a second time, once per comment, in the name of "context".
const maxAnchorTextBytes = 2000

type documentCommentService struct {
	commentRepo  repository.DocumentCommentRepository
	documentRepo repository.DocumentRepository
	// bodies reads the markdown, and only for the quote path. A nil one is allowed
	// — same arrangement as a nil object store elsewhere — and makes a quote
	// answer 503 while every other kind of comment keeps working.
	bodies DocumentBodyReader
}

// NewDocumentCommentService returns a DocumentCommentService backed by the given
// repositories.
//
// It takes the document repository as well as its own, and that is the tenancy
// check rather than a convenience: every entry point resolves the document inside
// the caller's workspace first, so a comment id or a document id from another
// tenant has nothing to attach to.
//
// bodies is the document-body reader the quote resolver needs. It is separate
// from documentRepo because reading a body is a round-trip to object storage, and
// only one of the five entry points here ever wants one: charging every comment
// for it would be paying an S3 fetch to write a reply. May be nil.
func NewDocumentCommentService(
	commentRepo repository.DocumentCommentRepository,
	documentRepo repository.DocumentRepository,
	bodies DocumentBodyReader,
) DocumentCommentService {
	return &documentCommentService{commentRepo: commentRepo, documentRepo: documentRepo, bodies: bodies}
}

// Create records a comment on a document, optionally as a reply.
func (s *documentCommentService) Create(ctx context.Context, input CreateDocumentCommentInput) (*domain.DocumentComment, error) {
	body := strings.TrimSpace(input.Body)
	if body == "" {
		return nil, apierror.ValidationError(map[string]string{"body": "body is required"})
	}
	if len(body) > maxDocumentCommentBodyBytes {
		return nil, apierror.ValidationError(map[string]string{
			"body": fmt.Sprintf("body must be at most %d bytes", maxDocumentCommentBodyBytes),
		})
	}

	// The document is loaded first, and inside the caller's workspace: it is the
	// ownership check, and a comment on a deleted or foreign page must be a 404
	// rather than a row nobody can reach.
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, input.DocumentID, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	var parentID *uuid.UUID
	if input.ParentCommentID != nil {
		parent, perr := s.requireReplyableParent(ctx, *input.ParentCommentID, doc.ID)
		if perr != nil {
			return nil, perr
		}
		// A reply with its own anchor is two claims about what one thread is about,
		// and nothing keeps them pointing at the same words once the document is
		// edited. The reply inherits the root's anchor instead.
		//
		// Refused here, before the anchor is worked out, so that a reply carrying a
		// quote is told the actual problem rather than "no such quote" from a
		// resolution that should never have been attempted — and so that it does not
		// pay a body fetch to be turned away. Named by whichever field the caller
		// sent, so an agent is not told about a field it never used.
		if input.Anchor != nil || strings.TrimSpace(input.Quote) != "" {
			field := "anchor"
			if strings.TrimSpace(input.Quote) != "" {
				field = "quote"
			}
			return nil, apierror.ValidationError(map[string]string{
				field: "a reply inherits its parent's anchor and cannot carry one of its own",
			})
		}
		id := parent.ID
		parentID = &id
	}

	anchor, err := s.resolveAnchor(ctx, input, doc)
	if err != nil {
		return nil, err
	}

	now := timeNow()
	comment := &domain.DocumentComment{
		ID:              uuid.New(),
		DocumentID:      doc.ID,
		ParentCommentID: parentID,
		AuthorID:        input.AuthorID,
		AuthorType:      input.AuthorType,
		Body:            body,
		Anchor:          anchor,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if createErr := s.commentRepo.Create(ctx, comment); createErr != nil {
		return nil, createErr
	}

	return s.enriched(ctx, comment), nil
}

// ListByDocument returns a page of the document's live comments.
func (s *documentCommentService) ListByDocument(
	ctx context.Context,
	documentID, workspaceID uuid.UUID,
	filter repository.DocumentCommentFilter,
	pg pagination.Params,
) (*pagination.Page[domain.DocumentComment], error) {
	// The document is checked first so a listing for another tenant's document is
	// a 404 rather than an empty page — an empty page is an answer, and answering
	// at all confirms which ids exist.
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, documentID, workspaceID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, apierror.NotFound("Document")
	}

	pg.Normalize()
	return s.commentRepo.ListByDocument(ctx, documentID, filter, pg)
}

// Update edits the body of the caller's own comment.
func (s *documentCommentService) Update(
	ctx context.Context,
	id, workspaceID uuid.UUID,
	input UpdateDocumentCommentInput,
) (*domain.DocumentComment, error) {
	body := strings.TrimSpace(input.Body)
	if body == "" {
		return nil, apierror.ValidationError(map[string]string{"body": "body is required"})
	}
	if len(body) > maxDocumentCommentBodyBytes {
		return nil, apierror.ValidationError(map[string]string{
			"body": fmt.Sprintf("body must be at most %d bytes", maxDocumentCommentBodyBytes),
		})
	}

	comment, err := s.requireComment(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}

	// Same rule as a task comment, and the same wording: editing is not a
	// moderation power, it is authorship. Both halves of the identity are
	// compared — an agent and a user can share neither id nor meaning, but
	// comparing only the uuid would make that an assumption rather than a check.
	if comment.AuthorID != input.EditorID || comment.AuthorType != input.EditorType {
		return nil, apierror.Forbidden("you can only edit your own comments")
	}

	comment.Body = body
	comment.UpdatedAt = timeNow()
	if updErr := s.commentRepo.Update(ctx, comment); updErr != nil {
		return nil, updErr
	}

	return s.enriched(ctx, comment), nil
}

// SetResolved resolves or unresolves a thread.
func (s *documentCommentService) SetResolved(
	ctx context.Context,
	id, workspaceID uuid.UUID,
	input ResolveDocumentCommentInput,
) (*domain.DocumentComment, error) {
	comment, err := s.requireComment(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}

	// Resolution belongs to the conversation, not to a line in it. Allowing a
	// reply to be resolved on its own would let a thread be half-resolved, which
	// the listing filter — which hides a thread by its root — could not represent.
	if comment.IsReply() {
		return nil, apierror.ValidationError(map[string]string{
			"comment_id": "resolve the thread's first comment, not a reply to it",
		})
	}

	// Idempotent in both directions, and deliberately not a no-op that returns
	// early on the resolve side only: re-resolving must not rewrite resolved_by,
	// or the last person to click a button already in that state takes credit for
	// somebody else's work.
	if comment.IsResolved() == input.Resolved {
		return comment, nil
	}

	if input.Resolved {
		now := timeNow()
		actorID, actorType := input.ActorID, input.ActorType
		comment.ResolvedAt = &now
		comment.ResolvedBy = &actorID
		comment.ResolvedByType = &actorType
	} else {
		// All three together — the schema refuses two of three, and a half-cleared
		// resolution would leave "unresolved, by Ann" in the row.
		comment.ResolvedAt = nil
		comment.ResolvedBy = nil
		comment.ResolvedByType = nil
	}

	comment.UpdatedAt = timeNow()
	if updErr := s.commentRepo.Update(ctx, comment); updErr != nil {
		return nil, updErr
	}

	return s.enriched(ctx, comment), nil
}

// Delete soft-deletes the caller's own comment and its replies.
func (s *documentCommentService) Delete(ctx context.Context, id, workspaceID, actorID uuid.UUID, actorType domain.ActorType) error {
	comment, err := s.requireComment(ctx, id, workspaceID)
	if err != nil {
		return err
	}
	if comment.AuthorID != actorID || comment.AuthorType != actorType {
		return apierror.Forbidden("you can only delete your own comments")
	}
	return s.commentRepo.SoftDelete(ctx, id, timeNow())
}

// requireComment loads a comment inside the caller's tenant, or 404s.
//
// Every object-scoped entry point goes through it: the workspace narrows the
// lookup to the caller's own tenant, which is defense-in-depth behind the route
// guard rather than a substitute for it, and nil becomes "Comment not found" so a
// stranger's id and a nonexistent one give the same answer.
func (s *documentCommentService) requireComment(ctx context.Context, id, workspaceID uuid.UUID) (*domain.DocumentComment, error) {
	comment, err := s.commentRepo.GetByIDInWorkspace(ctx, id, workspaceID)
	if err != nil {
		return nil, err
	}
	if comment == nil {
		return nil, apierror.NotFound("Comment")
	}
	return comment, nil
}

// requireReplyableParent refuses a parent that does not exist, lives on another
// document, or is itself a reply.
//
// The same-document check is not redundant with the workspace check above it: two
// documents in one tenant are two conversations, and a comment whose parent is on
// a different page would appear in one thread and be listed under another.
//
// Refusing a reply to a reply keeps threads one level deep, which is what makes
// the listing's resolved-thread filter a single COALESCE to the root and the
// delete a single JOIN rather than a recursive walk. Flattening it silently
// instead would answer 201 to a request the caller can only discover was
// reinterpreted by reading the response.
func (s *documentCommentService) requireReplyableParent(ctx context.Context, parentID, documentID uuid.UUID) (*domain.DocumentComment, error) {
	parent, err := s.commentRepo.GetByID(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.DocumentID != documentID {
		return nil, apierror.ValidationError(map[string]string{
			"parent_comment_id": "parent comment not found on this document",
		})
	}
	if parent.IsReply() {
		return nil, apierror.ValidationError(map[string]string{
			"parent_comment_id": "replies are one level deep; reply to the thread's first comment",
		})
	}
	return parent, nil
}

// enriched re-reads the comment so the caller gets the resolved display names
// alongside the ids, matching what the listing returns for the same row.
//
// A failure falls back to the in-memory copy rather than erroring: the write
// already succeeded, and answering 500 here would tell the caller their comment
// was lost when it is in the table.
func (s *documentCommentService) enriched(ctx context.Context, comment *domain.DocumentComment) *domain.DocumentComment {
	if fresh, err := s.commentRepo.GetByID(ctx, comment.ID); err == nil && fresh != nil {
		return fresh
	}
	return comment
}

// validateAnchor checks the selector pair and normalizes it, returning nil for a
// comment that is not anchored to anything.
//
// The states it enforces are the ones the schema's CHECK constraints describe,
// applied here so a client gets a 400 naming the field rather than a 500 from a
// constraint violation:
//
//   - no quote, no offsets      -> not anchored (a comment on the whole document)
//   - quote, offsets            -> anchored
//   - quote, no offsets         -> orphaned, and legitimately writable: a client
//     that failed to re-find the range can say so rather than lose the comment
//   - offsets, no quote         -> refused. It is the one shape that can never be
//     recovered after an edit, and it is indistinguishable from a bug that dropped
//     the quote on the way in.
func validateAnchor(anchor *domain.DocumentCommentAnchor) (*domain.DocumentCommentAnchor, error) {
	if anchor == nil {
		return nil, nil
	}

	exact := anchor.Exact
	if exact == "" {
		if anchor.Start != nil || anchor.End != nil || anchor.Prefix != "" || anchor.Suffix != "" {
			return nil, apierror.ValidationError(map[string]string{
				"anchor.exact": "anchor.exact is required when any other anchor field is given",
			})
		}
		return nil, nil
	}

	for field, value := range map[string]string{
		"anchor.exact":  exact,
		"anchor.prefix": anchor.Prefix,
		"anchor.suffix": anchor.Suffix,
	} {
		if len(value) > maxAnchorTextBytes {
			return nil, apierror.ValidationError(map[string]string{
				field: fmt.Sprintf("%s must be at most %d bytes", field, maxAnchorTextBytes),
			})
		}
	}

	if (anchor.Start == nil) != (anchor.End == nil) {
		return nil, apierror.ValidationError(map[string]string{
			"anchor.start": "anchor.start and anchor.end must be given together",
		})
	}
	if anchor.Start != nil {
		if *anchor.Start < 0 {
			return nil, apierror.ValidationError(map[string]string{
				"anchor.start": "anchor.start must not be negative",
			})
		}
		// Half-open [start, end), so an empty range is not a selection of anything
		// and would highlight nothing on the way back.
		if *anchor.End <= *anchor.Start {
			return nil, apierror.ValidationError(map[string]string{
				"anchor.end": "anchor.end must be greater than anchor.start",
			})
		}
	}

	return domain.NewDocumentCommentAnchor(exact, anchor.Prefix, anchor.Suffix, anchor.Start, anchor.End), nil
}

// resolveAnchor turns whatever the caller said about position into a validated
// anchor: offsets they measured, or a quote the server measures for them.
//
// The two are mutually exclusive by design and not by oversight. Accepting both
// would mean accepting offsets from a caller that also admits it does not know
// where the text is, and then having to decide which of the two to believe —
// silently, on every write.
func (s *documentCommentService) resolveAnchor(
	ctx context.Context,
	input CreateDocumentCommentInput,
	doc *domain.Document,
) (*domain.DocumentCommentAnchor, error) {
	quote := strings.TrimSpace(input.Quote)
	hasContext := input.QuoteContext != "" || input.QuotePrefix != "" || input.QuoteSuffix != ""

	if quote == "" {
		if hasContext {
			return nil, apierror.ValidationError(map[string]string{
				"quote": "quote is required when quote context is given",
			})
		}
		return validateAnchor(input.Anchor)
	}
	if input.Anchor != nil {
		return nil, apierror.ValidationError(map[string]string{
			"quote": "give either a quote or an anchor, not both: an anchor carries offsets and a quote asks the server to compute them",
		})
	}
	if len(quote) > maxAnchorTextBytes {
		return nil, apierror.ValidationError(map[string]string{
			"quote": fmt.Sprintf("quote must be at most %d bytes", maxAnchorTextBytes),
		})
	}

	body, err := s.documentBody(ctx, doc.ID, input.WorkspaceID)
	if err != nil {
		return nil, err
	}

	anchor, err := textanchor.Resolve(body, quote, textanchor.Context{
		Prefix:   input.QuotePrefix,
		Suffix:   input.QuoteSuffix,
		Fragment: input.QuoteContext,
	})
	if err != nil {
		return nil, quoteError(err)
	}

	start, end := anchor.Start, anchor.End
	return domain.NewDocumentCommentAnchor(anchor.Exact, anchor.Prefix, anchor.Suffix, &start, &end), nil
}

// documentBody fetches the markdown the quote is resolved against.
func (s *documentCommentService) documentBody(ctx context.Context, documentID, workspaceID uuid.UUID) (string, error) {
	if s.bodies == nil {
		return "", apierror.ServiceUnavailable("storage backend not configured; a quote cannot be located without the document body")
	}
	doc, err := s.bodies.GetByIDInWorkspace(ctx, documentID, workspaceID)
	if err != nil {
		return "", err
	}
	if doc == nil {
		return "", apierror.NotFound("Document")
	}
	if doc.Body == "" {
		// Said plainly rather than left to fall through the resolver, which would
		// answer "no such quote in the document" — true, and indistinguishable from
		// a body reader wired to something that does not fetch bodies. A refusal
		// that reads as a normal outcome is how a misconfiguration survives.
		return "", apierror.ValidationError(map[string]string{
			"quote": "the document has no body to search for a quote in",
		})
	}
	return doc.Body, nil
}

// quoteError turns a resolver refusal into the 400 the caller can act on.
//
// Each one names the field and says what to do next, because the caller is an
// agent with no screen: "ambiguous" is not actionable, "occurs 4 times, narrow it
// with quote_context" is. The match count travels in the message for the same
// reason.
func quoteError(err error) error {
	var notFound *textanchor.NotFoundError
	if errors.As(err, &notFound) {
		return apierror.ValidationError(map[string]string{
			"quote": "no such quote in the document; quote the text exactly as it appears",
		})
	}

	var ambiguous *textanchor.AmbiguousError
	if errors.As(err, &ambiguous) {
		occurs := fmt.Sprintf("%d times", ambiguous.Matches)
		if ambiguous.AtLeast {
			occurs = fmt.Sprintf("at least %d times", ambiguous.Matches)
		}
		return apierror.ValidationError(map[string]string{
			"quote": fmt.Sprintf(
				"this quote occurs %s in the document; narrow it with quote_context, or with quote_prefix/quote_suffix",
				occurs),
		})
	}

	var contextErr *textanchor.ContextError
	if errors.As(err, &contextErr) {
		return apierror.ValidationError(map[string]string{"quote_context": contextErr.Error()})
	}

	if errors.Is(err, textanchor.ErrEmptyQuote) {
		return apierror.ValidationError(map[string]string{"quote": "quote is required"})
	}

	// Not a refusal this function knows how to explain. Passing it through
	// unlabelled would be worse than saying so.
	return apierror.Wrap(err)
}
