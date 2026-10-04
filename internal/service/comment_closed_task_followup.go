package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/metrics"
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

// followUpLabel marks a card this mechanism opened. Since #5194afd4 the dedup
// keys on the persistent (source card, finding) identity in
// closed_followup_roots, not on a label scan — but the label stays: it is what
// makes these cards recognisable in a queue to the human reading it.
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

// commentInformationalFlag is the cooperative field an author sets on their OWN
// comment (metadata.informational) to say "this is a pure acknowledgement, no
// action needed — don't route it" (task #df22e695, follow-up to #754173eb).
//
// Deliberately NOT a phrase filter over the body. The audit that raised this
// card (class B1, 2026-09-07) named the exact reason: this fleet's other
// text-shape guard for the same kind of question — hasNegatorInScope on a
// human_gate withdrawal — has misfired on real bodies at least five times
// ("Отзываю запрос." clears the gate; "Отзываю запрос — решение Павла." does
// not), and a second phrase heuristic here would reproduce that defect inside
// the very fix meant to remove noise. A structural, self-declared flag cannot
// misparse a sentence it never reads.
//
// The asymmetry this flag is built around is the opposite of driver detection
// above: a driver comment is recognised so the mechanism does NOT need to be
// told; an informational comment can ONLY be recognised by being told, because
// nothing about "нет возражений" vs "нужно поправить X" is structurally
// distinguishable without reading the words — and reading the words is exactly
// what the audit forbade. So the default stays "route it": an unflagged
// acknowledgement still opens a card (unchanged, current behaviour, nothing
// lost); only an EXPLICITLY flagged one is skipped. Forgetting the flag costs
// one avoidable card; a caller mistakenly guessing "informational" on a real
// remark would lose it silently — trust model matches commentMetadataSource
// just below: self-declared, honoured only to suppress a side effect on the
// author's OWN comment, never to grant anything.
func commentIsInformational(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	v, _ := m["informational"].(bool)
	return v
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

// findingIdentityMaxRunes caps metadata.finding_id — the contract says a
// finding id is a string of at most 128 characters. A longer or empty value is
// not an error; the comment simply falls back to the text key, which still
// dedups an identical repeat and still delivers a reworded one.
const findingIdentityMaxRunes = 128

// findingIdentityFromMetadata extracts the cooperative finding identity a
// detector may stamp on its own comment: metadata.finding_id plus optional
// metadata.finding_ns. Self-declared, exactly like metadata.source — honoured
// to GROUP the detector's own repeats, never to suppress anyone else's remark:
// a key collision only ever merges comments carrying the SAME id.
func findingIdentityFromMetadata(raw json.RawMessage) (ns, id string, ok bool) {
	if len(raw) == 0 {
		return "", "", false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", "", false
	}
	id, ok = m["finding_id"].(string)
	if !ok {
		return "", "", false
	}
	// Whitespace-only carries no identity (fall back to the text key), but a
	// surrounding-space value is NOT normalized away: the contract defines the
	// identity by the metadata values as written, and " x" vs "x" are
	// different findings (codex-review P1, MR !1078, round 9) — trimming here
	// would merge them exactly the way the raw-':' join merged (ns, id) pairs
	// in round 7.
	if strings.TrimSpace(id) == "" || len([]rune(id)) > findingIdentityMaxRunes {
		return "", "", false
	}
	ns, _ = m["finding_ns"].(string)
	return ns, id, true
}

// normalizeFindingText is the legacy key's normalization: trim, collapse all
// whitespace runs to one space, lowercase. Deliberately EXACTLY this and
// nothing fuzzy — the contract forbids similarity matching: a reworded remark
// is a different finding and must be delivered, not silently merged by a
// heuristic that guesses it "means the same".
func normalizeFindingText(body string) string {
	return strings.ToLower(strings.Join(strings.Fields(body), " "))
}

// escapeFindingKeyComponent keeps the "id:<ns>:<id>" join injective: ':' is
// the join's delimiter, so a raw colon inside a component would let two
// DIFFERENT (ns, id) pairs serialize to one key and merge distinct findings
// (codex-review P1, MR !1078, round 7) — the exact "only ever merges comments
// carrying the SAME id" promise findingIdentityFromMetadata's doc makes.
// Backslash escapes itself first, so the mapping stays one-to-one;
// components without ':' or '\' serialize byte-identically to the plain
// contract format.
func escapeFindingKeyComponent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, ":", `\:`)
}

// closedFindingKey is the identity of a remark on a closed card: the pair
// (source card, key) is one finding for this mechanism's whole memory. An
// explicit metadata identity wins when present ("id:<ns>:<id>", components
// escaped so the join is injective); anything else falls back to the
// normalized text ("txt:" + sha256 hex).
func closedFindingKey(comment *domain.Comment) string {
	if ns, id, ok := findingIdentityFromMetadata(comment.Metadata); ok {
		return "id:" + escapeFindingKeyComponent(ns) + ":" + escapeFindingKeyComponent(id)
	}
	sum := sha256.Sum256([]byte(normalizeFindingText(comment.Body)))
	return "txt:" + hex.EncodeToString(sum[:])
}

// followUpReopenWindow and followUpReopenWindowLimit are the storm guard: at
// most 3 reopens of the same root inside any 24h window; the 4th repeat (and
// every further one until the window goes quiet for 24h) still lands as a
// comment on the root and a notice on the source, but wakes nobody. A
// permanent lock would trade a storm of cards for a silently dead finding —
// the exact defect this file exists to fix — so the limit expires instead.
const (
	// One canonical window constant and one canonical limit, shared with the
	// SQL side through the repository package — see
	// repository.ClosedFollowUpReopenWindow for why two independent constants
	// here would be a silent-drift bug.
	followUpReopenWindow      = repository.ClosedFollowUpReopenWindow
	followUpReopenWindowLimit = repository.ClosedFollowUpReopenLimit
)

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
	// Branch: the author marked this comment metadata.informational — a pure
	// acknowledgement, nothing to act on (task #df22e695). Overridden by a live
	// "❓ Blocking @pavel" marker in the SAME body: a gate marker means a human
	// still needs to see this, and no self-declared flag is allowed to make a
	// real ask disappear. Checked with hasBlockingMarker, not by re-deriving
	// whether the gate actually armed — the marker's presence is the thing that
	// must not be swallowed, independent of whether enforceBlockingTriage later
	// accepts it.
	if commentIsInformational(comment.Metadata) && !hasBlockingMarker(comment.Body) {
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

	// Identity and dedup (#5194afd4, contract approved on #9b712414). A
	// finding is the pair (source card, finding_key): an explicit
	// metadata.finding_id when a detector stamps one, else the normalized
	// text. One pair → one root for the pair's WHOLE life: an open root
	// absorbs repeats, a closed root is reopened, and a different finding
	// (different id, or different normalized text) is never absorbed by any
	// root of another finding.
	//
	// The claim is INSERT ... ON CONFLICT DO NOTHING BEFORE the card is
	// created: two concurrent remarks with the same key cannot both become
	// cards — the loser reads the winner's root and takes the repeat branch.
	//
	// Without the roots store there is no honest dedup left at all (the old
	// label-scan absorbed DIFFERENT findings into one open root, which the
	// contract forbids), so the mechanism is off rather than half-on.
	if s.followUpRoots == nil {
		return
	}
	// P2 (#db1c6c7a): the delivery runs on the inline retry ladder, and a
	// finding that survives the whole ladder undelivered is parked in the
	// pending queue with a visible notice — a read error is never "dedup
	// clean", and it is never silence either.
	if err := s.deliverClosedFollowUpWithRetry(ctx, task, comment); err != nil {
		s.enqueueClosedFollowUpPending(ctx, task, comment, err)
	}
}

// followUpRetryError marks a delivery failure that is TRANSIENT in the
// mechanism's eyes: the identity store could not be read or written, the
// root card not fetched, the reopen not moved — today's outage, not a
// permanent property of the remark. Exactly these climb the inline ladder
// and, when it is exhausted, land in the pending queue. Permanent outcomes
// (a project with no todo column, a storm-limited repeat, a wedged winner)
// return nil and never queue.
//
// op names the failing operation for the closed_followup_error metric — a
// bounded label set ("claim", "get_root", "status_read", ...), never the raw
// error string, whose cardinality is unbounded. The cause itself rides in
// the log line beside the counter.
type followUpRetryError struct {
	op  string
	err error
}

func (e *followUpRetryError) Error() string { return e.op + ": " + e.err.Error() }
func (e *followUpRetryError) Unwrap() error { return e.err }

// followUpOp extracts the metric label from a delivery error; anything that
// is not a followUpRetryError (a plain error from a path not yet wrapped)
// still counts, as "unknown".
func followUpOp(err error) string {
	var re *followUpRetryError
	if errors.As(err, &re) {
		return re.op
	}
	return "unknown"
}

// followUpDeliveryRetryBackoff paces the inline retry ladder (#db1c6c7a):
// one initial attempt plus up to three retries, 100/300/900 ms apart — the
// contract's exact ladder. The whole ladder lives inside the SAME comment
// request, so its ceiling bounds what a failing database can add to a
// comment's latency. Package vars so tests collapse the sleeps.
var followUpDeliveryRetryBackoff = []time.Duration{
	100 * time.Millisecond, 300 * time.Millisecond, 900 * time.Millisecond,
}

// followUpDeliveryRetrySleep is time.Sleep in production; tests swap it out
// so the ladder runs instantly and deterministically.
var followUpDeliveryRetrySleep = time.Sleep

// deliverClosedFollowUpWithRetry runs one delivery attempt and retries the
// transient ones on the ladder. Every exit leaves the already-persisted
// comment standing: a failure here routes, it never rejects.
func (s *commentService) deliverClosedFollowUpWithRetry(
	ctx context.Context,
	task *domain.Task,
	comment *domain.Comment,
) error {
	attempts := 1 + len(followUpDeliveryRetryBackoff)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			// The request's context is the one thing a retry cannot outlast:
			// once the caller is gone the remaining rungs have no reader, and
			// the pending queue is the honest place for the finding.
			if ctx.Err() != nil {
				return lastErr
			}
			followUpDeliveryRetrySleep(followUpDeliveryRetryBackoff[attempt-2])
		}
		lastErr = s.claimAndCreateRoot(ctx, task, comment, true)
		if lastErr == nil {
			return nil
		}
		log.Printf("[closed-followup] delivery attempt %d/%d for task=%s comment=%s failed: %v",
			attempt, attempts, task.ID, comment.ID, lastErr)
	}
	return lastErr
}

// enqueueClosedFollowUpPending parks a finding that survived the whole inline
// ladder undelivered (#db1c6c7a): the closed_followup_error signal fires, a
// pending row goes to the reconcile queue, and the commenter is told on the
// source card — delivery unconfirmed, retry automatic. When even the pending
// row cannot be written, the database is wholly down: that is the ONE
// residual case (named in the MR), logged at ERROR with its own metric label;
// the remark then rides on the source comment alone until a human or a later
// repeat of the finding re-enters delivery.
func (s *commentService) enqueueClosedFollowUpPending(
	ctx context.Context,
	task *domain.Task,
	comment *domain.Comment,
	deliveryErr error,
) {
	// The requester may be GONE — a cancelled request context is one of the
	// ways the ladder ends — but the comment is already saved, so the park
	// must complete without them: the queue write runs detached from the
	// request's cancellation while keeping its values (codex-review P2).
	// Without this, a healthy pending store would reject the row on the dead
	// context and the finding would ride on the source comment alone.
	ctx = context.WithoutCancel(ctx)
	op := followUpOp(deliveryErr)
	metrics.RecordClosedFollowUpError("inline", op)
	log.Printf("[closed-followup] closed_followup_error op=%s task=%s comment=%s: delivery unconfirmed after %d inline attempts, parking for reconcile: %v",
		op, task.ID, comment.ID, 1+len(followUpDeliveryRetryBackoff), deliveryErr)
	if s.followUpPending == nil {
		// Non-server build (no queue wired): P1's logged-return behavior is
		// the floor; the signal above has already fired.
		return
	}
	row := &domain.ClosedFollowUpPending{
		CommentID:    comment.ID,
		SourceTaskID: task.ID,
		FindingKey:   closedFindingKey(comment),
		Attempts:     0,
		LastError:    deliveryErr.Error(),
		CreatedAt:    timeNow(),
	}
	if err := s.followUpPending.Enqueue(ctx, row); err != nil {
		metrics.RecordClosedFollowUpError("enqueue", "enqueue_pending")
		log.Printf("[closed-followup] closed_followup_error ERROR op=enqueue_pending task=%s comment=%s: pending row not written either (residual case, database wholly down): %v",
			task.ID, comment.ID, err)
		return
	}
	metrics.RecordClosedFollowUpPending("enqueued")
	s.ensurePendingNotice(ctx, "enqueue", task.ID, comment.ID)
}

// ensurePendingNotice makes the park VISIBLE: the «не подтверждена» notice
// lands on the source card, and only then is noticed_at written. A failed
// notice write is not swallowed into a log line — the row keeps noticed_at
// NULL and every reconcile pass that works it retries the notice, the same
// durability the escalation notice got in round 3. A queued finding its own
// commenter cannot see is a queue, not a delivery (codex-review P1, round 4).
//
// Reports whether the notice is UP — posted by this call, or already on the
// card with only the stamp write lagging. The caller needs the answer because
// its row copy predates the stamp: a pass that lands the notice mid-loop must
// not go on reading the copy's NULL (codex-review P1, round 6).
func (s *commentService) ensurePendingNotice(ctx context.Context, stage string, sourceTaskID, commentID uuid.UUID) bool {
	if err := s.postPendingNotice(ctx, sourceTaskID, commentID); err != nil {
		metrics.RecordClosedFollowUpError(stage, "pending_notice")
		log.Printf("[closed-followup] WARNING: pending notice task=%s comment=%s not posted (a reconcile pass retries it): %v",
			sourceTaskID, commentID, err)
		return false
	}
	if err := s.followUpPending.MarkNoticed(ctx, commentID, timeNow()); err != nil {
		// The notice is up but the stamp is not: one duplicate notice may
		// follow on a later pass — noise, never silence. First stamp wins.
		log.Printf("[closed-followup] WARNING: pending notice task=%s comment=%s landed but noticed_at not written (one duplicate may follow): %v",
			sourceTaskID, commentID, err)
	}
	return true
}

// postPendingNotice is the visible half of parking a finding: a system
// comment on the source card telling the commenter their remark has not
// reached a follow-up card yet and will be retried automatically. Straight
// through commentRepo (system author), the same re-entry discipline as
// postFollowUpNotice — and its "🤖 Auto:" opening is a driver prefix, so even
// a future reader of this very comment cannot re-trigger the mechanism.
// The error is returned, not logged here: the caller owns the retry policy
// (ensurePendingNotice stamps noticed_at only after the write lands).
func (s *commentService) postPendingNotice(ctx context.Context, sourceTaskID, commentID uuid.UUID) error {
	now := timeNow()
	sys := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     sourceTaskID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: доставка замечания (комментарий `%s`) не подтверждена — сбой чтения при поиске карточки. "+
				"Замечание не потеряно: повторю автоматически (до %d попыток), о доставке сообщу здесь.",
			commentID, ClosedFollowUpReconcileMaxAttempts,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sys); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, sourceTaskID)
	}
	return nil
}

// The reconcile half of P2 (#db1c6c7a): the pending queue is drained by a
// periodic pass over closed_followup_pending. Interval is the scheduler's
// cadence; MaxAttempts is where automation stops and a human is asked —
// reached long after the inline ladder (4 attempts in one request), so a
// finding has had 1 + 3 + 10 = 14 chances before the escalation notice.
// Batch bounds one pass's work against a queue that grew during an outage.
const (
	ClosedFollowUpReconcileInterval    = 5 * time.Minute
	ClosedFollowUpReconcileMaxAttempts = 10
	closedFollowUpReconcileBatch       = 50
)

// ReconcileClosedFollowUpPending drains the pending queue: every row still
// under the attempt budget gets its delivery re-run — the SAME function the
// inline path used, so a finding cannot reconcile into a different shape
// than it would have delivered live. Rows at or past the budget whose
// escalation notice has not landed are worked for the NOTICE alone. Rows
// whose comment or source card no longer exists are dropped (nothing left to
// deliver), not counted as failures: a deleted remark is not an outage. The
// pass never re-filters the finding through createClosedTaskFollowUp's gates
// — the row exists precisely because it passed them once, and re-judging it
// against a card whose state has since changed would let a close-and-reopened
// source silently eat the parked remark.
//
// The caller (cmd/api scheduler) runs this on a system actor context;
// claimAndCreateRoot may create cards through taskSvc.Create, which writes
// the actor into activity_log.
//
// Concurrency invariant (codex-review round 7): passes are SINGLE-FLIGHT —
// the scheduler runs the pass inline in its ticker loop, so one process never
// works two passes at once, and the deployment (deploy/docker/mesh) runs one
// api container. ListDue therefore needs no lease: a row it returns is worked
// by exactly one pass. If the api is ever scaled past one replica, add a
// per-row claim (a claimed_until lease column) BEFORE the replica ships —
// until then the worst an overlapping inline delivery of the same comment
// can do is a duplicate system comment and one extra reopen slot, because
// Claim and TryReopen stay atomic and the notices are once-ever by stamp.
func (s *commentService) ReconcileClosedFollowUpPending(ctx context.Context) (delivered, retried, escalated int, err error) {
	if closedFollowUpDisabled() {
		return 0, 0, 0, nil
	}
	if s.followUpPending == nil || s.followUpRoots == nil || s.taskSvc == nil || s.statusRepo == nil {
		// Queue not wired (non-server build): nothing to reconcile.
		return 0, 0, 0, nil
	}
	due, err := s.followUpPending.ListDue(ctx, ClosedFollowUpReconcileMaxAttempts, closedFollowUpReconcileBatch)
	if err != nil {
		metrics.RecordClosedFollowUpError("reconcile", "list_pending")
		log.Printf("[closed-followup-reconcile] ERROR listing pending: %v", err)
		return 0, 0, 0, err
	}
	for i := range due {
		if ctx.Err() != nil {
			break
		}
		row := &due[i]

		// A row at or past the budget is listed for its NOTICES alone. The
		// finding's delivery is not retried — the budget bought the human,
		// not a resurrection — but visibility is TWO stamps, and the row
		// leaves the due set only when both have landed: retiring it on the
		// escalation alone would strand a park notice that never landed
		// (codex-review P1, round 5 — round 3 made the escalation durable,
		// round 4 the park notice; this is their intersection).
		if row.Attempts >= ClosedFollowUpReconcileMaxAttempts {
			if row.NoticedAt == nil {
				s.ensurePendingNotice(ctx, "reconcile", row.SourceTaskID, row.CommentID)
			}
			if row.EscalatedAt != nil {
				// The escalation already landed; this pass works the row
				// only for its park notice, and it stays listed until that
				// one lands too.
				retried++
				continue
			}
			if s.deliverEscalationNotice(ctx, row, fmt.Errorf("%s", row.LastError)) {
				escalated++
			} else {
				retried++
			}
			continue
		}

		comment, err := s.commentRepo.GetByID(ctx, row.CommentID)
		if err != nil {
			if s.markPendingAttempt(ctx, row, "comment_read", err) {
				escalated++
			}
			retried++
			continue
		}
		if comment == nil {
			// The remark itself is gone — deleted by an admin, wiped with the
			// card. Nothing left to deliver; holding the row would just burn
			// its attempts against a future that cannot happen.
			_ = s.followUpPending.Delete(ctx, row.CommentID)
			continue
		}
		task, err := s.taskRepo.GetByID(ctx, row.SourceTaskID)
		if err != nil {
			if s.markPendingAttempt(ctx, row, "task_read", err) {
				escalated++
			}
			retried++
			continue
		}
		if task == nil {
			_ = s.followUpPending.Delete(ctx, row.CommentID)
			continue
		}

		// The park must be visible to its commenter before more delivery
		// work: a notice that failed to write at enqueue time is retried
		// here, on every pass that works the row, until it lands — a queued
		// finding nobody can see is a queue, not a delivery (codex-review
		// P1, round 4). noticed_at makes it once-ever per row. The answer is
		// carried in a local, not re-read from row: the copy predates the
		// stamp this call may write (round 6).
		noticed := row.NoticedAt != nil
		if !noticed {
			noticed = s.ensurePendingNotice(ctx, "reconcile", row.SourceTaskID, row.CommentID)
		}

		if err := s.claimAndCreateRoot(ctx, task, comment, true); err != nil {
			if s.markPendingAttempt(ctx, row, followUpOp(err), err) {
				escalated++
			}
			retried++
			continue
		}
		// Delivered: the finding has its root (or its permanent terminal
		// outcome — wedged winner, no todo column — which is equally done).
		// But the delete must not discard an unlanded park notice
		// (codex-review P1, round 6): postFollowUpNotice inside the
		// delivery is best-effort, so a row whose «не подтверждена» write
		// failed at enqueue AND this pass keeps its retry path — it stays
		// due, the next pass re-posts the notice and re-delivers
		// idempotently, and only a landed notice lets the row go. `noticed`
		// is the pass-local truth: the stamp this same pass may have
		// written lives in the store, not in the stale row copy.
		if !noticed {
			log.Printf("[closed-followup-reconcile] WARNING: delivered comment=%s but its park notice has not landed — row kept, notice retried next pass",
				row.CommentID)
			metrics.RecordClosedFollowUpPending("delivered")
			delivered++
			continue
		}
		if derr := s.followUpPending.Delete(ctx, row.CommentID); derr != nil {
			log.Printf("[closed-followup-reconcile] WARNING: delivered comment=%s but pending row not deleted (it will re-deliver idempotently next pass): %v",
				row.CommentID, derr)
		}
		metrics.RecordClosedFollowUpPending("delivered")
		delivered++
		log.Printf("[closed-followup-reconcile] delivered comment=%s key=%q after %d attempt(s)",
			row.CommentID, row.FindingKey, row.Attempts+1)
	}
	return delivered, retried, escalated, nil
}

// markPendingAttempt records one failed reconcile pass on a pending row: the
// closed_followup_error signal, the attempt counter, and — exactly once, at
// the transition to the budget's edge — the escalation notice asking for a
// human. Reports whether THIS call escalated. ListDue stops listing rows at
// the budget, so the escalation posts once, on the pass that reaches it,
// never again after.
func (s *commentService) markPendingAttempt(
	ctx context.Context,
	row *domain.ClosedFollowUpPending,
	op string,
	cause error,
) bool {
	metrics.RecordClosedFollowUpError("reconcile", op)
	attempts, err := s.followUpPending.MarkAttempt(ctx, row.CommentID, cause.Error())
	if err != nil {
		// The counter itself is unwritable: log and move on — a row whose
		// count we cannot advance is re-listed next pass with its old count,
		// and the delivery retries anyway. The budget's only job is deciding
		// WHEN to escalate, not WHETHER to retry.
		log.Printf("[closed-followup-reconcile] WARNING: mark attempt comment=%s failed (counter unwritten): %v",
			row.CommentID, err)
		return false
	}
	if attempts < ClosedFollowUpReconcileMaxAttempts {
		log.Printf("[closed-followup-reconcile] attempt %d/%d comment=%s op=%s: %v",
			attempts, ClosedFollowUpReconcileMaxAttempts, row.CommentID, op, cause)
		return false
	}
	if attempts > ClosedFollowUpReconcileMaxAttempts {
		// Two passes raced on one row: both listed it at 9, and MarkAttempt
		// — atomic — handed each a unique post-increment value, 10 and 11.
		// Only the pass that drew exactly the limit is the transition to
		// "needs a human"; the one going past it must not post a second
		// «нужен человек» notice (codex-review P2). The row is not re-listed
		// past the limit either way, so nothing is lost by the refusal.
		log.Printf("[closed-followup-reconcile] comment=%s already past the budget (attempts=%d) — escalation already posted, not repeating",
			row.CommentID, attempts)
		return false
	}
	log.Printf("[closed-followup-reconcile] escalated comment=%s after %d attempts (op=%s): %v",
		row.CommentID, attempts, op, cause)
	// The transition counted; the notice's own landing is
	// deliverEscalationNotice's job — and a failed write leaves escalated_at
	// unset, which is exactly what keeps the row listed for the retry above.
	s.deliverEscalationNotice(ctx, row, cause)
	return true
}

// deliverEscalationNotice posts the «нужен человек» notice and, when it
// LANDS, persists completion (escalated_at) so the row leaves the due set.
// Called from the budget's transition and again by every later pass while
// the notice has not landed — the escalation's delivery is as durable as the
// finding's was, because a decision a human never reads is the silent loss
// this whole mechanism exists to remove (codex-review P1, round 3).
func (s *commentService) deliverEscalationNotice(
	ctx context.Context,
	row *domain.ClosedFollowUpPending,
	cause error,
) bool {
	if err := s.postEscalationNotice(ctx, row, cause); err != nil {
		metrics.RecordClosedFollowUpError("reconcile", "escalation_notice")
		log.Printf("[closed-followup-reconcile] WARNING: escalation notice comment=%s not posted (a later pass retries it): %v",
			row.CommentID, err)
		return false
	}
	if err := s.followUpPending.MarkEscalated(ctx, row.CommentID, timeNow()); err != nil {
		// The notice is up but the completion stamp is not: the row stays in
		// the due set and one later pass posts a single duplicate before the
		// stamp lands. Accepted noise — retiring the row here instead would
		// be the silent loss again, on the write that exists to end them.
		log.Printf("[closed-followup-reconcile] WARNING: escalation notice landed comment=%s but escalated_at not written (one duplicate may follow): %v",
			row.CommentID, err)
	}
	return true
}

// followUpCauseExcerpt caps an error's text for a human-facing comment: the
// cause travels whole in logs and in the row's last_error, the comment only
// needs enough to recognize the failure.
func followUpCauseExcerpt(cause string) string {
	runes := []rune(cause)
	if len(runes) <= 200 {
		return cause
	}
	return strings.TrimSpace(string(runes[:200])) + "…"
}

// postEscalationNotice writes the «нужен человек» comment on the source card
// — the end of the automatic road: the finding could not be delivered after
// the whole budget of retries, and pretending otherwise would be the silent
// loss this whole mechanism exists to remove. The row STAYS in the table (a
// human finding it there sees the full last_error); what retires it from the
// due set is the escalated_at stamp, written by the caller only once THIS
// write has landed. The remark itself still stands as the original comment.
// Returns the write's error: the pending-outcome counter fires on landing,
// because a decision whose comment never landed reaches nobody.
func (s *commentService) postEscalationNotice(ctx context.Context, row *domain.ClosedFollowUpPending, cause error) error {
	now := timeNow()
	sys := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     row.SourceTaskID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: доставка замечания (комментарий `%s`) не удалась после %d автоматических повторов — нужен человек. Причина последней ошибки: %s",
			row.CommentID, ClosedFollowUpReconcileMaxAttempts, followUpCauseExcerpt(cause.Error()),
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sys); err != nil {
		return err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, row.SourceTaskID)
	}
	metrics.RecordClosedFollowUpPending("escalated")
	return nil
}

// claimAndCreateRoot is the identity half of createClosedTaskFollowUp: claim
// the (source, finding key) pair, then — for the claim's winner — create the
// root card under exactly the claimed ID. allowTakeover=false is passed only
// one level down a takeover chain (see deliverRepeatFinding): a released
// claim re-enters here once, and that second level must not attempt a third —
// a pathological release loop would otherwise recurse for as long as
// contention kept reproducing it.
//
// The error contract (#db1c6c7a): nil means delivered OR permanently skipped
// (no todo column, storm-limited, wedged winner — outcomes the retry ladder
// cannot improve and the pending queue must not re-offer); a non-nil
// followUpRetryError means TRANSIENT — today the caller retries it inline,
// tonight the reconcile job does.
func (s *commentService) claimAndCreateRoot(
	ctx context.Context,
	task *domain.Task,
	comment *domain.Comment,
	allowTakeover bool,
) error {
	findingKey := closedFindingKey(comment)

	candidateID := uuid.New()
	claimed, err := s.followUpRoots.Claim(ctx, task.ID, findingKey, candidateID, timeNow())
	if err != nil {
		// Fail to NO CARD, not to an orphan: a card created outside the
		// identity store would be invisible to every later repeat of the
		// same finding — the duplicate-card defect back again, just delayed.
		// Since P2 the failure is not silent either: it climbs the inline
		// ladder and, failing that, the pending queue.
		return &followUpRetryError{op: "claim", err: err}
	}
	if !claimed {
		return s.deliverRepeatFinding(ctx, task, comment, findingKey, allowTakeover)
	}

	todoID, err := findStatusIDByCategory(ctx, s.statusRepo, task.ProjectID, domain.StatusCategoryTodo)
	if err != nil {
		// The statuses could not be READ — transient, retryable. The claim is
		// released first: a retry re-claims cleanly, and a held claim would
		// point at a card that may never exist.
		log.Printf("[closed-followup] skip task=%s — todo status read failed (err=%v)",
			task.ID, err)
		s.releaseClaim(ctx, task.ID, findingKey)
		return &followUpRetryError{op: "todo_status_read", err: err}
	}
	if todoID == uuid.Nil {
		// No todo column is a PERMANENT property of the project: it has no
		// status the agent feed polls, so there is no card we could create
		// that would wake anyone. Say so rather than parking a card in
		// whatever column happens to be first. The claim is released too: a
		// held claim would point at a card that never exists and send every
		// later repeat of this finding into the repeat branch with no root
		// behind it. Not retryable — retrying cannot add a todo column.
		log.Printf("[closed-followup] skip task=%s — project %s has no todo-category status",
			task.ID, task.ProjectID)
		s.releaseClaim(ctx, task.ID, findingKey)
		return nil
	}

	assignee := *task.AssigneeID
	followUp := &domain.Task{
		// The claimed identity: this ID is already the root_task_id row the
		// repeat branch will read, so the created card must be exactly this
		// one — the claim and the card cannot disagree.
		ID:           candidateID,
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
		// Compensation: the claim already points at candidateID, which now will
		// never be a card. Release it so the next repeat of this finding claims
		// cleanly instead of being routed to a root that does not exist —
		// and the release itself is what makes a retry honest: it re-claims
		// with a fresh candidate instead of colliding with its own ghost.
		s.releaseClaim(ctx, task.ID, findingKey)
		return &followUpRetryError{op: "create_root", err: err}
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

	s.postFollowUpNotice(ctx, task, followUp, comment, followUpCreated)

	log.Printf("[closed-followup] task=%s comment=%s author=%s/%s → follow-up=%s assignee=%s",
		task.ID, comment.ID, comment.AuthorType, comment.AuthorID, followUp.ID, assignee)
	return nil
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

// followUpNoticeOutcome is what the notice on the SOURCE card tells the
// commenter happened to their remark. Four outcomes, one root per finding —
// see the identity note in createClosedTaskFollowUp.
type followUpNoticeOutcome int

const (
	// followUpCreated: the finding was new, a root card was opened in todo.
	followUpCreated followUpNoticeOutcome = iota
	// followUpAlreadyOpen: the finding's root exists and is still open — the
	// assignee's feed already carries it, no second card for the same finding.
	followUpAlreadyOpen
	// followUpReopened: the root existed and had been closed; it is back in
	// todo with the assignee unchanged and the repeat appended as a comment.
	followUpReopened
	// followUpStormLimited: the root was closed but the 24h reopen limit is
	// exhausted — no reopen, the repeat is a comment on the root and a notice
	// here; nobody is woken for the 4th time in a day.
	followUpStormLimited
)

// postFollowUpNotice writes the system comment that tells the commenter what
// happened to their remark. Written straight through commentRepo rather than
// s.Create, so it cannot re-enter any of the comment gates — and its author is
// system, which is the branch createClosedTaskFollowUp returns on first.
func (s *commentService) postFollowUpNotice(
	ctx context.Context,
	task *domain.Task,
	followUp *domain.Task,
	comment *domain.Comment,
	outcome followUpNoticeOutcome,
) {
	verb := "заведена"
	switch outcome {
	case followUpAlreadyOpen:
		verb = "уже открыта"
	case followUpReopened:
		verb = "переоткрыта (снова `todo`, исполнитель прежний)"
	case followUpStormLimited:
		verb = "не переоткрыта — исчерпан лимит 3 переоткрытий за 24 ч; повтор приложен комментарием к карточке"
	}
	body := fmt.Sprintf(
		"🤖 Auto: закрытая карточка никого не будит — комментарий в `done` фиддлер не подаёт, "+
			"а @-меншен не хендофф. Замечание вынесено в карточку: %s #%s.",
		verb, shortTaskID(followUp.ID),
	)
	if outcome == followUpReopened || outcome == followUpStormLimited {
		body += fmt.Sprintf(" Источник: #%s, комментарий `%s`.", shortTaskID(task.ID), comment.ID)
	}
	now := timeNow()
	sys := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body:       body,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.commentRepo.Create(ctx, sys); err != nil {
		log.Printf("[closed-followup] WARNING: notice comment on task=%s (follow-up=%s) failed: %v",
			task.ID, followUp.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}

// deliverRepeatFinding routes a finding whose root already exists: an OPEN
// root absorbs the repeat as a notice; a CLOSED root is REOPENED — same card
// back in todo, assignee untouched, the repeat appended as a comment carrying
// the source-comment link, counted against the 24h storm limit. A different
// finding never reaches here: it claims a different key above and gets its own
// root.
//
// The root card may not exist YET: the winner's claim row is visible before
// its card is, so the lookup waits out that race (waitForRootCard) instead of
// dropping the repeat on it. When the card never appears, two honest cases:
// the claim VANISHED while waiting — the winner released it (no todo column,
// failed create), the finding has no root and no owner, and this call takes
// over the creation, once (allowTakeover); or the claim persists with no card
// — a wedged winner — and the repeat is deferred visibly rather than
// swallowed.
//
// Error contract as in claimAndCreateRoot (#db1c6c7a): transient failures
// (the store unreadable, the reopen unwritable) return a followUpRetryError
// for the ladder and the pending queue; delivered and permanent outcomes
// return nil. On a retryable exit nothing has been posted yet — the retry
// delivers whole, and a later attempt must not find half the remarks already
// on the root.
func (s *commentService) deliverRepeatFinding(
	ctx context.Context,
	task *domain.Task,
	comment *domain.Comment,
	findingKey string,
	allowTakeover bool,
) error {
	row, err := s.followUpRoots.Get(ctx, task.ID, findingKey)
	if err != nil {
		// The identity store could not be read: transient, retryable — a
		// working read may still find the root and deliver the repeat.
		return &followUpRetryError{op: "get_root", err: err}
	}
	if row == nil {
		// The claim lost to a row that vanished — a release that raced us,
		// or a manual cleanup. There is no root to deliver to and no honest
		// way to make one from INSIDE the repeat branch — but a retry of the
		// whole delivery re-enters through Claim, which mints the root this
		// finding is now missing.
		return &followUpRetryError{op: "get_root", err: fmt.Errorf("root row vanished for key %q", findingKey)}
	}
	root, err := s.waitForRootCard(ctx, row.RootTaskID)
	if err != nil {
		// The root card could not be READ (or the caller went away): reads
		// never succeeded, so this is not the wedged-winner branch below —
		// it is a transient outage, and the honest route is the ladder and,
		// failing that, the pending queue (codex-review P1 on this MR).
		return &followUpRetryError{op: "root_card_read", err: err}
	}
	if root == nil {
		rowNow, errNow := s.followUpRoots.Get(ctx, task.ID, findingKey)
		if errNow == nil && rowNow == nil && allowTakeover {
			log.Printf("[closed-followup] task=%s key=%q: claim vanished while waiting for root=%s — taking over creation",
				task.ID, findingKey, row.RootTaskID)
			return s.claimAndCreateRoot(ctx, task, comment, false)
		}
		// Wedged winner: the claim persists with no card behind it. Reads
		// SUCCEEDED — this is not an outage the ladder can wait out, so it
		// stays P1's terminal deferral (visible notice, next repeat retries)
		// rather than ten reconciles of the same unfixable wedge.
		log.Printf("[closed-followup] WARNING: root %s for task=%s key=%q not visible after %d attempts (claim present=%v) — repeat deferred",
			row.RootTaskID, task.ID, findingKey, len(followUpRootRetryBackoff), rowNow != nil)
		s.postDeferredRepeatNotice(ctx, task, comment)
		return nil
	}
	st, err := s.statusRepo.GetByID(ctx, root.StatusID)
	if err != nil {
		return &followUpRetryError{op: "status_read", err: err}
	}
	if st == nil {
		// The root points at a status row that does not resolve — broken
		// referential state, not an outage. Permanent for this attempt:
		// deliver as storm-limited-style comment so the remark is not lost.
		log.Printf("[closed-followup] WARNING: status %s of root %s resolves to nothing",
			root.StatusID, root.ID)
		s.postRepeatOnRoot(ctx, task, root, comment, false)
		return nil
	}

	if st.Category != domain.StatusCategoryDone && st.Category != domain.StatusCategoryCancelled {
		// Root still open: its assignee's feed already carries this finding; a
		// second card for the SAME finding is the duplicate this table exists
		// to prevent. The commenter still learns where the remark went.
		s.postFollowUpNotice(ctx, task, root, comment, followUpAlreadyOpen)
		return nil
	}

	// Closed root — same finding again, same card again. The storm limit
	// first, and ATOMICALLY with the count it reads: a read-check-record
	// split let N concurrent repeats of this finding all pass the check and
	// all record a reopen — a count above the number of repeats actually
	// delivered, suppressing a later one prematurely (codex-review P1, MR
	// !1078). TryReopen refuses the limit+1-th increment in the same UPDATE
	// that records the reopen, so the count can never exceed the limit
	// inside a window. The slot is held BEFORE the steps below that can
	// fail; every failure exit returns it (compensateFailedReopen) — the
	// budget counts delivered reopens, never attempts (codex-review P2).
	reopenAt := timeNow()
	reopened, claim, err := s.followUpRoots.TryReopen(ctx, task.ID, findingKey, reopenAt)
	if err != nil {
		return &followUpRetryError{op: "try_reopen", err: err}
	}
	if !reopened {
		// The window is exhausted: the close/reopen cycle is spinning, and
		// waking it again is the storm, not the delivery. The repeat still
		// lands on the root and the commenter is still told — nothing
		// swallowed, nobody woken.
		s.postRepeatOnRoot(ctx, task, root, comment, false)
		s.postFollowUpNotice(ctx, task, root, comment, followUpStormLimited)
		return nil
	}

	todoID, err := findStatusIDByCategory(ctx, s.statusRepo, root.ProjectID, domain.StatusCategoryTodo)
	if err != nil {
		// Transient read failure: the slot goes back (the reopen did not
		// deliver), nothing is posted, and the retry takes a fresh slot.
		s.compensateFailedReopen(ctx, task, findingKey, root.ID, claim)
		return &followUpRetryError{op: "todo_status_read", err: err}
	}
	if todoID == uuid.Nil {
		// Permanent: the project has no todo column to move the root to.
		s.compensateFailedReopen(ctx, task, findingKey, root.ID, claim)
		s.postRepeatOnRoot(ctx, task, root, comment, false)
		log.Printf("[closed-followup] WARNING: cannot reopen root=%s — project %s has no todo-category status",
			root.ID, root.ProjectID)
		return nil
	}
	// A shipped root cannot MoveTask to todo — TaskShippedError is the guard,
	// and its only documented escape hatch is clearing the flag first. The
	// flag means "fix deployed and verified live"; a repeat of the same
	// finding is precisely the disproof of that, so the reopen honestly costs
	// the flag rather than bypassing the guard.
	wasShipped := root.IsShipped
	if wasShipped {
		if err := s.taskSvc.ShipTask(ctx, root.ID, false); err != nil {
			s.compensateFailedReopen(ctx, task, findingKey, root.ID, claim)
			return &followUpRetryError{op: "ship_flag", err: err}
		}
	}
	if err := s.taskSvc.MoveTask(ctx, root.ID, MoveTaskInput{StatusID: &todoID}); err != nil {
		// The flag was cleared above and the root stays closed: a reopen that
		// did not happen must not cost it. Best-effort, like the slot return.
		if wasShipped {
			if rerr := s.taskSvc.ShipTask(ctx, root.ID, true); rerr != nil {
				log.Printf("[closed-followup] WARNING: restore shipped on root=%s after failed move failed: %v",
					root.ID, rerr)
			}
		}
		s.compensateFailedReopen(ctx, task, findingKey, root.ID, claim)
		return &followUpRetryError{op: "reopen_move", err: err}
	}
	s.postRepeatOnRoot(ctx, task, root, comment, true)
	s.postFollowUpNotice(ctx, task, root, comment, followUpReopened)
	log.Printf("[closed-followup] task=%s comment=%s key=%q → reopened root=%s",
		task.ID, comment.ID, findingKey, root.ID)
	return nil
}

// compensateFailedReopen returns a storm-limit slot taken by a reopen that
// never delivered: TryReopen holds it atomically, but the todo lookup, the
// shipped-flag clear and the move can still fail after the slot is taken,
// and slots burned by failures would let the window exhaust on attempts
// rather than reopens (codex-review P2, MR !1078). The restore puts back the
// claim's whole pre-claim state — count and window anchor both: a count-only
// return leaves the window stretched over a reopen that never happened and
// storm-limits the next healthy one (round 5). The repeat body still lands
// on the closed root — a refused move is not a licence to swallow the
// finding. Best-effort by design: a failed compensation leaks one slot into
// the window (it decays with it) and never propagates its failure into the
// comment path this mechanism serves.
func (s *commentService) compensateFailedReopen(ctx context.Context, task *domain.Task, findingKey string, rootID uuid.UUID, claim repository.ReopenClaim) {
	if err := s.followUpRoots.CompensateReopen(ctx, task.ID, findingKey, claim); err != nil {
		log.Printf("[closed-followup] WARNING: reopen compensation task=%s key=%q root=%s failed: %v",
			task.ID, findingKey, rootID, err)
	}
}

// followUpRootRetryBackoff paces waitForRootCard's lookups. The winner's
// remaining work after its claim is one SELECT (the todo column) plus one
// INSERT (the card) — milliseconds — so ~3 s of backoff covers a busy
// database many times over while never holding the comment request for long.
// A package var so tests can collapse the sleeps.
var followUpRootRetryBackoff = []time.Duration{
	50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond,
	400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond,
}

// followUpRootRetrySleep is time.Sleep in production; tests swap it out so
// the wait logic runs instantly and deterministically.
var followUpRootRetrySleep = time.Sleep

// waitForRootCard reads the root card, retrying while the winner's claim is
// ahead of its card. Three exits, three meanings (#db1c6c7a, codex-review
// P1): the card → (card, nil); the budget spent where the LAST read
// succeeded and found nothing → (nil, nil), the honest absent/wedged case
// the caller resolves by takeover or deferral; and anything else →
// (nil, err), a transient state the caller must route to the retry ladder
// and the pending queue. The LAST attempt decides, not any earlier one: a
// read that succeeded mid-budget does not settle absence for good — the
// winner may still be inserting its card — so if the budget ENDS on a
// failed read the card's state is unknown and the honest answer is
// retryable, never a wedged verdict on evidence that predates the failure.
func (s *commentService) waitForRootCard(ctx context.Context, rootTaskID uuid.UUID) (*domain.Task, error) {
	var lastErr error
	for i, pause := range followUpRootRetryBackoff {
		root, err := s.taskRepo.GetByID(ctx, rootTaskID)
		if err == nil && root != nil {
			return root, nil
		}
		lastErr = err // nil on a successful read that found nothing
		if err != nil {
			log.Printf("[closed-followup] WARNING: root lookup %s attempt %d/%d: %v",
				rootTaskID, i+1, len(followUpRootRetryBackoff), err)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		followUpRootRetrySleep(pause)
	}
	if lastErr != nil {
		// The budget ended on a failed read (or every read failed): the
		// card's state is unknown — transient by the taxonomy of every
		// other read here.
		return nil, lastErr
	}
	return nil, nil // the last read succeeded and found nothing: absence
}

// postDeferredRepeatNotice is the wedged-winner fallback: the repeat could
// not be routed to a root within the wait budget, and is NOT lost — the
// remark itself is the comment this notice sits under, and the next repeat of
// the finding re-enters delivery with a fresh budget. Written straight
// through commentRepo (system author), same re-entry discipline as
// postFollowUpNotice.
func (s *commentService) postDeferredRepeatNotice(ctx context.Context, task *domain.Task, comment *domain.Comment) {
	now := timeNow()
	sys := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     task.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: повтор замечания (комментарий `%s`) пока не доставлен в карточку — root ещё не виден (внутренняя гонка). "+
				"Замечание не потеряно; следующий повтор попробует снова.",
			comment.ID,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sys); err != nil {
		log.Printf("[closed-followup] WARNING: deferred-repeat notice on task=%s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}

// postRepeatOnRoot writes the repeat itself onto the root as a system comment
// — the body plus the exact source-comment link — so the reopened card opens
// with the new remark on it, and the storm-limited case still delivers the
// remark somewhere a human reads it. Straight through commentRepo (system
// author), same discipline as postFollowUpNotice: it cannot re-enter the
// comment gates, and createClosedTaskFollowUp returns on system authors
// before ever reaching the identity store, so it cannot re-enter this
// mechanism either.
func (s *commentService) postRepeatOnRoot(
	ctx context.Context,
	task *domain.Task,
	root *domain.Task,
	comment *domain.Comment,
	reopened bool,
) {
	verb := "карточка переоткрыта (повтор того же замечания)"
	if !reopened {
		verb = "карточка НЕ переоткрыта — исчерпан лимит переоткрытий (3 за 24 ч), повтор приложён"
	}
	now := timeNow()
	sys := &domain.Comment{
		ID:         uuid.New(),
		TaskID:     root.ID,
		AuthorID:   systemActorID,
		AuthorType: domain.ActorTypeSystem,
		Body: fmt.Sprintf(
			"🤖 Auto: повтор того же замечания на закрытой карточке #%s — %s.\n\n---\n\n%s\n\n---\n\nИсточник: #%s, комментарий `%s`.",
			shortTaskID(task.ID), verb, comment.Body, shortTaskID(task.ID), comment.ID,
		),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, sys); err != nil {
		log.Printf("[closed-followup] WARNING: repeat comment on root=%s failed: %v", root.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, root.ID)
	}
}

// releaseClaim drops a claim whose card never came to exist (no todo column,
// card create failed). Best-effort by design: a leak means a later repeat
// reads a root_task_id that resolves to no card and is logged as a warning
// there — recoverable by hand, unlike a phantom claim silently eating every
// future repeat of the finding.
func (s *commentService) releaseClaim(ctx context.Context, sourceID uuid.UUID, findingKey string) {
	if err := s.followUpRoots.Delete(ctx, sourceID, findingKey); err != nil {
		log.Printf("[closed-followup] WARNING: claim release failed for task=%s key=%q: %v",
			sourceID, findingKey, err)
	}
}

// hasLabel moved to task_service.go as hasDupCandidateLabel (#5194afd4): its
// last generic caller here died with the label-scan dedup, and every remaining
// call site passes dupCandidateLabel.
