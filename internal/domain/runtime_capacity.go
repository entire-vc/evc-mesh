package domain

import (
	"time"

	"github.com/google/uuid"
)

// Capacity reasons. Ready capacity is derived from durable state only; anything
// whose liveness is unknown is counted as occupied, never as free.
const (
	RuntimeCapacityAvailable   = "available"
	RuntimeCapacityAtCapacity  = "at_capacity"
	RuntimeCapacityCapUnset    = "identity_cap_unset"
	RuntimeCapacitySourceStore = "durable_state"
)

// RuntimeCapacity is the read-only execution-state projection of a workspace's
// agent identities (GET /workspaces/:ws_id/runtime/capacity). It authorizes
// nothing: only a reservation acquire admits work.
type RuntimeCapacity struct {
	WorkspaceID uuid.UUID              `json:"workspace_id"`
	ObservedAt  time.Time              `json:"observed_at"`
	Source      string                 `json:"source"`
	Agents      []RuntimeAgentCapacity `json:"agents"`
}

// RuntimeAgentCapacity: the capacity fields (configured … ready) are global to
// the agent identity across every workspace, like the reservation cap. Tasks
// counts are scoped to the requested workspace and carry no task identifiers.
type RuntimeAgentCapacity struct {
	AgentID    uuid.UUID `json:"agent_id"`
	Name       string    `json:"name"`
	Configured int       `json:"configured"`
	Effective  int       `json:"effective"`
	// Reserved: unconsumed, unexpired reservations.
	Reserved int `json:"reserved"`
	// Running: consumed reservations inside their run lease.
	Running int `json:"running"`
	// Reconcile: consumed reservations past their run lease (unknown, occupied).
	Reconcile int `json:"reconcile"`
	// Writers: live task checkouts without an active reservation (direct mode or
	// a controller between checkout and acquire).
	Writers int `json:"writers"`
	// StaleWriters: checkouts still held past their lease expiry (unknown, occupied).
	StaleWriters int `json:"stale_writers"`
	// Unknown = Reconcile + StaleWriters, already included in Occupied.
	Unknown  int                `json:"unknown"`
	Occupied int                `json:"occupied"`
	Ready    int                `json:"ready"`
	Reason   string             `json:"reason"`
	Tasks    RuntimeTaskCapView `json:"tasks"`
}

// RuntimeTaskCapView: the agent's open assigned tasks in this workspace that
// hold no checkout and no active reservation, split into ready and waiting.
// Waiting tasks consume no capacity; a task may wait for several reasons, so
// the per-reason counts can sum to more than Waiting.
type RuntimeTaskCapView struct {
	Ready     int                    `json:"ready"`
	Waiting   int                    `json:"waiting"`
	WaitingBy RuntimeTaskWaitReasons `json:"waiting_by"`
}

type RuntimeTaskWaitReasons struct {
	HumanGate    int `json:"human_gate"`
	Triage       int `json:"triage"`
	ParkedWait   int `json:"parked_wait"`
	Dependencies int `json:"dependencies"`
	StartAfter   int `json:"start_after"`
}

// Derive fills Effective, Unknown, Occupied, Ready and Reason from the counts.
func (c *RuntimeAgentCapacity) Derive() {
	c.Unknown = c.Reconcile + c.StaleWriters
	c.Occupied = c.Reserved + c.Running + c.Reconcile + c.Writers + c.StaleWriters
	c.Effective = max(c.Configured, 0)
	switch {
	case c.Effective == 0:
		c.Ready, c.Reason = 0, RuntimeCapacityCapUnset
	case c.Occupied >= c.Effective:
		c.Ready, c.Reason = 0, RuntimeCapacityAtCapacity
	default:
		c.Ready, c.Reason = c.Effective-c.Occupied, RuntimeCapacityAvailable
	}
}
