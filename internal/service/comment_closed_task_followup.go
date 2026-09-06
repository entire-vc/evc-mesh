package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// This file closes the gap measured in audit item 1.14 (task #754173eb), from
// the precedent of 2026-09-06: an agent closed a card on 05.09; a colleague
// wrote five concrete corrections into that ALREADY-CLOSED card the next
// morning, twice; nothing happened, and the work only moved when a human
// noticed by hand.
//
// Nothing was broken. Every layer behaved as designed and the design has a
// hole between the layers:
//
//   - commentService.Create deliberately suppresses task.commented on a
//     done/cancelled task (incident #56a6d5b2 — the notification used to spawn
//     a session whose prompt reopened shipped work);
//   - a fiddler-driven lane is woken ONLY by a card assigned to it and sitting
//     in a todo-category status (CLAUDE-communication.md "How @-mentions
//     wake"), so a comment on a closed card reaches nothing at all;
//   - an @-mention is not a hand-off either — that is the sibling gate in
//     comment_mention_handoff_gate.go.
//
// So a remark on a closed card is a message with no recipient, and — unlike a
// bare @-mention, which at least renders a highlighted name — nothing about
// writing it looks unusual to the author. The fix cannot be "agents should
// remember to open a card instead", because that is a rule, and a rule that
// has to be remembered at exactly the moment attention is lowest is the class
// of control this fleet has already measured not holding. It has to be a
// mechanism.
//
// The mechanism is deliberately DETERMINISTIC and reads no meaning from the
// text: closed card + comment from someone who is not its assignee + author is
// a real actor rather than a driver → open one follow-up card, assigned to the
// original assignee, in todo, related to the original. Whether the remark
// deserved a card is the assignee's judgement to make with the card in front
// of them, not this code's to make from the prose.

// followUpLabel marks a card this mechanism opened. It is also how the dedup
// below recognises its own earlier output, so renaming it silently disables
// the dedup — change both or neither.
const followUpLabel = "follow-up"

// followUpTitleExcerptRunes is how much of the comment goes into the follow-up
// card's title, per the task's spec ("первые 60 символов"). Counted in RUNES,
// not bytes: the traffic this runs on is majority Russian, and 60 bytes of
// Cyrillic is 30 characters — a byte slice would also be free to cut a
// multi-byte rune in half and put invalid UTF-8 into a title.
const followUpTitleExcerptRunes = 60

// closedFollowUpDisableEnv turns the mechanism off without a deploy.
//
// It defaults to ENABLED, and that direction is deliberate. A flag defaulting
// to off is how a guard dies quietly — this fleet has paid for that repeatedly,
// and the sibling gate in comment_mention_handoff_gate.go says so in its own
// note. The asymmetry that justifies the difference: that gate REFUSES traffic
// (a wrong default 422s two thirds of inter-agent conversation), this one only
// ADDS a card to one agent's own queue. The blast radius of being wrong here is
// noise in a queue, not a broken channel, so it ships on.
const closedFollowUpDisableEnv = "CLOSED_CARD_FOLLOWUP_DISABLE"

// closedFollowUpDisabled reports whether the mechanism is switched off.
// Read from the environment on every call rather than cached at construction,
// for the same reason mentionHandoffEnforced does: a cached value turns "I set
// the variable and nothing changed" into a debugging session.
func closedFollowUpDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(closedFollowUpDisableEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// driverCommentPrefixes are the opening tokens of a comment written by fleet
// automation rather than by someone with something to say: the fiddler feeder,
// the PR driver, the lease reaper, the server's own auto-transitions, and the
// review-verify driver's verdict block.
//
// Matched as a PREFIX of the comment's first non-empty line, never as a
// substring of the whole body. That is the difference between "this comment IS
// a driver's" and "this comment MENTIONS a driver" — a human writing «поезд
// ответил "do-not-ship", смотри сам» is exactly the remark this mechanism
// exists to deliver, and a substring match would silently drop it. The task's
// own wording is "драйверный ПРЕФИКС"; this is that, literally.
var driverCommentPrefixes = []string{
	"🤖 auto",
	"🔄 auto",
	"🔄 авто",
	"🔄 checkout ttl",
	"🔄 задача",
	"[fiddler]",
	"[pr-driver]",
	"[pr driver]",
	"🔄 pr driver",
	"[review-verify-driver]",
	"[verify]",
	"verdict:",
	"вердикт:",
	"╔══",
}

// commentMetadataSource is the cooperative label a driver may put on its own
// comment (metadata.source) to say "this is automation". Honoured here as an
// ADDITIONAL opt-out, never as the only one: comment_service.go's
// validateCommentMetadata is explicit that metadata.source is self-declared and
// is not proof of origin. Using it to SUPPRESS a side effect on yourself is
// safe in a way using it to grant anything would not be — the worst a liar
// achieves is not getting their own follow-up card.
func commentMetadataSource(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	s, _ := m["source"].(string)
	return strings.ToLower(strings.TrimSpace(s))
}

// isDriverComment reports whether this comment was written by fleet automation.
func isDriverComment(comment *domain.Comment) bool {
	if src := commentMetadataSource(comment.Metadata); src != "" && src != "api" && src != "ui" && src != "mcp" {
		return true
	}
	line := firstNonEmptyLine(comment.Body)
	if line == "" {
		return false
	}
	lower := strings.ToLower(line)
	for _, p := range driverCommentPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// firstNonEmptyLine returns the first line of body with content, trimmed.
// Leading blockquote and list markers are stripped so a driver comment does not
// evade the prefix check by opening with "> " — but nothing else is normalised,
// because every extra normalisation step is another way for a human comment to
// accidentally look like a driver's.
func firstNonEmptyLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		t = strings.TrimLeft(t, ">*- \t")
		t = strings.TrimSpace(t)
		if t != "" {
			return t
		}
	}
	return ""
}

// shortTaskID renders a task id the way the fleet writes one in prose: the
// first 8 hex characters, which is what "#<short>" means everywhere from
// tg-mesh-linkify to the Mesh UI's own resolver.
func shortTaskID(id uuid.UUID) string {
	s := id.String()
	s = strings.ReplaceAll(s, "-", "")
	if len(s) < 8 {
		return s
	}
	return s[:8]
}

// followUpTitleExcerpt flattens body to a single line and cuts it to
// followUpTitleExcerptRunes, appending an ellipsis when it actually cut.
func followUpTitleExcerpt(body string) string {
	flat := strings.Join(strings.FieldsFunc(body, unicode.IsSpace), " ")
	runes := []rune(flat)
	if len(runes) <= followUpTitleExcerptRunes {
		return flat
	}
	return strings.TrimRight(string(runes[:followUpTitleExcerptRunes]), " ") + "…"
}

// closingReportWindow is how recently the card's own close must have happened
// for a comment by the SAME actor to read as that close's report rather than as
// a remark on already-shipped work.
//
// This window is the whole precision of the guard, and it was measured, not
// guessed. `move_task` with a comment is two separate API calls from the client
// (MoveTaskInput carries no comment field), so the pairing this window has to
// span is one client round-trip. Measured on prod: the move landed at
// 17:44:13.735 and its own closing note at 17:44:13.875 — **139 milliseconds**.
//
// It was 60s for one deploy, on the reasoning that a minute is "the same
// breath". A live run showed that reasoning is wrong in the direction that
// costs something: a genuine, unrelated remark written 28 seconds after a close
// — the ordinary "close it, then think of something" — was swallowed, and the
// mechanism silently did not fire for exactly the case it exists for. A guard
// that suppresses real remarks is worse than the noise it was added to stop,
// because the noise is visible and the suppression is not.
//
// 10s keeps a ~70x margin over the measured round-trip (covering a slow or
// retried client) while sitting far below any deliberate second thought. The
// asymmetry that sets the direction: too WIDE swallows real remarks silently;
// too NARROW produces one extra card somebody closes. Prefer too narrow.
const closingReportWindow = 10 * time.Second

// commentIsOwnClosingReport reports whether this comment is the closing note of
// the actor who just closed this card.
//
// This is the defect the live acceptance run found, and it is not a small one:
// the fleet's own rule is that ANY agent may close ANY card, and the governance
// rule REQUIRES a comment before the move to done. So an orchestrator closing a
// colleague's superseded card — routine, many times a day — would have opened a
// follow-up card for that colleague every single time, titled with the first 60
// characters of "Закрываю: …". A mechanism whose whole purpose is to stop noise
// reaching an agent's queue would have become the fleet's largest single source
// of it.
//
// Measured live on prod 2026-09-06 (`#754173eb`): `move_task(done, comment=…)`
// produced follow-up card `#0da96e03` from the closer's own closing note within
// the same second.
//
// The signal is deliberately about WHO and WHEN, never about what the text
// says: the most recent move on this card was made by this comment's author,
// moments ago, and the card is terminal now — so that move is the one that
// closed it, and this comment is that move's report. A text heuristic
// ("Закрываю", "closing") would be a second, weaker way to answer a question
// the activity log already answers exactly, and it would miss every language
// and phrasing nobody thought of.
//
// Fails OPEN (false → the follow-up is created) when the activity log cannot be
// read. That direction is chosen deliberately and it is the opposite of the
// usual fail-closed instinct: the cost of a wrong "true" is a swallowed remark,
// which is the defect this whole file exists to fix; the cost of a wrong
// "false" is one extra card the assignee can close.
func (s *commentService) commentIsOwnClosingReport(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
) bool {
	if s.activityRepo == nil {
		return false
	}
	page, err := s.activityRepo.ListByTask(ctx, task.ID, pagination.Params{Page: 1, PageSize: 10})
	if err != nil || page == nil {
		return false
	}

	// Scan for the latest move rather than trusting position. Postgres returns
	// this page created_at DESC, but the test double returns map order, and a
	// guard that is correct only under one repository's ordering is a guard
	// whose test cannot see it break.
	var lastMove *domain.ActivityLog
	for i := range page.Items {
		e := &page.Items[i]
		if e.Action != "task.moved" {
			continue
		}
		if lastMove == nil || e.CreatedAt.After(lastMove.CreatedAt) {
			lastMove = e
		}
	}
	if lastMove == nil {
		return false
	}

	// The comment's own author, not the request's — a comment carries the
	// identity it was written under, and that is the one being judged.
	authorID, authorType := comment.AuthorID, comment.AuthorType
	if authorID == uuid.Nil {
		// Fall back to the authenticated actor when the comment carries no
		// author of its own.
		authorID, authorType = actorctx.FromContext(ctx)
	}
	if lastMove.ActorID != authorID || lastMove.ActorType != authorType {
		return false
	}

	gap := comment.CreatedAt.Sub(lastMove.CreatedAt)
	if gap < 0 {
		gap = -gap
	}
	return gap <= closingReportWindow
}

// createClosedTaskFollowUp opens a follow-up card for a comment written on a
// closed card by someone other than its assignee.
//
// terminalTask is passed in rather than re-derived: Create has already resolved
// the task's status category to decide whether to suppress task.commented, and
// that is the SAME fact this mechanism turns on. Two lookups would be two
// notions of "is this card closed" free to disagree — the defect #4545660b
// removed from this same file one gate over.
//
// Every step is best-effort: a failure is logged and the comment still stands.
// The comment is already persisted by the time this runs, and it must remain
// so — this mechanism exists to ROUTE a remark, never to reject one.
func (s *commentService) createClosedTaskFollowUp(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
	terminalTask bool,
) {
	if closedFollowUpDisabled() {
		return
	}
	if s.taskSvc == nil || s.statusRepo == nil {
		return
	}
	// Branch: the card is not closed. An open card already wakes its assignee
	// through the ordinary task.commented path — adding a second card here
	// would duplicate a channel that works.
	if !terminalTask {
		return
	}
	// Branch: system-authored (including this mechanism's own reply comment,
	// which is what makes the whole thing non-recursive) — nothing to route.
	if comment.AuthorType == domain.ActorTypeSystem {
		return
	}
	// Branch: a driver wrote it. Drivers are not people with remarks; a
	// follow-up card per lease-reaper line would be pure noise.
	if isDriverComment(comment) {
		return
	}
	// Only an AGENT assignee is routed. A human assignee already has a real
	// notification channel for comment.created (in-app / push / email /
	// Telegram, see notificationService) — this mechanism exists because the
	// agent feed has no such channel, and manufacturing a card for someone who
	// was already told is noise, not delivery. Same scoping, and the same
	// reason, as the mention-handoff gate's agents-only rule.
	if task.AssigneeType != domain.AssigneeTypeAgent || task.AssigneeID == nil {
		log.Printf("[closed-followup] skip task=%s comment=%s — closed card has no agent assignee to route to",
			task.ID, comment.ID)
		return
	}
	// Branch: the assignee's own comment on their own closed card. A
	// post-mortem, a link, a correction to their own report — routing that back
	// to the person who just wrote it is a loop, not a delivery.
	if comment.AuthorID == *task.AssigneeID && comment.AuthorType == domain.ActorTypeAgent {
		return
	}
	// Branch: this comment IS the closing note of whoever just closed the card.
	// See commentIsOwnClosingReport — without this the mechanism turns every
	// routine cross-agent close into a follow-up card.
	if s.commentIsOwnClosingReport(ctx, comment, task) {
		return
	}

	// Dedup. One remark → one card, but a burst of remarks on the same closed
	// card in the same sitting (the measured precedent was two the same
	// morning) must not become a card each: the assignee opens the live
	// follow-up and the whole thread is on the closed card it points at.
	//
	// Recognised by this mechanism's OWN output — a still-open card carrying
	// followUpLabel with a relates_to edge onto this exact source card — not by
	// text similarity. Fails OPEN: if the edges cannot be read we create the
	// card, because a duplicate card is recoverable and a swallowed remark is
	// the thing we are fixing.
	if existing := s.liveFollowUpFor(ctx, task.ID); existing != nil {
		s.postFollowUpNotice(ctx, task, existing, comment, true)
		return
	}

	todoID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryTodo)
	if err != nil || todoID == uuid.Nil {
		// No todo column means the project has no status the agent feed polls,
		// so there is no card we could create that would wake anyone. Say so
		// rather than parking a card in whatever column happens to be first.
		log.Printf("[closed-followup] skip task=%s — project %s has no todo-category status (err=%v)",
			task.ID, task.ProjectID, err)
		return
	}

	assignee := *task.AssigneeID
	followUp := &domain.Task{
		ProjectID:    task.ProjectID,
		StatusID:     todoID,
		Title:        fmt.Sprintf("Замечание к #%s — %s", shortTaskID(task.ID), followUpTitleExcerpt(comment.Body)),
		Description:  followUpBody(comment, task),
		AssigneeID:   &assignee,
		AssigneeType: domain.AssigneeTypeAgent,
		// Priority is inherited rather than invented: a remark about shipped
		// work is worth what the work was worth, and any fixed value here
		// would be this code guessing at an importance it cannot see.
		Priority: task.Priority,
		Labels:   []string{followUpLabel},
		// Attributed to the commenter, who is the person actually raising it —
		// not to the system. A follow-up whose author is "system" would strand
		// the assignee with nobody to reply to.
		CreatedBy:       comment.AuthorID,
		CreatedByType:   comment.AuthorType,
		DelegationLevel: domain.DelegationLevelAuto,
	}
	if err := s.taskSvc.Create(ctx, followUp); err != nil {
		log.Printf("[closed-followup] WARNING: create follow-up for task=%s comment=%s failed: %v",
			task.ID, comment.ID, err)
		return
	}

	// relates_to, deliberately NOT blocks. A blocks edge onto a still-open
	// blocker freezes the feed (CLAUDE-workflow.md §ROUTE-gate) — which is the
	// exact opposite of what this card is for. The edge is here so the two
	// cards are navigable from each other, not to gate anything.
	if s.depRepo != nil {
		dep := &domain.TaskDependency{
			ID:              uuid.New(),
			TaskID:          followUp.ID,
			DependsOnTaskID: task.ID,
			DependencyType:  domain.DependencyTypeRelatesTo,
			CreatedAt:       timeNow(),
		}
		if err := s.depRepo.Create(ctx, dep); err != nil {
			log.Printf("[closed-followup] WARNING: relates_to edge %s→%s failed: %v",
				followUp.ID, task.ID, err)
		}
	}

	s.postFollowUpNotice(ctx, task, followUp, comment, false)

	log.Printf("[closed-followup] task=%s comment=%s author=%s/%s → follow-up=%s assignee=%s",
		task.ID, comment.ID, comment.AuthorType, comment.AuthorID, followUp.ID, assignee)
}

// followUpBody is the follow-up card's description: why it exists, the remark
// itself in full, and a way back to where it was written.
func followUpBody(comment *domain.Comment, task *domain.Task) string {
	author := string(comment.AuthorType)
	if comment.AuthorName != nil && strings.TrimSpace(*comment.AuthorName) != "" {
		author = *comment.AuthorName
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Замечание от **%s** к закрытой карточке #%s («%s»).\n\n", author, shortTaskID(task.ID), task.Title)
	b.WriteString("Заведено автоматически: закрытая карточка никого не будит — фиддлер подаёт только `todo`, " +
		"а @-меншен не хендофф. Комментарий в `done` остаётся непрочитанным, поэтому замечание вынесено " +
		"сюда, в карточку, которую фид действительно подаёт.\n\n---\n\n")
	b.WriteString(comment.Body)
	fmt.Fprintf(&b, "\n\n---\n\nИсточник: #%s, комментарий `%s`.\n", shortTaskID(task.ID), comment.ID)
	b.WriteString("Замечание не разобрано и не принято — решение по нему твоё; если оно не требует работы, закрой эту карточку с причиной.\n")
	return b.String()
}

// postFollowUpNotice writes the system comment that tells the commenter what
// happened to their remark. Written straight through commentRepo rather than
// s.Create, so it cannot re-enter any of the comment gates — and its author is
// system, which is the branch createClosedTaskFollowUp returns on first.
func (s *commentService) postFollowUpNotice(
	ctx context.Context,
	task *domain.Task,
	followUp *domain.Task,
	comment *domain.Comment,
	reused bool,
) {
	verb := "заведена"
	if reused {
		verb = "уже открыта"
	}
	now := timeNow()
	sys := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: закрытая карточка никого не будит — комментарий в `done` фиддлер не подаёт, "+
				"а @-меншен не хендофф. Замечание вынесено в карточку: %s #%s (`todo`, исполнитель прежний).",
			verb, shortTaskID(followUp.ID),
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sys); err != nil {
		log.Printf("[closed-followup] WARNING: notice comment on task=%s (follow-up=%s) failed: %v",
			task.ID, followUp.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
	_ = comment
}

// liveFollowUpFor returns this mechanism's own still-open follow-up card for
// sourceID, or nil when there is none.
//
// Fails OPEN (nil) on any read error — see the dedup note at the call site.
func (s *commentService) liveFollowUpFor(ctx context.Context, sourceID uuid.UUID) *domain.Task {
	if s.depRepo == nil {
		return nil
	}
	deps, err := s.depRepo.ListDependents(ctx, sourceID)
	if err != nil {
		return nil
	}
	for _, d := range deps {
		if d.DependencyType != domain.DependencyTypeRelatesTo {
			continue
		}
		cand, err := s.taskRepo.GetByID(ctx, d.TaskID)
		if err != nil || cand == nil {
			continue
		}
		if !hasLabel(cand.Labels, followUpLabel) {
			continue
		}
		st, err := s.statusRepo.GetByID(ctx, cand.StatusID)
		if err != nil || st == nil {
			continue
		}
		if st.Category == domain.StatusCategoryDone || st.Category == domain.StatusCategoryCancelled {
			continue
		}
		return cand
	}
	return nil
}

// hasLabel reports whether labels contains name, case-insensitively.
func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), name) {
			return true
		}
	}
	return false
}
