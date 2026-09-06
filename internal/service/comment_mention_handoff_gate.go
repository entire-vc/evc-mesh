package service

import (
	"context"
	"encoding/json"
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

// mentionHandoffWindow is how recently an accompanying assign_task or
// create_subtask onto the mentioned agent must have landed for a plain
// @-mention to count as "accompanied by a hand-off" rather than a bare,
// undelivered ping. Chosen to match the workflow this gate is written
// against (CLAUDE-workflow.md §0: assign_task then move_task todo, or
// create_subtask, immediately before or after writing the comment) — not a
// tuning knob, a description of "the same breath".
const mentionHandoffWindow = 60 * time.Second

// mentionWakesCapabilityKey is a boolean an agent's Capabilities JSON
// (internal/domain/agent.go) may carry to opt out of this gate entirely: true
// means a plain @-mention already has a fleet-side path to wake THIS agent
// without any Mesh-side queue state at all.
//
// As of 2026-09 exactly one agent (the dispatcher-driven "Riker" lane) is
// actually woken by a bare @-mention (its SSE listener spawns a session on
// task.mentioned); every other lane is fiddler-driven and is not. That
// distinction lives entirely in fleet-ops config on the Mac Mini
// (~/bin/mesh-agents.json vs ~/.config/fiddler/fiddler.json) — files the Mesh
// API process has no access to and no business reading even if it did, since
// the roster changes without a Mesh deploy. Hardcoding a slug here would
// silently go stale the next time that roster changes (exactly the failure
// mode `registry_stamp_is_documentation_not_a_gate` and
// `registry_profile_is_a_routing_control` already catalogue for this fleet).
// A capability flag on the agent's own row is a DATA change, not a CODE
// change, and is the row's own claim about itself rather than this gate's
// guess from a name.
//
// Nothing sets this flag today — until the dispatcher-driven lane's row
// carries {"mention_wakes": true}, this gate treats it like every other
// agent. See the task's closing report for the follow-up this implies.
const mentionWakesCapabilityKey = "mention_wakes"

// agentMentionAlreadyWakes reports whether agent's own Capabilities row
// claims a plain @-mention already reaches it through a fleet-side channel
// this gate cannot see. Fails closed (false) on absent/malformed
// capabilities: an agent must opt out explicitly, an unreadable claim is not
// one.
func agentMentionAlreadyWakes(agent *domain.Agent) bool {
	if agent == nil || len(agent.Capabilities) == 0 {
		return false
	}
	var caps map[string]any
	if err := json.Unmarshal(agent.Capabilities, &caps); err != nil {
		return false
	}
	v, _ := caps[mentionWakesCapabilityKey].(bool)
	return v
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
// comment that @-mentions an agent lane with no path to reach it, unless the
// mention is explicitly marked "fyi:" or the agent's own row claims a
// fleet-side channel already wakes it (agentMentionAlreadyWakes).
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
		blocked = append(blocked, slug)
	}

	if len(blocked) == 0 {
		return nil
	}

	// SHADOW MODE — the default, deliberately.
	//
	// The gate itself is correct: a bare @agent-slug does not wake a fiddler-driven
	// lane, so a comment that reads as a handoff delivers nothing. But independent
	// verification measured what hard-enforcing would do to CURRENT traffic, on prod,
	// over 14 days:
	//
	//     8175 comments total
	//      426 mention a real agent slug
	//     ~272-331 (65-78% of those) would be refused on day one
	//
	// and a hand-inspection of a sample found most are NOT authors who believed they
	// had handed work over. They are ordinary in-thread addressing — "@garfield
	// принято, правку не делай." — which CLAUDE-communication.md §5a explicitly
	// endorses: you may address a paragraph to a colleague, the mention was never
	// claimed to deliver. This gate cannot tell that apart from a mis-believed handoff
	// and would refuse both.
	//
	// Shipping that as an unannounced fleet-wide 422 would break most inter-agent
	// conversation to fix a subset of it. So enforcement is off until the fleet has
	// been told and the real volume is known from the log below — the measurement is
	// the point, and it cannot be taken without shipping the detector first.
	//
	// ⚠️ A flag that defaults to off is normally how a guard dies quietly, which is a
	// failure mode this fleet has paid for repeatedly. The difference here is that the
	// flip is tracked on its own card with the enabling condition written down, and
	// this log makes the not-yet-enforcing state loud rather than silent. If you find
	// this still defaulting to shadow long after that card closed, that IS the rot —
	// treat it as a bug, not as configuration.
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
