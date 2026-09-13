package service

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// This file closes the gap measured in audit §3.1 (task #9d8f7606, 2026-09):
// 606 @-mentions in one week, 72% never answered, only 3.8% delivered by the
// feed. The reason is CLAUDE-communication.md "How @-mentions wake": a
// fiddler-driven agent lane is woken ONLY by a card assigned to it and sitting
// in a todo-category status — an @-mention in a comment body reaches nothing.
// The sender does not know this; the comment publishes, the name renders
// highlighted, and the "handoff" the author believes they just made never
// happens. This gate refuses to persist that comment instead of accepting it
// silently, the same shape of fix as the delivery-outcome ledger
// (comment_delivery_outcome.go) it deliberately reuses the InTaskQueue fact
// from rather than re-deriving a second notion of "is there a queue path".

// fyiMentionRegex matches an explicit "fyi: @slug" pairing — the escape hatch
// from the handoff gate for a mention that names an agent purely for
// awareness, not as a work handoff. Same slug pattern and case-insensitivity
// as mentionRegex; the literal keyword "fyi" must be followed immediately by
// ":" and then the @mention with only optional whitespace between, so the
// ordinary word "fyi" appearing elsewhere in a comment does not accidentally
// exempt an unrelated mention. Symmetric with the "ℹ️ **FYI @user**" no-op
// marker documented above blockingMarkerRegex, but that one is about the
// blocking-triage gate, not this one — a comment may use either form, or
// both, independently.
var fyiMentionRegex = regexp.MustCompile(`(?i)(?:^|[\s(\[{])fyi\s*:\s*@([a-z0-9][a-z0-9-]{0,38}[a-z0-9])\b`)

// askContextWindowBytes is how far (in bytes, each side) around a single
// @-mention occurrence this gate looks for an ask-signal before deciding the
// mention reads as a hand-off attempt. "Соседство" (Riker's #ffa6e607
// decision, based on the full 7-day shadow-log read on #bb39554c) means
// near the mention, not anywhere in the comment — a question elsewhere in a
// multi-paragraph update about a different agent must not turn an unrelated
// addressed mention into a gated one. 80 bytes covers a short clause on
// either side (~40 Cyrillic characters, since each is 2 UTF-8 bytes) without
// reaching into an adjacent sentence in the bodies this gate actually sees.
const askContextWindowBytes = 80

// askImperativeWords are the lower-cased imperative verb forms #ffa6e607's
// decision names as the ask signal — "сделай", "нужен", "возьми" — plus the
// close synonyms the shadow-log sample (#bb39554c) actually used on the ask
// examples it found ("нужен вердикт", "разбери", "взгляни ещё раз",
// "проверь"), and the two most common English equivalents.
//
// Deliberately does NOT include the bare stem "делай": the canonical
// do-not-gate example this gate's own shadow-mode comment names below is
// "@garfield принято, правку не делай" — a NEGATED imperative — and a bare
// stem would match that too, re-introducing exactly the false positive the
// narrowing exists to remove. Every entry here is a distinct enough verb
// form that "не <verb>" reads as a real negated ask in Russian too ("не
// нужен" still means something is not needed of the mentioned agent) — the
// full-sentence judgment is left to a human reading the report, not chased
// here; the goal is cutting the 65-78% false-positive rate the report
// measured, not zero remaining edge cases.
//
// Each Russian verb is listed in the couple of inflected forms this gate
// actually needs to recognise (informal/formal imperative) rather than a
// regex stem+`\w*`: see containsAskImperative for why `\w*` cannot do this
// job for a Cyrillic suffix.
var askImperativeWords = []string{
	"сделай", "сделайте",
	"нужен", "нужна", "нужно", "нужны",
	"возьми", "возьмите",
	"проверь", "проверьте",
	"разбери", "разберите",
	"взгляни", "взгляните",
	"глянь", "гляньте",
	"почини", "почините",
	"исправь", "исправьте",
	"подтверди", "подтвердите",
	"ответь", "ответьте",
	"please", "can you", "could you",
}

// containsAskImperative reports whether window contains, as a standalone
// word/phrase, any askImperativeWords entry. Hand-rolled via the existing
// containsNegatorWholeWord (this file's package, one function over) rather
// than a regexp \b/\w check: Go's RE2 defines \w — and therefore \b — as
// ASCII [0-9A-Za-z_] only, so it never recognises a boundary next to a
// Cyrillic letter (a plain `\b(?:нужен|...)\b` regex was tried first here
// and silently matched nothing on ANY Cyrillic input — measured directly,
// not assumed). It also means a Cyrillic verb stem's `\w*` regex suffix
// cannot consume a Cyrillic inflectional ending either, since that
// `\w` is just as ASCII-only — hence listing inflected forms explicitly
// above instead of stem+`\w*`. containsNegatorWholeWord already solves
// exactly this boundary problem with a hand-rolled rune scan; reused rather
// than re-derived, same principle as mentionHasHandoff reusing
// decideDelivery's queue-path fact.
func containsAskImperative(window string) bool {
	lower := strings.ToLower(window)
	for _, w := range askImperativeWords {
		if containsNegatorWholeWord(lower, w) {
			return true
		}
	}
	return false
}

// mentionHasAskPattern reports whether AT LEAST ONE occurrence of @slug in
// body has an ask-signal — a question mark or a containsAskImperative match
// — within askContextWindowBytes on either side. A slug mentioned more than
// once in the same body (routine in a multi-turn thread) only needs ONE
// ask-shaped occurrence to count: the gate is about "was this ever used as
// an ask", not "was every occurrence one".
func mentionHasAskPattern(body, slug string) bool {
	for _, m := range mentionRegex.FindAllStringSubmatchIndex(body, -1) {
		if len(m) < 4 {
			continue
		}
		if !strings.EqualFold(body[m[2]:m[3]], slug) {
			continue
		}
		start := m[0] - askContextWindowBytes
		if start < 0 {
			start = 0
		}
		end := m[1] + askContextWindowBytes
		if end > len(body) {
			end = len(body)
		}
		window := body[start:end]
		if strings.ContainsRune(window, '?') || containsAskImperative(window) {
			return true
		}
	}
	return false
}

// mentionHandoffWindow is how recently an accompanying assign_task or
// create_subtask onto the mentioned agent must have landed for a plain
// @-mention to count as "accompanied by a hand-off" rather than a bare,
// undelivered ping. Chosen to match the workflow this gate is written
// against (CLAUDE-workflow.md §0: assign_task then move_task todo, or
// create_subtask, immediately before or after writing the comment) — not a
// tuning knob, a description of "the same breath".
const mentionHandoffWindow = 60 * time.Second

// agentMentionAlreadyWakes reports whether agent's own MentionWakes column
// (internal/domain/agent.go) claims a plain @-mention already has a
// fleet-side path to wake THIS agent without any Mesh-side queue state at
// all. Fails closed (false) on a nil agent: an agent must opt out
// explicitly.
//
// MentionWakes is a dedicated column, not a Capabilities key, because
// Capabilities is written by an incompatible second consumer
// (UpdateAgentProfile's config export/import, which expects a plain string
// array) that blind-replaces rather than merges — see #33b7d4b7. A single
// jsonb value could not hold both shapes at once; whichever write landed
// last silently destroyed the other, with no activity_log trail. That is
// exactly how the flag set on the "Riker" lane by migration 20260906004
// vanished by 2026-09-13 (#ba959606's red/green control caught it).
//
// As of 2026-09 no lane in the fleet is actually woken by a bare
// @-mention — Riker moved from the dispatcher (whose SSE listener used to
// spawn a session on task.mentioned) to fiddler, which is poll-only. This
// column exists for if/when a fleet-side listener like that comes back for
// some agent; that agent's row is the one that sets it true then. Which
// lane, if any, has a live listener is fleet-ops config on the Mac Mini
// (~/bin/mesh-agents.json vs ~/.config/fiddler/fiddler.json) — files the
// Mesh API process has no access to and no business reading even if it did,
// since the roster changes without a Mesh deploy. Hardcoding a slug here
// would silently go stale the next time that roster changes (exactly the
// failure mode `registry_stamp_is_documentation_not_a_gate` and
// `registry_profile_is_a_routing_control` already catalogue for this
// fleet). A flag on the agent's own row is a DATA change, not a CODE
// change, and is the row's own claim about itself rather than this gate's
// guess from a name.
func agentMentionAlreadyWakes(agent *domain.Agent) bool {
	if agent == nil {
		return false
	}
	return agent.MentionWakes
}

// fyiExemptSlugs returns the lowercase slugs that appear in an explicit
// "fyi: @slug" pairing anywhere in body. A mention of the SAME slug elsewhere
// in the same body without the "fyi:" prefix is still gated — the exemption
// is per occurrence-intent, not a blanket pass once the word appears once.
func fyiExemptSlugs(body string) map[string]bool {
	matches := fyiMentionRegex.FindAllStringSubmatch(body, -1)
	out := make(map[string]bool, len(matches))
	for _, m := range matches {
		out[strings.ToLower(m[1])] = true
	}
	return out
}

// MentionHandoffRequiredError reports that a comment @-mentions one or more
// agent lanes with no real path for them to see it: no live queue path
// (task assigned to them AND in a todo-category status) and no accompanying
// hand-off (assign_task or create_subtask onto them) in the preceding
// minute. The comment is refused unpersisted — see enforceMentionHandoffGate.
type MentionHandoffRequiredError struct {
	TaskID uuid.UUID
	// Slugs are the mentioned agent handles that have no queue path and no
	// recent accompanying assign_task/create_subtask. Order matches the
	// order the slugs first appeared in the comment body.
	Slugs []string
}

func (e *MentionHandoffRequiredError) Error() string {
	return fmt.Sprintf(
		"comment on task %s mentions agent(s) [%s] with no queue path and no "+
			"accompanying assign_task/create_subtask in the last minute — "+
			"a mention alone does not wake a fiddler-driven lane",
		e.TaskID, strings.Join(e.Slugs, ", "))
}

// mentionHasHandoff reports whether mentioning agent on task gives them a
// real path to see the comment:
//
//  1. The task is ALREADY theirs and sitting in a status they poll — the
//     exact fact decideDelivery calls delivered/task_queue. Reused rather
//     than re-derived: two implementations of "is there a queue path" is
//     the defect #4545660b removed from this same file one function over.
//  2. Failing that, an assign_task onto them landed on THIS task within the
//     handoff window, even if the follow-up move to a todo-category status
//     has not landed yet as a separate call — the assignment itself, this
//     recently, is the accompanying hand-off the gate asks for.
//  3. Failing that, a create_subtask onto them, under THIS task, landed
//     within the window — the mention on the PARENT is pointing at real
//     work that already exists somewhere the agent will see it, even though
//     the parent task's own assignee never changed.
func (s *commentService) mentionHasHandoff(
	ctx context.Context,
	task *domain.Task,
	agent *domain.Agent,
	taskInTodo bool,
	now time.Time,
) bool {
	assignedToAgent := task.AssigneeType == domain.AssigneeTypeAgent &&
		task.AssigneeID != nil && *task.AssigneeID == agent.ID

	if assignedToAgent && taskInTodo {
		return true
	}

	cutoff := now.Add(-mentionHandoffWindow)

	if assignedToAgent && s.activityRepo != nil {
		page, err := s.activityRepo.ListByTask(ctx, task.ID, pagination.Params{Page: 1, PageSize: 5})
		if err == nil {
			for _, e := range page.Items {
				if e.Action == "task.assigned" && !e.CreatedAt.Before(cutoff) {
					return true
				}
			}
		}
	}

	if s.taskRepo != nil {
		subtasks, err := s.taskRepo.ListSubtasks(ctx, task.ID)
		if err == nil {
			for _, st := range subtasks {
				if st.AssigneeType == domain.AssigneeTypeAgent &&
					st.AssigneeID != nil && *st.AssigneeID == agent.ID &&
					!st.CreatedAt.Before(cutoff) {
					return true
				}
			}
		}
	}

	return false
}

// enforceMentionHandoffGate refuses (via MentionHandoffRequiredError) a
// comment that @-mentions an agent lane with no path to reach it AND reads
// as an actual ask (mentionHasAskPattern) — unless the mention is explicitly
// marked "fyi:" or the agent's own row claims a fleet-side channel already
// wakes it (agentMentionAlreadyWakes).
//
// The ask-pattern requirement is the narrowing decided in #ffa6e607
// (2026-09-13), on the strength of the full 7-day shadow-log read on
// #bb39554c: 0 of 11 read cards were a real silent hand-off; the dominant
// class (48% from one coordinator alone) was ordinary in-thread addressing —
// "@garfield принято, правку не делай" — inside a thread already routed by a
// real assign_task/task-splitter/ROUTE elsewhere. Gating every bare @slug
// would have refused that routine traffic to catch zero confirmed misses.
// Narrowing to "@slug next to an imperative or a question mark, with no
// accompanying hand-off" is the alternative #9d8f7606 itself proposed.
//
// Deliberately scoped to AGENT recipients only. A human username (@pavel and
// anyone else) is never gated here: humans have a real notification channel
// (in-app/push/email/Telegram, see userHasMentionSubscription) that this
// gate has no business second-guessing, and an unresolved handle is an
// existing, separately-recorded case (delivery outcome
// skipped/recipient_unknown) — not a hand-off attempt this gate should
// judge. Self-mentions are exempt for the same reason decideDelivery treats
// them as terminal before anything else: nobody hands work to themselves.
func (s *commentService) enforceMentionHandoffGate(
	ctx context.Context,
	comment *domain.Comment,
	task *domain.Task,
	wsID uuid.UUID,
) error {
	if s.agentSvc == nil {
		return nil
	}
	slugs := extractMentionSlugs(comment.Body)
	if len(slugs) == 0 {
		return nil
	}
	exempt := fyiExemptSlugs(comment.Body)

	actorID, actorType := actorctx.FromContext(ctx)
	now := timeNow()
	taskInTodo := s.taskIsInTodoCategory(ctx, task)

	var blocked []string
	for _, slug := range slugs {
		if exempt[slug] {
			continue
		}
		agent, err := s.agentSvc.GetBySlug(ctx, wsID, slug)
		if err != nil || agent == nil {
			// Not an agent handle at all (unresolved, or a human username) —
			// out of scope for this gate.
			continue
		}
		if actorType == domain.ActorTypeAgent && actorID == agent.ID {
			continue
		}
		if agentMentionAlreadyWakes(agent) {
			continue
		}
		if s.mentionHasHandoff(ctx, task, agent, taskInTodo, now) {
			continue
		}
		if !mentionHasAskPattern(comment.Body, slug) {
			continue
		}
		blocked = append(blocked, slug)
	}

	if len(blocked) == 0 {
		return nil
	}

	// SHADOW MODE — the default until #ba959606 flips MENTION_HANDOFF_ENFORCE=1.
	//
	// The gate itself is correct: a bare @agent-slug does not wake a fiddler-driven
	// lane, so a comment that reads as a handoff delivers nothing. Pre-launch
	// verification measured what hard-enforcing the WIDE trigger (any bare @slug, no
	// ask-pattern requirement) would have done to CURRENT traffic, on prod, over 14 days:
	//
	//     8175 comments total
	//      426 mention a real agent slug
	//     ~272-331 (65-78% of those) would be refused on day one
	//
	// and a hand-inspection of a sample found most are NOT authors who believed they
	// had handed work over. They are ordinary in-thread addressing — "@garfield
	// принято, правку не делай." — which CLAUDE-communication.md §5a explicitly
	// endorses: you may address a paragraph to a colleague, the mention was never
	// claimed to deliver.
	//
	// That is why the WIDE trigger was never flipped. A full 7-day shadow-log read
	// (#bb39554c, 130 log lines / 97 cards, 11 cards read in full across all 13
	// authors) confirmed it live: 0 of 11 were a real silent hand-off, dominant class
	// was exactly this kind of addressing. #ffa6e607 decided NOT to gate every bare
	// @slug, and instead to require the ask-pattern check above
	// (mentionHasAskPattern) — a question mark or an imperative right next to the
	// mention — before a missing hand-off counts as "blocked" at all. `blocked` at
	// this point in the function already reflects that narrower set.
	//
	// ⚠️ A flag that defaults to off is normally how a guard dies quietly, which is a
	// failure mode this fleet has paid for repeatedly. The difference here is that the
	// flip is tracked on its own card (#ba959606) with the enabling condition written
	// down, and this log makes the not-yet-enforcing state loud rather than silent. If
	// you find this still defaulting to shadow long after that card closed, that IS
	// the rot — treat it as a bug, not as configuration.
	if !s.mentionHandoffEnforced() {
		log.Printf("[mention-handoff] SHADOW (not refused): task=%s author=%s/%s would_block=%v "+
			"— set MENTION_HANDOFF_ENFORCE=1 to enforce",
			task.ID, actorType, actorID, blocked)
		return nil
	}

	return &MentionHandoffRequiredError{TaskID: task.ID, Slugs: blocked}
}

// mentionHandoffEnforced reports whether the gate refuses (true) or only logs (false).
//
// Read from the environment on every call rather than cached at construction: flipping
// this is meant to be a config change plus a restart, and a cached value makes "I set the
// variable and nothing happened" a debugging session instead of an obvious no-op.
func (s *commentService) mentionHandoffEnforced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MENTION_HANDOFF_ENFORCE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
