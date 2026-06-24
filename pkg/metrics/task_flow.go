package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// TaskTransitionsTotal counts status moves: project × from_status × to_status.
	TaskTransitionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_task_transitions_total",
			Help: "Total task status transitions including done/reopen/bounce.",
		},
		[]string{"project", "from_status", "to_status"},
	)

	// TaskTimeInStatusSeconds records how long a task spent in a status before leaving.
	TaskTimeInStatusSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mesh_task_time_in_status_seconds",
			Help:    "Time a task spent in a status before transitioning (emitted on transition).",
			Buckets: []float64{60, 300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 259200, 604800},
		},
		[]string{"project", "status"},
	)

	// TasksShippedTotal counts is_shipped flag flips per project.
	TasksShippedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_tasks_shipped_total",
			Help: "Total tasks marked as shipped (is_shipped=true flip).",
		},
		[]string{"project"},
	)

	// TriageDwellSeconds records how long a task spent in triage before auto-exit.
	TriageDwellSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "mesh_triage_dwell_seconds",
			Help:    "Time from entering triage to auto-exit (human comment after blocking marker).",
			Buckets: []float64{60, 300, 1800, 7200, 28800, 86400, 259200},
		},
		[]string{"project"},
	)

	// CapacityLeaseReleasesTotal counts checkout TTL expiry sweeps per project.
	CapacityLeaseReleasesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_capacity_lease_releases_total",
			Help: "Total checkout lease releases by the TTL reaper.",
		},
		[]string{"project"},
	)

	// DoDGateTotal counts DoD gate evaluations: pass or fail per gate type.
	DoDGateTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mesh_dod_gate_total",
			Help: "Total DoD gate evaluations (pass/fail) per gate type.",
		},
		[]string{"project", "gate", "result"},
	)
)

// RecordTaskTransition records a status move and emits time-in-status if known.
func RecordTaskTransition(project, fromStatus, toStatus string, timeInStatus *time.Duration) {
	TaskTransitionsTotal.WithLabelValues(project, fromStatus, toStatus).Inc()
	if timeInStatus != nil {
		TaskTimeInStatusSeconds.WithLabelValues(project, fromStatus).Observe(timeInStatus.Seconds())
	}
}

// RecordTaskShipped increments the shipped counter for a project.
func RecordTaskShipped(project string) {
	TasksShippedTotal.WithLabelValues(project).Inc()
}

// RecordTriageDwell records the dwell time for a triage→in_progress transition.
func RecordTriageDwell(project string, d time.Duration) {
	TriageDwellSeconds.WithLabelValues(project).Observe(d.Seconds())
}

// RecordLeaseRelease records one checkout lease release for a project.
func RecordLeaseRelease(project string) {
	CapacityLeaseReleasesTotal.WithLabelValues(project).Inc()
}

// RecordDoDGate records a DoD gate pass or fail.
func RecordDoDGate(project, gate, result string) {
	DoDGateTotal.WithLabelValues(project, gate, result).Inc()
}
