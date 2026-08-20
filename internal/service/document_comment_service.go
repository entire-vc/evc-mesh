package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/mdoc"
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

// DocumentBodyReader loads a document together with its markdown, inside a
// workspace. It is the narrow slice of DocumentService this file needs: the body
// is fetched from object storage by the service, not by the repository, and the
// anchor guard below cannot check an offset without the text it points into.
type DocumentBodyReader interface {
	GetByIDInWorkspace(ctx context.Context, id, workspaceID uuid.UUID) (*domain.Document, error)
}

type documentCommentService struct {
	commentRepo  repository.DocumentCommentRepository
	documentRepo repository.DocumentRepository

	// documentBody loads the markdown an anchor's offsets are checked against.
	// Required, not an option: with it absent the anchor guard fails closed and
	// every anchored comment is refused, so a call site that forgot it must fail
	// to compile rather than to run.
	documentBody DocumentBodyReader

	// The @-mention dependencies, all optional and all nil-checked at the point
	// of use. They are options rather than constructor arguments because the
	// comment CRUD above is complete without any of them, and a constructor
	// taking eight collaborators to support one feature makes every test that
	// does not care about mentions build seven mocks that do nothing.
	//
	// The one thing a nil dependency must never do is make a mention look
	// delivered — see mentionsEnabled, which turns "cannot resolve anything" into
	// a log line rather than a silent no-op.
	agentSvc       AgentService
	userRepo       repository.UserRepository
	mentionRepo    repository.DocumentCommentMentionRepository
	agentNotifySvc AgentNotifyService
	notifySvc      NotificationService
	wsPublisher    WSPublisher

	// watch subscribes commenters to the page they just joined the conversation
	// on, and tells its other watchers. Optional for the same reason as the
	// mention dependencies: commenting works without it.
	watch DocumentWatchService
}

// DocumentCommentServiceOption configures optional collaborators on the service.
type DocumentCommentServiceOption func(*documentCommentService)

// WithDocumentCommentWatch wires document subscriptions into the comment path:
// the commenter is auto-subscribed, and the document's watchers are told at
// once — commenting is not coalesced, see DocumentWatchService.NotifyComment.
func WithDocumentCommentWatch(w DocumentWatchService) DocumentCommentServiceOption {
	return func(s *documentCommentService) { s.watch = w }
}

// WithDocumentCommentAgentService sets the agent lookup used to resolve @-slugs
// to agents.
func WithDocumentCommentAgentService(a AgentService) DocumentCommentServiceOption {
	return func(s *documentCommentService) { s.agentSvc = a }
}

// WithDocumentCommentUserRepo sets the user lookup used to resolve @-slugs to
// people.
func WithDocumentCommentUserRepo(r repository.UserRepository) DocumentCommentServiceOption {
	return func(s *documentCommentService) { s.userRepo = r }
}

// WithDocumentCommentMentionRepo sets the repository that persists
// document_comment_mentions rows.
func WithDocumentCommentMentionRepo(r repository.DocumentCommentMentionRepository) DocumentCommentServiceOption {
	return func(s *documentCommentService) { s.mentionRepo = r }
}

// WithDocumentCommentAgentNotifier sets the push channel for mentioned agents.
func WithDocumentCommentAgentNotifier(n AgentNotifyService) DocumentCommentServiceOption {
	return func(s *documentCommentService) { s.agentNotifySvc = n }
}

// WithDocumentCommentNotificationService sets the fan-out for mentioned humans —
// in-app bell, browser push, email, Telegram.
func WithDocumentCommentNotificationService(n NotificationService) DocumentCommentServiceOption {
	return func(s *documentCommentService) { s.notifySvc = n }
}

// WithDocumentCommentWSPublisher sets the live badge channel for mentioned
// humans who currently have the app open.
func WithDocumentCommentWSPublisher(p WSPublisher) DocumentCommentServiceOption {
	return func(s *documentCommentService) { s.wsPublisher = p }
}

// NewDocumentCommentService returns a DocumentCommentService backed by the given
// repositories.
//
// It takes the document repository as well as its own, and that is the tenancy
// check rather than a convenience: every entry point resolves the document inside
// the caller's workspace first, so a comment id or a document id from another
// tenant has nothing to attach to.
// documentBody is used on one path only — checking that an anchor's offsets
// point at the anchor's own quote — so the ordinary comment, which has no
// offsets, still costs no object-storage read.
func NewDocumentCommentService(
	commentRepo repository.DocumentCommentRepository,
	documentRepo repository.DocumentRepository,
	documentBody DocumentBodyReader,
	opts ...DocumentCommentServiceOption,
) DocumentCommentService {
	s := &documentCommentService{
		commentRepo:  commentRepo,
		documentRepo: documentRepo,
		documentBody: documentBody,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
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

	anchor, err := validateAnchor(input.Anchor)
	if err != nil {
		return nil, err
	}
	var parentID *uuid.UUID
	if input.ParentCommentID != nil {
		parent, perr := s.requireReplyableParent(ctx, *input.ParentCommentID, doc.ID)
		if perr != nil {
			return nil, perr
		}
		if anchor != nil {
			// A reply with its own anchor is two claims about what one thread is
			// about, and nothing keeps them pointing at the same words once the
			// document is edited. The reply inherits the root's anchor instead.
			return nil, apierror.ValidationError(map[string]string{
				"anchor": "a reply inherits its parent's anchor and cannot carry one of its own",
			})
		}
		id := parent.ID
		parentID = &id
	}

	// Before the insert, not after: an unresolvable @-slug fails the whole
	// request, and a comment that was written and then refused would be a row
	// the author was told did not exist.
	recipients, err := s.resolveMentions(ctx, input.WorkspaceID, body, nil)
	if err != nil {
		return nil, err
	}

	// After the reply check, deliberately: a reply that carries an anchor at all
	// is refused whatever its offsets say, and "a reply inherits its parent's
	// anchor" is the answer that tells the caller what to change.
	//
	// And after the mention check, for cost rather than meaning: resolving slugs
	// is a lookup we already hold, while this fetches the document body from
	// object storage. A request wrong in both ways should not pay for the
	// download to be told about the slug.
	if aerr := s.requireAnchorInThisDocument(ctx, doc, input.WorkspaceID, anchor); aerr != nil {
		return nil, aerr
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

	s.deliverMentions(ctx, comment, doc, input.WorkspaceID, recipients)
	s.deliverToWatchers(ctx, comment, doc, input.WorkspaceID, recipients)

	return s.enriched(ctx, comment), nil
}

// deliverToWatchers subscribes the author to the page and tells everyone else
// who follows it.
//
// Ordered after deliverMentions and given the same recipient list on purpose:
// somebody who was @-mentioned in this comment AND watches the page has already
// been told, by the more specific of the two routes. Sending the watch copy as
// well would mean the reward for subscribing is being notified twice.
func (s *documentCommentService) deliverToWatchers(
	ctx context.Context,
	comment *domain.DocumentComment,
	doc *domain.Document,
	workspaceID uuid.UUID,
	mentioned []documentMentionRecipient,
) {
	if s.watch == nil {
		return
	}

	// Joining a conversation is a subscription to it. Automatic, so it cannot
	// overwrite an earlier unsubscribe — see DocumentWatchRepo.Subscribe.
	s.watch.AutoSubscribe(ctx, doc.ID, comment.AuthorID, string(comment.AuthorType), domain.WatchSourceCommenter)

	already := make(map[uuid.UUID]bool, len(mentioned))
	for _, r := range mentioned {
		already[r.id] = true
	}

	s.watch.NotifyComment(ctx, NotifyDocumentCommentInput{
		Document:        doc,
		WorkspaceID:     workspaceID,
		CommentID:       comment.ID,
		Body:            comment.Body,
		ActorID:         comment.AuthorID,
		ActorKind:       string(comment.AuthorType),
		ActorName:       actorctx.NameFromContext(ctx),
		AlreadyNotified: already,
	})
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

	// Only slugs the edit ADDS are resolved and notified. Re-checking the ones
	// that were already there would let a departed member turn every later edit
	// of an old comment into a 400, and re-notifying them would make "fixed a
	// typo" ping everybody the paragraph names, every time.
	alreadyMentioned := make(map[string]bool)
	for _, slug := range extractDocumentMentionSlugs(comment.Body) {
		alreadyMentioned[slug] = true
	}
	recipients, err := s.resolveMentions(ctx, workspaceID, body, alreadyMentioned)
	if err != nil {
		return nil, err
	}

	comment.Body = body
	comment.UpdatedAt = timeNow()
	if updErr := s.commentRepo.Update(ctx, comment); updErr != nil {
		return nil, updErr
	}

	// Guarded rather than left to deliverMentions' own early return: documentFor
	// is a database read, and an edit that added no mentions should not pay for
	// one.
	if len(recipients) > 0 {
		s.deliverMentions(ctx, comment, s.documentFor(ctx, comment, workspaceID), workspaceID, recipients)
	}

	return s.enriched(ctx, comment), nil
}

// documentFor loads the page a comment lives on, for the notification copy.
//
// A nil result would be a nil dereference in the notifier, and the comment has
// already been written by the time this is called, so an unreadable document
// degrades to an untitled placeholder rather than failing the edit. It cannot
// return another tenant's page: the workspace is the caller's own, resolved by
// the route guard.
func (s *documentCommentService) documentFor(
	ctx context.Context,
	comment *domain.DocumentComment,
	workspaceID uuid.UUID,
) *domain.Document {
	if doc, err := s.documentRepo.GetByIDInWorkspace(ctx, comment.DocumentID, workspaceID); err == nil && doc != nil {
		return doc
	}
	return &domain.Document{ID: comment.DocumentID, Title: "a document"}
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

// requireAnchorInThisDocument refuses an anchor whose offsets do not point at its
// own quote in this document's markdown.
//
// ## Why the server has to check this at all
//
// anchor_start/anchor_end are documented — in the column comments of
// 20260819100_create_document_comments.sql — as BYTE offsets into the markdown,
// half-open. Until this check existed, nothing enforced it: validateAnchor read
// the sign, the order and the length of the fields and never opened the document,
// so the coordinate system was decided by whichever client wrote the row.
//
// That is not hypothetical. Two independently written frontends put different
// units in these columns — PR #619 UTF-8 byte offsets into the markdown, with a
// conversion module and its own Cyrillic tests; PR #621 character indices into
// the editor's rendered text, stated in its header as deliberately not markdown
// byte offsets. On ASCII the two are the same number and both look correct. On a
// Russian document they differ by about half, and the row that results points
// confidently at a different sentence — with no error, at write time or at read
// time. This runs while document_comments still holds zero rows, which is the
// only moment the format can be fixed without migrating anything.
//
// ## Why it is a 400 and not a repair
//
// The server could re-resolve the quote and store the offsets it found. It does
// not, because a client that sent the wrong units will go on sending them, and
// silently correcting the write leaves both sides believing they agree. A 400
// naming the units is what makes the disagreement visible while it is still one
// client's bug rather than a column full of mixed coordinates.
func (s *documentCommentService) requireAnchorInThisDocument(
	ctx context.Context,
	doc *domain.Document,
	workspaceID uuid.UUID,
	anchor *domain.DocumentCommentAnchor,
) error {
	// No anchor, or an orphaned one (quote kept, position deliberately absent):
	// there are no offsets to be wrong about, and refusing to record a comment
	// whose range the client honestly could not find would lose the comment.
	if anchor == nil || anchor.Start == nil || anchor.End == nil {
		return nil
	}

	body, err := s.documentBodyFor(ctx, doc, workspaceID)
	if err != nil {
		return err
	}

	if mdoc.SpanMatchesQuote(body, *anchor.Start, *anchor.End, anchor.Exact) {
		return nil
	}

	return apierror.ValidationError(map[string]string{
		"anchor.start": fmt.Sprintf(
			"anchor.start/anchor.end must be UTF-8 BYTE offsets into the document's markdown, "+
				"half-open [start, end); [%d, %d) does not contain anchor.exact. The usual cause "+
				"is character or UTF-16 indices, or indices into the rendered text rather than "+
				"the markdown — on a document with non-ASCII text those are a different number "+
				"and point at other words. The other cause is that the document was edited since "+
				"the selection was made: re-read the body and re-locate the quote, or send the "+
				"quote with no offsets to store the comment as orphaned. "+
				"POST /api/v1/documents/%s/resolve-anchor computes the offsets from a quotation.",
			*anchor.Start, *anchor.End, doc.ID),
	})
}

// documentBodyFor returns the document's markdown.
//
// It fails closed. A body that cannot be read is not evidence that the anchor is
// fine — it is the absence of evidence either way, and the whole point of this
// check is that a wrong anchor is indistinguishable from a right one once it is
// in the table.
func (s *documentCommentService) documentBodyFor(
	ctx context.Context,
	doc *domain.Document,
	workspaceID uuid.UUID,
) (string, error) {
	if s.documentBody == nil {
		return "", apierror.ServiceUnavailable("document body reader not configured")
	}
	withBody, err := s.documentBody.GetByIDInWorkspace(ctx, doc.ID, workspaceID)
	if err != nil {
		return "", err
	}
	if withBody == nil {
		return "", apierror.NotFound("Document")
	}
	if withBody.Body == "" {
		// An empty body has no range to select, so an anchored comment on one is
		// either a client bug or a body that failed to load as an empty string.
		return "", apierror.ValidationError(map[string]string{
			"anchor.start": "this document has no body, so there is no range for an anchor to point at",
		})
	}
	return withBody.Body, nil
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
