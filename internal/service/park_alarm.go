package service

import (
	"time"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// parkAlarmLabels are the labels that declare a backlog card to be a PASSIVE WAIT —
// "I am not working on this right now; wake me when the window closes" (kind:monitor,
// CLAUDE-workflow-reference.md §0m) or "I am waiting on a verification window"
// (phase:verify). Both mean the card is idle by design, and both are read by the
// promotion sweeper as "there is nothing to do here until something else happens".
//
// The set is deliberately EXACTLY these two and not the wider
// backlogPassiveWaitLabels vocabulary in backlog_promotion_advisory.go. This set
// drives a 422 — a hard refusal of a write a caller is trying to make — and widening
// a refusal is the dangerous direction: every extra label here is a park somebody can
// no longer perform. The wider vocabulary drives a *skip*, whose failure direction is
// merely "stays in backlog". Audit 2026-09-07 (#559270cf) measured 34 of 56 cards
// carrying one of these two labels with no due_date at all — 11 of them already in the
// "parked with no exit" bucket — which is the population this gate exists to stop
// growing.
var parkAlarmLabels = map[string]struct{}{
	"kind:monitor": {},
	"phase:verify": {},
}

// hasParkAlarmLabel returns the first passive-wait label found and whether any is set.
func hasParkAlarmLabel(labels []string) (string, bool) {
	for _, l := range labels {
		if _, ok := parkAlarmLabels[l]; ok {
			return l, true
		}
	}
	return "", false
}

// hasArmedAlarm reports whether a task carries a due_date that can still fire.
//
// A due_date in the PAST is deliberately NOT an armed alarm, matching
// checkoutLeaseReaper.parkTask's own definition ("A previously-fired alarm is
// deliberately NOT treated as armed"). The two have to agree: MonitorPromotionService
// promotes any backlog card whose due_date has passed, so accepting a past date here
// would let a caller create a park that the very next sweeper tick undoes — a park
// that does not park, reported to the caller as success.
func hasArmedAlarm(due *time.Time) bool {
	return due != nil && due.After(timeNow())
}

// ParkAlarmError is returned when a caller tries to leave a task parked in backlog
// under a passive-wait label without a future due_date to wake it — a park with no
// exit. Surfaces as 422.
type ParkAlarmError struct {
	Label string
}

func (e *ParkAlarmError) Error() string {
	return "park without a wake-up: this task carries the passive-wait label \"" + e.Label +
		"\", so nothing will feed it again while it sits in backlog — no agent feed polls " +
		"backlog, and the promotion sweeper fires on due_date. Set a future due_date on the " +
		"task first (PATCH /tasks/:id with due_date) and retry, or drop the label if the card " +
		"is genuinely ready to be picked up"
}

// wouldBeAlarmlessPark reports whether the given (labels, due_date) pair, resting in a
// backlog-category status, is a park with no wake-up path.
func wouldBeAlarmlessPark(labels []string, due *time.Time) (string, bool) {
	label, labelled := hasParkAlarmLabel(labels)
	if !labelled {
		return "", false
	}
	if hasArmedAlarm(due) {
		return "", false
	}
	return label, true
}

// alarmStateUnchanged reports whether an update leaves the park-alarm situation exactly
// as it found it — same passive-wait label present or absent, same armed/unarmed
// due_date verdict.
//
// This is what keeps the gate from turning the 34 already-alarmless cards the audit
// found into cards nobody can edit. The gate's job is to stop NEW alarmless parks being
// created, not to hold an existing one hostage: refusing every write to an
// already-broken card would mean the only way to fix its title, assignee or labels is
// to first satisfy a rule about a field the caller may not be touching at all. A caller
// who does not make the situation worse is let through unchanged; a caller who creates
// or re-creates the alarmless state is refused.
func alarmStateUnchanged(oldLabels []string, oldDue *time.Time, newLabels []string, newDue *time.Time) bool {
	oldLabel, oldBad := wouldBeAlarmlessPark(oldLabels, oldDue)
	newLabel, newBad := wouldBeAlarmlessPark(newLabels, newDue)
	return oldBad == newBad && oldLabel == newLabel
}

// isBacklogCategory is a small readability helper used by both gate call sites.
func isBacklogCategory(c domain.StatusCategory) bool {
	return c == domain.StatusCategoryBacklog
}
