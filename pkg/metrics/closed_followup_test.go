package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// The two Record helpers are the whole `closed_followup_error` and
// pending-outcome surface of the closed-card follow-up mechanism (#db1c6c7a):
// a delivery that used to vanish silently now has to MOVE a counter, or the
// fix's own signal is the next silent loss. Pinned per label pair because a
// CounterVec with the wrong labels silently records nothing — WithLabelValues
// with a swapped (stage, op) order would pass a naive "counter went up" check
// on the wrong series while the queried one stayed flat.
func TestRecordClosedFollowUpHelpers(t *testing.T) {
	RecordClosedFollowUpError("inline", "claim")
	assert.Equal(t, float64(1),
		testutil.ToFloat64(ClosedFollowUpErrorsTotal.WithLabelValues("inline", "claim")))
	RecordClosedFollowUpError("inline", "claim") // counters only go up
	assert.Equal(t, float64(2),
		testutil.ToFloat64(ClosedFollowUpErrorsTotal.WithLabelValues("inline", "claim")))
	RecordClosedFollowUpError("reconcile", "get_root")
	assert.Equal(t, float64(1),
		testutil.ToFloat64(ClosedFollowUpErrorsTotal.WithLabelValues("reconcile", "get_root")))
	assert.Equal(t, float64(0),
		testutil.ToFloat64(ClosedFollowUpErrorsTotal.WithLabelValues("reconcile", "claim")),
		"one op's failure must not bleed into another op's series")

	RecordClosedFollowUpPending("enqueued")
	RecordClosedFollowUpPending("delivered")
	RecordClosedFollowUpPending("delivered")
	assert.Equal(t, float64(1),
		testutil.ToFloat64(ClosedFollowUpPendingTotal.WithLabelValues("enqueued")))
	assert.Equal(t, float64(2),
		testutil.ToFloat64(ClosedFollowUpPendingTotal.WithLabelValues("delivered")))
}
