package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Parity guards for BacklogPromotionAdvisoryService (#c15c503a, prerequisite of the
// enforcing cutover #f42fe0a8). Each function below is ported one-to-one from
// bob/scripts/mesh-intake-sweep.py and bob/scripts/human_gate.py; the Python name is
// in each doc comment. Where the server CANNOT reproduce a sweep behaviour (it has no
// comment repository here), it errs toward HOLD: a server that holds where the sweep
// promotes shows up as `sweep_only` in the divergence journal and costs a delay,
// while the opposite direction would move a card that is waiting on a person.

// backlogAbsoluteNoPromoteLabels mirrors ABSOLUTE_NO_PROMOTE_LABELS: an explicit
// human freeze or an Agent-Eval fixture. A passed due_date never overrides these;
// only an explicit wake:<type> label does.
var backlogAbsoluteNoPromoteLabels = map[string]struct{}{
	"freeze": {}, "no-intake-promote": {}, "no-promote": {},
	"golden": {}, "eval-harness": {},
}

// backlogEvalFixtureLabels mirrors EVAL_FIXTURE_LABELS.
var backlogEvalFixtureLabels = map[string]struct{}{"golden": {}, "eval-harness": {}}

func hasAnyLabel(labels []string, set map[string]struct{}) bool {
	for _, l := range labels {
		if _, ok := set[l]; ok {
			return true
		}
	}
	return false
}

// --- human gate (human_gate.py: is_human_gated, cheap path) ----------------------

var (
	labelSepRE    = regexp.MustCompile(`[\s:_/-]+`)
	labelFillerRE = regexp.MustCompile(`on(?:pavel|human|macbook)`)
	pavelAskRE    = regexp.MustCompile(`(?m)^[\s#*_❓]*blocking\s*@?\s*pavel`)
	codeFenceRE   = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRE  = regexp.MustCompile("`[^`\n]*`")
)

// normLabel mirrors human_gate.norm_label: case, separators and the `on` filler
// dropped, so `blocked:pavel` and `blocked-on-pavel` collide.
func normLabel(l string) string {
	s := labelSepRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(l)), "")
	return labelFillerRE.ReplaceAllStringFunc(s, func(m string) string { return m[2:] })
}

// humanGateLabelsN = HUMAN_GATE_LABELS ∪ HUMAN_GATE_LABELS_RELEASABLE, normalised.
// The releasable tier is treated as structural here: releasing it needs Pavel's
// comment, and this service reads no comments — hold is the safe direction.
var humanGateLabelsN = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, l := range []string{
		"decision", "blocked:pavel", "blocking:pavel", "needs:pavel",
		"kind:decision", "kind:human-verify", "human-verify",
		"needs:human", "needs:macbook", "host:macbook",
		"blocked-on-pavel", "blocking-on-pavel", "blocked-on-human",
	} {
		m[normLabel(l)] = struct{}{}
	}
	return m
}()

// maskCode blanks fenced and inline code, like human_gate._mask_code: a marker QUOTED
// as code documents the mechanism and must not arm it.
func maskCode(s string) string {
	blank := func(m string) string { return strings.Repeat(" ", len(m)) }
	return inlineCodeRE.ReplaceAllStringFunc(codeFenceRE.ReplaceAllStringFunc(s, blank), blank)
}

// hasPavelAsk mirrors human_gate.has_pavel_ask.
func hasPavelAsk(body string) bool {
	return pavelAskRE.MatchString(maskCode(strings.ToLower(body)))
}

// humanGateReason returns why the task is waiting on a human, or "" if it is not.
// Ports the cheap (no-comments) path of is_human_gated: assignee=user, supervised,
// the server flag, a gate label, and a marker written in the description.
// Deliberately NOT ported: the release tiers that read comments/decisions
// (human_answered, has_live_decision) and the description negator scan — the server
// holds where the sweep's comment-aware second pass would release.
func humanGateReason(t *domain.Task) string {
	if t.AssigneeType == domain.AssigneeTypeUser {
		return "human-gated: assignee=user"
	}
	if t.DelegationLevel == domain.DelegationLevelSupervised {
		return "human-gated: delegation_level=supervised"
	}
	if t.HumanGate {
		return "human-gated: human_gate armed"
	}
	for _, l := range t.Labels {
		if _, ok := humanGateLabelsN[normLabel(l)]; ok {
			return fmt.Sprintf("human-gated: label %q", l)
		}
	}
	if hasPavelAsk(t.Description) {
		return "human-gated: `Blocking @pavel` in description"
	}
	return ""
}

// --- parent gate (parent_gates_children / parent_is_eval_fixture) ----------------

type parentVerdict struct {
	gatesChildren bool
	evalFixture   bool
}

// parentGatesChildren mirrors parent_gates_children: supervised, human_gate, or an
// ABSOLUTE_NO_PROMOTE label on the master freezes the whole subtree.
func parentGatesChildren(p *domain.Task) bool {
	if p.DelegationLevel == domain.DelegationLevelSupervised || p.HumanGate {
		return true
	}
	return hasAnyLabel(p.Labels, backlogAbsoluteNoPromoteLabels)
}

// parentOf fetches a parent once per tick. A parent that cannot be read (error or
// gone) is an error, which the caller turns into fail-closed no-promote — the sweep
// raises on the same condition.
func (s *backlogPromotionAdvisoryService) parentOf(
	ctx context.Context, id uuid.UUID, cache map[uuid.UUID]parentVerdict,
) (parentVerdict, error) {
	if v, ok := cache[id]; ok {
		return v, nil
	}
	p, err := s.taskRepo.GetByID(ctx, id)
	if err != nil {
		return parentVerdict{}, err
	}
	if p == nil {
		return parentVerdict{}, fmt.Errorf("parent %s not found", id)
	}
	v := parentVerdict{gatesChildren: parentGatesChildren(p), evalFixture: hasAnyLabel(p.Labels, backlogEvalFixtureLabels)}
	cache[id] = v
	return v, nil
}

// --- park labels + wake overrides (due_wake_overrides_park, wake:<type>) ---------

// wakeType mirrors wake_type(): the alphabetically-first recognised wake:<type>.
func wakeType(labels []string) string {
	known := map[string]struct{}{
		"date": {}, "all_children_closed": {}, "dependency_edges": {},
		"external_task_done": {}, "task_done": {}, "log_threshold": {},
		"delete_after": {}, "manual": {},
	}
	best := ""
	for _, l := range labels {
		suffix, ok := strings.CutPrefix(l, "wake:")
		if !ok {
			continue
		}
		if _, ok := known[suffix]; ok && (best == "" || suffix < best) {
			best = suffix
		}
	}
	return best
}

func (s *backlogPromotionAdvisoryService) dueDateArrived(t *domain.Task) bool {
	return t.DueDate != nil && !t.DueDate.After(s.now())
}

// evaluateWake decides whether the task is held by a park label after the wake
// rules, returning (hold, reason). Order mirrors sweep(): wake:delete_after /
// wake:manual never promote; a passed due_date expires a NON-absolute park
// (due_wake_overrides_park); an explicit wake:date / wake:dependency_edges licenses
// the override past an absolute label; the condition-comment wake types are held
// (their audit comment is not read server-side, fail closed).
//
// Not ported: the QUEUE-BEHIND override (a passive-wait label is lifted when open
// work depends on the card). It needs the dependents' statuses; the journal shows it
// as `sweep_only` on 4 cards, all on 2026-09-10 — the safe direction.
func (s *backlogPromotionAdvisoryService) evaluateWake(t *domain.Task) (hold bool, reason string) {
	wt := wakeType(t.Labels)
	arrived := s.dueDateArrived(t)
	if arrived && (wt == "delete_after" || wt == "manual") {
		return true, fmt.Sprintf("wake:%s never promotes", wt)
	}

	passive, isPassive := hasBacklogParkLabel(t.Labels)
	if !isPassive {
		return false, ""
	}
	absolute := hasAnyLabel(t.Labels, backlogAbsoluteNoPromoteLabels)

	if !absolute && arrived {
		return false, "" // park expired by its own due_date
	}
	if absolute && wt != "" && arrived {
		switch wt {
		case "date", "dependency_edges":
			return false, "" // explicit wake licence; dependency guard below still applies
		default:
			return true, fmt.Sprintf("wake:%s condition not evaluated server-side (fail closed), label %q", wt, passive)
		}
	}
	return true, fmt.Sprintf("passive-wait label %q", passive)
}
