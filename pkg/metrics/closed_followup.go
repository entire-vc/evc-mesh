package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ClosedFollowUpErrorsTotal is the `closed_followup_error` signal of the
	// closed-card follow-up mechanism (#db1c6c7a): every delivery failure that
	// used to be a silent fail-open. stage tells WHERE it failed —
	// "inline" (the request-path retry ladder gave up, finding parked in
	// pending), "enqueue" (the residual case: even the pending row could not
	// be written, the database is wholly down), "reconcile" (a 5-minute retry
	// attempt failed again) — and op names the failing operation ("claim",
	// "get_root", "root_card_read", "status_read", "create_root", "reopen",
	// "escalation_notice", "pending_notice", "claim_lease" (the reconcile
	// lease store itself unreadable, #a2368528), ...) rather than the
	// raw error string, which would be unbounded label cardinality. The full
	// error text rides in the log line next to the counter.
	ClosedFollowUpErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_closed_followup_errors_total",
			Help: "Closed-card follow-up delivery failures by stage and operation (closed_followup_error).",
		},
		[]string{"stage", "op"},
	)

	// ClosedFollowUpPendingTotal counts the queue's outcomes: a finding
	// enqueued ("enqueued"), re-delivered by reconcile ("delivered"), or
	// escalated to a human after the attempt budget ran out ("escalated" —
	// counted when the «нужен человек» notice LANDS, not when the budget
	// decides: a decision nobody reads is not an escalation).
	ClosedFollowUpPendingTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_closed_followup_pending_total",
			Help: "Closed-card follow-up pending queue outcomes: enqueued, delivered by reconcile, escalated to a human.",
		},
		[]string{"outcome"},
	)
)

// RecordClosedFollowUpError increments the closed_followup_error counter.
func RecordClosedFollowUpError(stage, op string) {
	ClosedFollowUpErrorsTotal.WithLabelValues(stage, op).Inc()
}

// RecordClosedFollowUpPending increments a pending-queue outcome.
func RecordClosedFollowUpPending(outcome string) {
	ClosedFollowUpPendingTotal.WithLabelValues(outcome).Inc()
}
