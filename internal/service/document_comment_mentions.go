package service

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DocumentMentionedEvent is the notification/SSE event type for an @-mention in
// a document comment.
//
// A distinct type rather than reusing task.mentioned: the payload names a
// document and no task, and a consumer that branches on the event type must be
// able to tell which of the two it is holding before it tries to open one.
// Registered in handler.dispatchableEvents (so a preference row may subscribe to
// it) and in criticalSSEEventTypes (so an offline agent can still replay it).
const DocumentMentionedEvent = "document.mentioned"

// maxMentionNotificationBody caps how much of a comment travels inside a
// notification. Matches notifyUserMention's cap on the task path.
const maxMentionNotificationBody = 200

var (
	// fencedCodeBlock and inlineCode are the author's escape hatch.
	//
	// An unresolvable @-slug is refused (see errUnresolvedMentions), which makes
	// "I am quoting a log line that happens to contain @something" a request that
	// would otherwise be impossible to submit. Masking code spans before
	// extraction means the documented way to write an @ that is not a mention —
	// backticks — actually works, and it is the same rule the renderer already
	// applies when it decides what to highlight (markdown-renderer.tsx protects
	// backticked spans from the mention regex).
	fencedCodeBlock = regexp.MustCompile("(?s)```.*?```")
	inlineCode      = regexp.MustCompile("`[^`\n]*`")
)

// maskCodeSpans blanks fenced and inline code, preserving byte length so that
// nothing else about the body shifts.
func maskCodeSpans(body string) string {
	blank := func(s string) string { return strings.Repeat(" ", len(s)) }
	return inlineCode.ReplaceAllStringFunc(fencedCodeBlock.ReplaceAllStringFunc(body, blank), blank)
}

// extractDocumentMentionSlugs returns the unique @-slugs a document comment
// addresses, in the order they appear, ignoring anything inside code.
//
// mentionRegex is shared with the task path on purpose: two regexes would drift,
// and the difference would show up as a slug that is a mention in one kind of
// comment and plain text in the other.
func extractDocumentMentionSlugs(body string) []string {
	return extractMentionSlugs(maskCodeSpans(body))
}

// documentMentionRecipient is one resolved addressee of a comment.
type documentMentionRecipient struct {
	id   uuid.UUID
	kind string // "agent" | "user"
	slug string
}

// errUnresolvedMentions is the refusal that keeps a misspelled mention from
// disappearing.
//
// This is the whole point of the feature's error handling, so it is worth
// stating plainly. The task path writes a mention row only once a slug resolves,
// and does nothing at all when it does not: no row, no notification, no log. A
// typo, a renamed agent or a slug that was never right is therefore
// indistinguishable from a comment that mentioned nobody — for the author, for
// the intended recipient, and for anyone reading the database afterwards. That
// failure ran for 25 days against one agent's misspelt slug and stalled a P0
// before anybody noticed, because there was nothing anywhere to notice.
//
// Refusing the write is the only remedy that is impossible to miss: the author
// finds out at the moment they can still fix it, and no comment exists claiming
// to have told somebody something. The alternative — accept it and render it as
// plain text — is quieter and puts the discovery on the recipient, who by
// definition is not looking.
func errUnresolvedMentions(slugs []string) error {
	quoted := make([]string, 0, len(slugs))
	for _, s := range slugs {
		quoted = append(quoted, "@"+s)
	}
	subject, verb := "no workspace member or agent", "is"
	if len(slugs) > 1 {
		subject, verb = "no workspace members or agents", "are"
	}
	return apierror.ValidationError(map[string]string{
		"body": fmt.Sprintf(
			"%s named %s %s known in this workspace — check the spelling, or wrap it in `backticks` if it is not a mention",
			subject, strings.Join(quoted, ", "), verb,
		),
	})
}

// mentionsEnabled reports whether this service can resolve a slug at all.
//
// Both lookups are optional dependencies, and a service constructed without them
// cannot tell a real slug from a typo — so it must not refuse either. It skips
// mention handling entirely instead, and says so in the log the first time a
// body actually contains one, because "mentions silently do nothing" is the
// state this feature exists to make impossible to reach unnoticed.
func (s *documentCommentService) mentionsEnabled() bool {
	return s.agentSvc != nil || s.userRepo != nil
}

// resolveMentions turns the slugs in a body into recipients, refusing the write
// if any of them names nobody.
//
// `skip` holds slugs that were already in the body before an edit: they were
// resolvable when the comment was first written, and re-checking them would let
// a later rename turn an unrelated typo fix into a 400 on somebody else's words.
func (s *documentCommentService) resolveMentions(
	ctx context.Context,
	workspaceID uuid.UUID,
	body string,
	skip map[string]bool,
) ([]documentMentionRecipient, error) {
	slugs := extractDocumentMentionSlugs(body)
	if len(slugs) == 0 {
		return nil, nil
	}

	fresh := slugs[:0]
	for _, slug := range slugs {
		if !skip[slug] {
			fresh = append(fresh, slug)
		}
	}
	if len(fresh) == 0 {
		return nil, nil
	}

	if !s.mentionsEnabled() {
		log.Printf("[doc-mention] mention resolution is not configured: %d @-slug(s) in a comment "+
			"on workspace %s were stored as plain text and delivered to nobody", len(fresh), workspaceID)
		return nil, nil
	}

	var (
		recipients []documentMentionRecipient
		unresolved []string
	)
	for _, slug := range fresh {
		switch recipient, err := s.lookupMention(ctx, workspaceID, slug); {
		case err != nil:
			// A lookup that failed is not a lookup that found nothing, and
			// answering 400 to a database blip would tell the author their
			// perfectly good mention was misspelt. Fail the write instead.
			return nil, err
		case recipient == nil:
			unresolved = append(unresolved, slug)
		default:
			recipients = append(recipients, *recipient)
		}
	}

	if len(unresolved) > 0 {
		// Logged as well as returned: the 400 reaches whoever is at the keyboard,
		// and this reaches whoever is reading the logs six weeks later asking why
		// an agent never answered.
		log.Printf("[doc-mention] refused a comment in workspace %s: unresolvable @-slug(s) %s",
			workspaceID, strings.Join(unresolved, ", "))
		return nil, errUnresolvedMentions(unresolved)
	}

	return recipients, nil
}

// lookupMention resolves one slug, agents before users.
//
// The order matches the task path's, and matters where a workspace has an agent
// and a person with the same handle: whichever is checked first wins, and the
// two paths disagreeing about which would be worse than either answer.
// (nil, nil) means "nobody by that name", which is the caller's cue to refuse.
func (s *documentCommentService) lookupMention(
	ctx context.Context,
	workspaceID uuid.UUID,
	slug string,
) (*documentMentionRecipient, error) {
	if s.agentSvc != nil {
		agent, err := s.agentSvc.GetBySlug(ctx, workspaceID, slug)
		if err != nil {
			return nil, err
		}
		if agent != nil {
			return &documentMentionRecipient{id: agent.ID, kind: "agent", slug: slug}, nil
		}
	}
	if s.userRepo != nil {
		user, err := s.userRepo.GetByUsername(ctx, workspaceID, slug)
		if err != nil {
			return nil, err
		}
		if user != nil {
			return &documentMentionRecipient{id: user.ID, kind: "user", slug: slug}, nil
		}
	}
	return nil, nil
}

// deliverMentions records the mention rows and tells the recipients.
//
// Best-effort throughout, and called only after the comment is safely written: a
// comment is never failed because of what could not be announced about it. The
// refusal that CAN fail a write happens earlier, in resolveMentions, where the
// author can still act on it.
func (s *documentCommentService) deliverMentions(
	ctx context.Context,
	comment *domain.DocumentComment,
	doc *domain.Document,
	workspaceID uuid.UUID,
	recipients []documentMentionRecipient,
) {
	if len(recipients) == 0 {
		return
	}

	actorID, actorType := comment.AuthorID, comment.AuthorType
	actorName := actorctx.NameFromContext(ctx)
	now := timeNow()

	rows := make([]domain.DocumentCommentMention, 0, len(recipients))
	for _, r := range recipients {
		// Naming yourself is a way of writing, not a way of being told. No row
		// either, so a self-mention cannot light up your own unseen badge — the
		// task path skips the row for a self-mentioning human and this keeps the
		// same rule for both kinds rather than only one.
		if r.id == actorID && string(actorType) == r.kind {
			continue
		}

		rows = append(rows, domain.DocumentCommentMention{
			CommentID:     comment.ID,
			MentionedID:   r.id,
			MentionedKind: r.kind,
			MentionedSlug: r.slug,
			ExtractedAt:   now,
		})

		if r.kind == "agent" {
			s.notifyMentionedAgent(ctx, comment, doc, workspaceID, r, actorID, actorType, actorName)
			continue
		}
		s.notifyMentionedUser(ctx, comment, doc, workspaceID, r.id, actorName)
	}

	if s.mentionRepo != nil && len(rows) > 0 {
		if err := s.mentionRepo.InsertBatch(ctx, rows); err != nil {
			log.Printf("[doc-mention] failed to record %d mention(s) on document comment %s: %v",
				len(rows), comment.ID, err)
		}
	}
}

// notifyMentionedAgent pushes the mention down the agent's own channel —
// callback URL and the Redis/SSE stream AgentNotifyService owns.
//
// Task is left nil and TaskID unset, deliberately: there is no task, and an
// invented one would be a task id that resolves to nothing for any consumer that
// followed it. Everything a consumer needs to open the page is in Payload.
func (s *documentCommentService) notifyMentionedAgent(
	ctx context.Context,
	comment *domain.DocumentComment,
	doc *domain.Document,
	workspaceID uuid.UUID,
	recipient documentMentionRecipient,
	actorID uuid.UUID,
	actorType domain.ActorType,
	actorName string,
) {
	if s.agentNotifySvc == nil {
		return
	}

	s.agentNotifySvc.NotifyAgent(ctx, recipient.id, AgentNotification{
		EventType:   DocumentMentionedEvent,
		Timestamp:   timeNow(),
		WorkspaceID: workspaceID,
		AgentID:     recipient.id,
		ActorID:     actorID,
		ActorType:   string(actorType),
		ActorName:   actorName,
		ProjectID:   doc.ProjectID,
		Comment: map[string]any{
			"id":        comment.ID,
			"body":      truncateRunes(comment.Body, 500),
			"author_id": comment.AuthorID,
		},
		Payload: map[string]any{
			"mentioned_slug": recipient.slug,
			"document_id":    doc.ID,
			"document_slug":  doc.Slug,
			"document_title": doc.Title,
			"comment_id":     comment.ID,
		},
	})
}

// notifyMentionedUser delivers to a human: the live in-app badge over the
// WebSocket, and the notification service for every channel that person actually
// subscribed to — in-app bell, browser push, email, Telegram.
//
// Both, not either. The WebSocket badge only lands if the app happens to be open
// at that instant and is discarded otherwise, so a mention delivered only that
// way is a mention that reaches whoever was already looking.
//
// TargetUserID is what keeps it private: dispatch delivers a targeted event to
// that user's own preference rows only, so naming one person does not send the
// comment body to everyone in the workspace subscribed to document.mentioned.
func (s *documentCommentService) notifyMentionedUser(
	ctx context.Context,
	comment *domain.DocumentComment,
	doc *domain.Document,
	workspaceID, mentionedUserID uuid.UUID,
	actorName string,
) {
	if s.wsPublisher != nil {
		if err := s.wsPublisher.Publish(ctx, "ws:user:"+mentionedUserID.String(), map[string]any{
			"event":        "mention.badge",
			"workspace_id": workspaceID,
			"document_id":  doc.ID,
			"comment_id":   comment.ID,
		}); err != nil {
			log.Printf("[doc-mention] failed to publish badge for user %s: %v", mentionedUserID, err)
		}
	}

	if s.notifySvc == nil {
		return
	}

	title := "You were mentioned on: " + doc.Title
	if actorName != "" {
		title = actorName + " mentioned you on: " + doc.Title
	}

	projectID := doc.ProjectID
	target := mentionedUserID

	s.notifySvc.Notify(ctx, domain.NotificationEvent{
		WorkspaceID:  workspaceID,
		ProjectID:    &projectID,
		TargetUserID: &target,
		EventType:    DocumentMentionedEvent,
		Title:        title,
		Body:         truncateRunes(comment.Body, maxMentionNotificationBody),
		Metadata: map[string]any{
			"document_id":    doc.ID,
			"document_slug":  doc.Slug,
			"document_title": doc.Title,
			"project_id":     doc.ProjectID,
			"comment_id":     comment.ID,
		},
	})
}

// truncateRunes shortens a body for a notification without splitting a
// character in half. The task path slices bytes, which turns a cut through a
// multi-byte rune into a replacement character in an email subject line.
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
