package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Document subscription event types.
//
// Three, not one, because a subscriber needs to be able to turn one off without
// turning the others off: the whole reason this feature coalesces edits is that
// edits and conversations arrive at completely different rates.
//
// All three are registered in handler.dispatchableEvents (so a preference row
// may subscribe) and back-filled onto existing rows by
// migrations/20260820105_create_document_watchers.sql — an event type that
// appears in no stored `events` array is dispatched perfectly and delivered to
// nobody.
const (
	// DocumentChangedEvent is a COALESCED body/title edit: one notification per
	// editing session, never one per autosave.
	DocumentChangedEvent = "document.changed"
	// DocumentCommentedEvent is a new comment on a watched document, delivered
	// immediately — a comment is an address to people, and it arrives at human
	// pace already.
	DocumentCommentedEvent = "document.commented"
	// DocumentDeletedEvent is a watched document being deleted. Immediate, and
	// the only one of the three where the page the notification links to is gone
	// by the time it is read; the title in the body is therefore the message,
	// not decoration.
	DocumentDeletedEvent = "document.deleted"
)

// inAppChannel is the notification_preferences channel that feeds the bell.
// Named here because the watch path provisions it, and a typo would provision a
// channel nothing dispatches to — the silent failure this whole file is about.
const inAppChannel = "web_push"

// defaultQuietWindow is how long an editor must be idle before their edits are
// announced.
//
// Five minutes is a judgement, not a measurement, and it is configurable
// (DOCUMENT_WATCH_QUIET_WINDOW) precisely because it is a judgement. It has to
// sit well above the editor's 2-second autosave debounce — anything near it
// would coalesce nothing — and well below the length of a working session, or
// one sitting turns into several notifications. Its cost is the delay: a
// watcher hears about an edit up to a window after it landed.
const defaultQuietWindow = 5 * time.Minute

// maxNoticesPerSweep bounds one sweeper tick. A backlog is drained over several
// ticks rather than in one transaction holding hundreds of row locks.
const maxNoticesPerSweep = 200

// maxWatchNotificationBody caps the body of a change notification.
const maxWatchNotificationBody = 200

// DocumentWatchService owns document subscriptions and the coalesced delivery of
// document changes.
type DocumentWatchService interface {
	// Watch subscribes the caller explicitly, clearing any earlier unsubscribe.
	Watch(ctx context.Context, documentID, workspaceID uuid.UUID) (*domain.DocumentWatchState, error)
	// Unwatch mutes the caller's subscription, including one they never made
	// explicitly.
	Unwatch(ctx context.Context, documentID, workspaceID uuid.UUID) (*domain.DocumentWatchState, error)
	// State reports whether the caller is watching and how many principals are.
	State(ctx context.Context, documentID, workspaceID uuid.UUID) (*domain.DocumentWatchState, error)

	// AutoSubscribe records a subscription the system creates on the principal's
	// behalf — the author of a page, anyone who comments on it. It never
	// overrides an explicit unsubscribe.
	AutoSubscribe(ctx context.Context, documentID, actorID uuid.UUID, actorKind, source string)

	// RecordChange folds one document write into the pending notice for its
	// author. Called on every autosave; sends nothing by itself.
	RecordChange(ctx context.Context, in RecordDocumentChangeInput)

	// NotifyComment tells a document's watchers about a new comment, at once.
	// `alreadyNotified` names principals the caller has already reached by
	// another route — the @-mention path — so nobody is told twice about one
	// comment.
	NotifyComment(ctx context.Context, in NotifyDocumentCommentInput)

	// NotifyDeleted tells a document's watchers it is gone.
	NotifyDeleted(ctx context.Context, doc *domain.Document, workspaceID uuid.UUID)

	// SweepPendingNotices delivers every notice whose author has been quiet for
	// the window. Returns how many notices were dispatched.
	SweepPendingNotices(ctx context.Context) (int, error)
}

// RecordDocumentChangeInput is one write to a document, as the watch service
// sees it.
type RecordDocumentChangeInput struct {
	Document     *domain.Document
	WorkspaceID  uuid.UUID
	ActorID      uuid.UUID
	ActorKind    string
	FromVersion  int
	ToVersion    int
	TitleChanged bool
	BodyChanged  bool
}

// NotifyDocumentCommentInput is a new comment on a document.
type NotifyDocumentCommentInput struct {
	Document    *domain.Document
	WorkspaceID uuid.UUID
	CommentID   uuid.UUID
	Body        string
	ActorID     uuid.UUID
	ActorKind   string
	ActorName   string
	// AlreadyNotified holds principals reached by the mention path for this same
	// comment. A watcher who was also @-mentioned gets the mention, which is the
	// more specific of the two, and not a second copy of the same news.
	AlreadyNotified map[uuid.UUID]bool
}

type documentWatchService struct {
	repo           repository.DocumentWatchRepository
	documentRepo   repository.DocumentRepository
	notifySvc      NotificationService
	agentNotifySvc AgentNotifyService
	quietWindow    time.Duration
}

// NewDocumentWatchService constructs a DocumentWatchService.
//
// notifySvc and agentNotifySvc are optional in the same way they are optional
// for the mention path: a service built without them still records
// subscriptions and still coalesces, it just has nowhere to deliver. Silently
// skipping is only acceptable because the caller-visible half of the feature —
// the Watch button and its state — keeps working, and because dispatch says so
// in the notice's own row rather than only in a log.
func NewDocumentWatchService(
	repo repository.DocumentWatchRepository,
	documentRepo repository.DocumentRepository,
	notifySvc NotificationService,
	agentNotifySvc AgentNotifyService,
	quietWindow time.Duration,
) DocumentWatchService {
	if quietWindow <= 0 {
		quietWindow = defaultQuietWindow
	}
	return &documentWatchService{
		repo:           repo,
		documentRepo:   documentRepo,
		notifySvc:      notifySvc,
		agentNotifySvc: agentNotifySvc,
		quietWindow:    quietWindow,
	}
}

// callerPrincipal reads the acting user or agent out of the request context.
//
// The pair is returned, never the id alone: users and agents are separate id
// spaces, so an id without its kind cannot be compared to a stored watcher
// without the risk of matching the wrong principal entirely.
func callerPrincipal(ctx context.Context) (uuid.UUID, string, error) {
	actorID, actorType := actorctx.FromContext(ctx)
	if actorID == uuid.Nil {
		return uuid.Nil, "", apierror.Unauthorized("authentication required")
	}
	kind := string(actorType)
	if kind != "user" && kind != "agent" {
		return uuid.Nil, "", apierror.Unauthorized("authentication required")
	}
	return actorID, kind, nil
}

// requireDocument refuses a document the caller cannot see.
//
// Returns only an error: a subscription is keyed on the id, so nothing here
// needs the document itself — it needs the 404 that a foreign or deleted id has
// to produce before any row is written for it.
func (s *documentWatchService) requireDocument(ctx context.Context, documentID, workspaceID uuid.UUID) error {
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, documentID, workspaceID)
	if err != nil {
		return err
	}
	if doc == nil {
		return apierror.NotFound("Document")
	}
	return nil
}

func (s *documentWatchService) Watch(ctx context.Context, documentID, workspaceID uuid.UUID) (*domain.DocumentWatchState, error) {
	actorID, kind, err := callerPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireDocument(ctx, documentID, workspaceID); err != nil {
		return nil, err
	}
	// force: an explicit Watch is exactly the act that clears an earlier
	// unsubscribe, and the only one that may.
	if err := s.repo.Subscribe(ctx, domain.DocumentWatcher{
		DocumentID:  documentID,
		WatcherID:   actorID,
		WatcherKind: kind,
		Source:      domain.WatchSourceExplicit,
	}, true); err != nil {
		return nil, err
	}
	// Pressing Watch is a request to be told, and until this call existed it was
	// only a request to be *counted*: delivery to a person runs through
	// notification_preferences, a row nothing creates on a user's behalf. A
	// workspace whose members had never opened Notification Settings therefore
	// had no deliverable channel at all, so every watched change was handed to
	// dispatch, matched no preference row, and vanished — while the notice
	// recorded the watcher as a recipient. Measured on prod 2026-08-20: one
	// preference row in the entire database, none in this workspace, and the
	// notifications table empty since it was created.
	if kind == "user" {
		s.ensureInAppDelivery(ctx, workspaceID, actorID)
	}
	return s.repo.GetState(ctx, documentID, actorID, kind)
}

// ensureInAppDelivery gives an explicitly-subscribed person somewhere for their
// document notifications to arrive.
//
// Deliberately narrow, because it edits settings the person owns:
//   - only the in-app channel (web_push). Watching a page is not consent to be
//     emailed or messaged on Telegram.
//   - only the three document event types, unioned into whatever the row already
//     carries. It never removes an event and never touches another channel.
//   - a row the person has switched OFF is left off. An explicit "no in-app
//     notifications" outranks the implicit request inside a Watch click; the
//     subscription is still recorded, and the log says why nothing will arrive.
//
// It runs only for an explicit Watch, never for AutoSubscribe: silently
// re-adding an event type to the settings of everyone who ever commented would
// be exactly the sort of thing an unsubscribe exists to prevent.
//
// Best-effort by construction — a subscription that was written must not be
// rolled back because the preference row could not be.
func (s *documentWatchService) ensureInAppDelivery(ctx context.Context, workspaceID, userID uuid.UUID) {
	if s.notifySvc == nil {
		return
	}
	prefs, err := s.notifySvc.GetPreferences(ctx, userID)
	if err != nil {
		log.Printf("[docwatch] user %s watched a document in workspace %s but their notification preferences could not be read, so in-app delivery is unconfirmed: %v",
			userID, workspaceID, err)
		return
	}

	var inApp *domain.NotificationPreference
	for i := range prefs {
		p := &prefs[i]
		if p.UserID == nil || *p.UserID != userID || p.WorkspaceID != workspaceID {
			continue
		}
		// Any enabled channel that already carries all three event types is
		// enough — someone who subscribed by email does not also need the bell.
		if p.IsEnabled && coversAll(p.Events, documentWatchEvents()) {
			return
		}
		if p.Channel == inAppChannel {
			inApp = p
		}
	}

	if inApp != nil && !inApp.IsEnabled {
		log.Printf("[docwatch] user %s watched a document in workspace %s with in-app notifications switched off — subscription recorded, nothing will be delivered there",
			userID, workspaceID)
		return
	}

	pref := &domain.NotificationPreference{
		WorkspaceID: workspaceID,
		UserID:      &userID,
		Channel:     inAppChannel,
		Events:      documentWatchEvents(),
		IsEnabled:   true,
	}
	if inApp != nil {
		pref.ID = inApp.ID
		pref.Config = inApp.Config
		pref.Events = unionEvents(inApp.Events, documentWatchEvents())
	}
	if _, err := s.notifySvc.UpsertPreferences(ctx, pref); err != nil {
		log.Printf("[docwatch] user %s watched a document in workspace %s but the in-app channel could not be provisioned, so nothing will be delivered there: %v",
			userID, workspaceID, err)
	}
}

// documentWatchEvents is the set a watcher is subscribed to. Returned fresh each
// call: the value ends up in a preference row that callers mutate.
func documentWatchEvents() []string {
	return []string{DocumentChangedEvent, DocumentCommentedEvent, DocumentDeletedEvent}
}

// coversAll reports whether every wanted event is present in have.
func coversAll(have, wanted []string) bool {
	for _, w := range wanted {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// unionEvents adds the missing wanted events to have, preserving order and
// never dropping one the person already had.
func unionEvents(have, wanted []string) []string {
	out := append([]string(nil), have...)
	for _, w := range wanted {
		if !coversAll(out, []string{w}) {
			out = append(out, w)
		}
	}
	return out
}

func (s *documentWatchService) Unwatch(ctx context.Context, documentID, workspaceID uuid.UUID) (*domain.DocumentWatchState, error) {
	actorID, kind, err := callerPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireDocument(ctx, documentID, workspaceID); err != nil {
		return nil, err
	}
	if err := s.repo.Unsubscribe(ctx, documentID, actorID, kind); err != nil {
		return nil, err
	}
	return s.repo.GetState(ctx, documentID, actorID, kind)
}

func (s *documentWatchService) State(ctx context.Context, documentID, workspaceID uuid.UUID) (*domain.DocumentWatchState, error) {
	actorID, kind, err := callerPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireDocument(ctx, documentID, workspaceID); err != nil {
		return nil, err
	}
	return s.repo.GetState(ctx, documentID, actorID, kind)
}

// AutoSubscribe is best-effort and never fails the operation it rides along
// with: nobody's comment is rejected because the subscription that would have
// come with it could not be written.
func (s *documentWatchService) AutoSubscribe(ctx context.Context, documentID, actorID uuid.UUID, actorKind, source string) {
	if s == nil || s.repo == nil || actorID == uuid.Nil {
		return
	}
	if actorKind != "user" && actorKind != "agent" {
		return
	}
	if err := s.repo.Subscribe(ctx, domain.DocumentWatcher{
		DocumentID:  documentID,
		WatcherID:   actorID,
		WatcherKind: actorKind,
		Source:      source,
	}, false); err != nil {
		log.Printf("[doc-watch] auto-subscribe (%s) failed for %s on document %s: %v",
			source, actorID, documentID, err)
	}
}

// RecordChange folds one write into the open notice for its author.
//
// Deliberately cheap and deliberately silent: this runs on the editor's
// autosave path, up to once every two seconds per open document, and it must
// stay one UPDATE of one row. Everything expensive — reading the watcher list,
// building a message, four delivery channels — happens once per session in the
// sweeper instead.
func (s *documentWatchService) RecordChange(ctx context.Context, in RecordDocumentChangeInput) {
	if s == nil || s.repo == nil || in.Document == nil {
		return
	}
	if !in.TitleChanged && !in.BodyChanged {
		// A move in the tree is not news. It does not even bump the version.
		return
	}
	if err := s.repo.RecordChange(ctx, domain.DocumentChangeNotice{
		DocumentID:   in.Document.ID,
		WorkspaceID:  in.WorkspaceID,
		ActorID:      in.ActorID,
		ActorKind:    in.ActorKind,
		ActorName:    actorctx.NameFromContext(ctx),
		TitleChanged: in.TitleChanged,
		BodyChanged:  in.BodyChanged,
		FromVersion:  in.FromVersion,
		ToVersion:    in.ToVersion,
	}); err != nil {
		log.Printf("[doc-watch] failed to record change on document %s: %v", in.Document.ID, err)
	}
}

// SweepPendingNotices turns every notice whose author has gone quiet into one
// notification per watcher.
func (s *documentWatchService) SweepPendingNotices(ctx context.Context) (int, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	quietBefore := timeNow().Add(-s.quietWindow)
	notices, err := s.repo.ClaimPendingNotices(ctx, quietBefore, maxNoticesPerSweep)
	if err != nil {
		return 0, err
	}
	for i := range notices {
		s.dispatchNotice(ctx, &notices[i])
	}
	return len(notices), nil
}

// dispatchNotice delivers one coalesced notice and records what came of it.
//
// Every exit from this function writes the outcome back to the notice row. That
// is the point: a subscriber who was not reached must leave a trace, because
// "the watcher list could not be read" and "there were no watchers" are the same
// silence from the outside and only one of them is normal.
func (s *documentWatchService) dispatchNotice(ctx context.Context, n *domain.DocumentChangeNotice) {
	doc, err := s.documentRepo.GetByIDInWorkspace(ctx, n.DocumentID, n.WorkspaceID)
	if err != nil {
		s.finish(ctx, n, 0, fmt.Sprintf("document lookup failed: %v", err))
		return
	}
	if doc == nil {
		// Deleted between the last edit and the sweep. The deletion has its own
		// immediate notification, so there is nothing to announce here — but it
		// is recorded rather than dropped, so the count of silent notices stays
		// explainable.
		s.finish(ctx, n, 0, "document no longer exists")
		return
	}

	watchers, err := s.repo.ListLiveWatchers(ctx, n.DocumentID)
	if err != nil {
		s.finish(ctx, n, 0, fmt.Sprintf("watcher lookup failed: %v", err))
		return
	}

	title, body := changeMessage(n, doc)
	sent := s.fanOut(ctx, doc, n.WorkspaceID, watchers,
		principal{id: n.ActorID, kind: n.ActorKind, name: n.ActorName},
		nil, DocumentChangedEvent, title, body, map[string]any{
			"document_id":    doc.ID,
			"document_slug":  doc.Slug,
			"document_title": doc.Title,
			"project_id":     doc.ProjectID,
			"edit_count":     n.EditCount,
			"from_version":   n.FromVersion,
			"to_version":     n.ToVersion,
			"title_changed":  n.TitleChanged,
		})

	log.Printf("[doc-watch] notice %s: document %s, %d edit(s) by %s, %d live watcher(s), delivered to %d",
		n.ID, n.DocumentID, n.EditCount, n.ActorID, len(watchers), sent)
	s.finish(ctx, n, sent, "")
}

func (s *documentWatchService) finish(ctx context.Context, n *domain.DocumentChangeNotice, recipients int, dispatchErr string) {
	if dispatchErr != "" {
		log.Printf("[doc-watch] notice %s on document %s delivered to nobody: %s", n.ID, n.DocumentID, dispatchErr)
	}
	if err := s.repo.FinishNotice(ctx, n.ID, recipients, dispatchErr); err != nil {
		log.Printf("[doc-watch] failed to record outcome of notice %s: %v", n.ID, err)
	}
}

// changeMessage turns a coalesced notice into a sentence.
//
// It says how many edits it is summarising rather than pretending to be one
// edit. A watcher who is told "12 edits" understands they are reading a summary
// and that the delay was deliberate; one told "edited the document" twelve
// minutes late has been quietly misled about when it happened.
func changeMessage(n *domain.DocumentChangeNotice, doc *domain.Document) (title, body string) {
	who := n.ActorName
	if who == "" {
		who = "Someone"
	}
	title = who + " edited: " + doc.Title

	switch {
	case n.TitleChanged && n.BodyChanged:
		body = fmt.Sprintf("%s renamed the page and made %s.", who, editCountPhrase(n.EditCount))
	case n.TitleChanged:
		body = fmt.Sprintf("%s renamed the page to «%s».", who, doc.Title)
	default:
		body = fmt.Sprintf("%s made %s.", who, editCountPhrase(n.EditCount))
	}
	return title, truncateRunes(body, maxWatchNotificationBody)
}

func editCountPhrase(n int) string {
	if n <= 1 {
		return "1 edit"
	}
	return fmt.Sprintf("%d edits", n)
}

// principal is an (id, kind) pair — the only honest way to identify a watcher.
type principal struct {
	id   uuid.UUID
	kind string
	name string
}

// fanOut delivers one message to a document's watchers and returns how many
// were reached.
//
// `actor` is excluded: you are not told about your own edit. The exclusion
// compares BOTH id and kind, because a user id and an agent id are drawn from
// different spaces and an id-only comparison would either fail to exclude the
// real author or exclude an innocent bystander who happens to share the value.
//
// `skip` holds principals already told about this same event by a more specific
// route — today, the @-mention path on a comment.
func (s *documentWatchService) fanOut(
	ctx context.Context,
	doc *domain.Document,
	workspaceID uuid.UUID,
	watchers []domain.DocumentWatcher,
	actor principal,
	skip map[uuid.UUID]bool,
	eventType, title, body string,
	metadata map[string]any,
) int {
	sent := 0
	for i := range watchers {
		w := &watchers[i]
		if w.WatcherID == actor.id && w.WatcherKind == actor.kind {
			continue
		}
		if skip != nil && skip[w.WatcherID] {
			continue
		}

		switch w.WatcherKind {
		case "agent":
			if s.agentNotifySvc == nil {
				continue
			}
			s.agentNotifySvc.NotifyAgent(ctx, w.WatcherID, AgentNotification{
				EventType:   eventType,
				Timestamp:   timeNow(),
				WorkspaceID: workspaceID,
				AgentID:     w.WatcherID,
				ActorID:     actor.id,
				ActorType:   actor.kind,
				ActorName:   actor.name,
				ProjectID:   doc.ProjectID,
				Payload:     metadata,
			})
			sent++
		case "user":
			if s.notifySvc == nil {
				continue
			}
			// TargetUserID is what keeps this a subscription rather than a
			// broadcast: dispatch delivers a targeted event to that one person's
			// preference rows, so watching a page does not send its contents to
			// everyone in the workspace who happens to subscribe to the event.
			target := w.WatcherID
			projectID := doc.ProjectID
			s.notifySvc.Notify(ctx, domain.NotificationEvent{
				WorkspaceID:  workspaceID,
				ProjectID:    &projectID,
				TargetUserID: &target,
				EventType:    eventType,
				Title:        title,
				Body:         body,
				Metadata:     metadata,
			})
			sent++
		}
	}
	return sent
}

// NotifyComment tells watchers about a new comment, immediately.
//
// Not coalesced, and that asymmetry is the design: a comment is an address to
// people and arrives at conversational pace, while a body edit arrives at
// autosave pace. Delaying the first to protect against the second would make
// the useful half of the feature worse to fix a problem it does not have.
func (s *documentWatchService) NotifyComment(ctx context.Context, in NotifyDocumentCommentInput) {
	if s == nil || s.repo == nil || in.Document == nil {
		return
	}
	watchers, err := s.repo.ListLiveWatchers(ctx, in.Document.ID)
	if err != nil {
		log.Printf("[doc-watch] watcher lookup failed for comment %s on document %s: %v",
			in.CommentID, in.Document.ID, err)
		return
	}
	if len(watchers) == 0 {
		return
	}

	who := in.ActorName
	if who == "" {
		who = "Someone"
	}
	sent := s.fanOut(ctx, in.Document, in.WorkspaceID, watchers,
		principal{id: in.ActorID, kind: in.ActorKind, name: in.ActorName},
		in.AlreadyNotified,
		DocumentCommentedEvent,
		who+" commented on: "+in.Document.Title,
		truncateRunes(in.Body, maxWatchNotificationBody),
		map[string]any{
			"document_id":    in.Document.ID,
			"document_slug":  in.Document.Slug,
			"document_title": in.Document.Title,
			"project_id":     in.Document.ProjectID,
			"comment_id":     in.CommentID,
		})
	log.Printf("[doc-watch] comment %s on document %s: %d live watcher(s), delivered to %d",
		in.CommentID, in.Document.ID, len(watchers), sent)
}

// NotifyDeleted tells watchers a page they follow is gone.
//
// Safe to call after the delete because a document delete is soft: the row
// stays, deleted_at is stamped, and the ON DELETE CASCADE on document_watchers
// therefore does not fire. If deletes ever become hard, this call has to move
// ahead of the delete — a watcher list read after a real DELETE comes back
// empty, and the notification would be lost with no error anywhere.
func (s *documentWatchService) NotifyDeleted(ctx context.Context, doc *domain.Document, workspaceID uuid.UUID) {
	if s == nil || s.repo == nil || doc == nil {
		return
	}
	actorID, actorType := actorctx.FromContext(ctx)
	watchers, err := s.repo.ListLiveWatchers(ctx, doc.ID)
	if err != nil {
		log.Printf("[doc-watch] watcher lookup failed for deletion of document %s: %v", doc.ID, err)
		return
	}
	if len(watchers) == 0 {
		return
	}
	who := actorctx.NameFromContext(ctx)
	if who == "" {
		who = "Someone"
	}
	sent := s.fanOut(ctx, doc, workspaceID, watchers,
		principal{id: actorID, kind: string(actorType), name: who}, nil,
		DocumentDeletedEvent,
		who+" deleted: "+doc.Title,
		// The title carries the whole message here: unlike every other document
		// notification, the page this one names cannot be opened to find out
		// what it was about.
		fmt.Sprintf("The document «%s» you were watching was deleted by %s.", doc.Title, who),
		map[string]any{
			"document_id":    doc.ID,
			"document_slug":  doc.Slug,
			"document_title": doc.Title,
			"project_id":     doc.ProjectID,
			"deleted":        true,
		})
	log.Printf("[doc-watch] deletion of document %s: %d live watcher(s), delivered to %d",
		doc.ID, len(watchers), sent)
}
