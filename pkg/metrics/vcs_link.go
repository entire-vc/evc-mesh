package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// VCSWebhookAmbiguousTaskRefTotal counts PR/MR webhook deliveries whose
// title/body/branch named several tasks and no decisive one, so no task could
// be chosen. outcome says what the delivery still managed to do:
// "status_synced" (an existing link to this exact PR/MR had its status
// refreshed) or "none" (nothing was written). A rising "none" is a lost status.
var VCSWebhookAmbiguousTaskRefTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "mesh_vcs_webhook_ambiguous_task_ref_total",
		Help: "PR/MR webhook deliveries refused as ambiguous_task_ref, by provider and outcome (status_synced|none).",
	},
	[]string{"provider", "outcome"},
)

// RecordVCSWebhookAmbiguousTaskRef increments the ambiguous_task_ref counter.
func RecordVCSWebhookAmbiguousTaskRef(provider, outcome string) {
	VCSWebhookAmbiguousTaskRefTotal.WithLabelValues(provider, outcome).Inc()
}
