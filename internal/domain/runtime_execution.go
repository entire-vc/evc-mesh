package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Execution observation protocol v1 (docs/api/agent-executions.md). It is
// read-only telemetry next to the runtime catalog: nothing in it admits,
// launches, leases or routes.
const (
	ExecutionSchemaVersion = 1
	// ExecutionMaxAttempts bounds one report batch.
	ExecutionMaxAttempts = 200
	// ExecutionFreshnessSeconds is the telemetry freshness window; the read
	// projection publishes it as max_age_seconds.
	ExecutionFreshnessSeconds = 120
	// ExecutionPublisherIntervalSeconds is the publisher refresh interval.
	ExecutionPublisherIntervalSeconds = 30
)

const (
	ExecutionStateStarting  = "starting"
	ExecutionStateRunning   = "running"
	ExecutionStateWaiting   = "waiting"
	ExecutionStateStopped   = "stopped"
	ExecutionStateCompleted = "completed"
	ExecutionStateFailed    = "failed"
)

const (
	FreshnessUnknown = "unknown"
	FreshnessCurrent = "current"
	FreshnessStale   = "stale"
	FreshnessOffline = "offline"
	// FreshnessHistory marks a stopped/completed/failed attempt: not running,
	// whatever its age.
	FreshnessHistory = "history"
)

var (
	executionStates = []string{ExecutionStateStarting, ExecutionStateRunning, ExecutionStateWaiting, ExecutionStateStopped, ExecutionStateCompleted, ExecutionStateFailed}
	// Opaque ids and observed values: one token, no path separators, spaces or
	// quoting, so an environment value, path or address cannot ride along.
	executionTokenPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:+-]{0,99}$`)
)

// ValidExecutionToken reports whether value is an acceptable opaque id or
// observed harness/model/effort/source token.
func ValidExecutionToken(value string) bool { return executionTokenPattern.MatchString(value) }

// ExecutionTerminal reports whether state ends an attempt.
func ExecutionTerminal(state string) bool {
	return state == ExecutionStateStopped || state == ExecutionStateCompleted || state == ExecutionStateFailed
}

type ExecutionInstanceRequest struct {
	SchemaVersion int    `json:"schema_version"`
	InstanceID    string `json:"instance_id"`
}

type ExecutionInstance struct {
	SchemaVersion int       `json:"schema_version"`
	InstanceID    string    `json:"instance_id"`
	Generation    int64     `json:"generation"`
	ReceivedAt    time.Time `json:"received_at"`
}

// ExecutionResolvedRoute is what the Runner's local resolver selected for one
// attempt: immutable evidence, never a replacement for the configured policy.
type ExecutionResolvedRoute struct {
	Source     string  `json:"source"`
	Revision   int64   `json:"revision"`
	Digest     *string `json:"digest,omitempty"`
	ProfileRef *string `json:"profile_ref,omitempty"`
	Harness    *string `json:"harness,omitempty"`
	Model      *string `json:"model,omitempty"`
	Effort     *string `json:"effort,omitempty"`
}

// ExecutionObserved is the adapter's confirmation for the current turn.
// Harness, Model and Effort are independently nullable; null means unknown.
type ExecutionObserved struct {
	Harness    *string   `json:"harness"`
	Model      *string   `json:"model"`
	Effort     *string   `json:"effort"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

type ExecutionFieldSource struct {
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

// ExecutionProvenance overrides Source/ObservedAt for single fields whose
// source or time differs from the attempt-level observation.
type ExecutionProvenance struct {
	Harness *ExecutionFieldSource `json:"harness,omitempty"`
	Model   *ExecutionFieldSource `json:"model,omitempty"`
	Effort  *ExecutionFieldSource `json:"effort,omitempty"`
}

type ExecutionAttemptReport struct {
	WorkspaceID   uuid.UUID              `json:"workspace_id"`
	ProjectID     uuid.UUID              `json:"project_id"`
	AgentID       uuid.UUID              `json:"agent_id"`
	BindingRef    string                 `json:"binding_ref"`
	TaskID        *uuid.UUID             `json:"task_id"`
	ControllerRef string                 `json:"controller_ref"`
	InstanceID    string                 `json:"instance_id"`
	Generation    int64                  `json:"generation"`
	AttemptID     string                 `json:"attempt_id"`
	TurnID        *string                `json:"turn_id"`
	State         string                 `json:"state"`
	ResolvedRoute ExecutionResolvedRoute `json:"resolved_route"`
	Observed      ExecutionObserved      `json:"observed"`
	Provenance    *ExecutionProvenance   `json:"provenance,omitempty"`
}

type ExecutionReport struct {
	SchemaVersion int                      `json:"schema_version"`
	InstanceID    string                   `json:"instance_id"`
	Generation    int64                    `json:"generation"`
	Sequence      int64                    `json:"sequence"`
	ReportedAt    time.Time                `json:"reported_at"`
	Attempts      []ExecutionAttemptReport `json:"attempts"`
}

// BodyHash identifies the exact content of a report, so a retry of the same
// instance+sequence is idempotent and a changed replay is a conflict.
func (r ExecutionReport) BodyHash() string {
	data, _ := json.Marshal(r)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validOptionalToken(v *string) bool { return v == nil || ValidExecutionToken(*v) }

func (s *ExecutionFieldSource) validate(now time.Time) bool {
	return s == nil || (ValidExecutionToken(s.Source) && !s.ObservedAt.IsZero() && !s.ObservedAt.After(now))
}

// Validate checks the envelope and every attempt in isolation; no partial
// acceptance. Ownership (binding, project, grant) is checked by the repository.
func (r ExecutionReport) Validate(now time.Time, controllerRef string) error {
	if r.SchemaVersion != ExecutionSchemaVersion || !ValidExecutionToken(r.InstanceID) || r.Generation < 1 || r.Sequence < 1 {
		return apierror.BadRequest("invalid execution report")
	}
	if r.ReportedAt.IsZero() || r.ReportedAt.After(now) {
		return apierror.BadRequest("reported_at must be set and not in the future")
	}
	if len(r.Attempts) == 0 || len(r.Attempts) > ExecutionMaxAttempts {
		return apierror.BadRequest("invalid execution report")
	}
	type key struct {
		project, agent uuid.UUID
		attempt        string
	}
	seen := map[key]bool{}
	for _, a := range r.Attempts {
		if err := a.validate(now, controllerRef, r.InstanceID, r.Generation); err != nil {
			return err
		}
		k := key{a.ProjectID, a.AgentID, a.AttemptID}
		if seen[k] {
			return apierror.BadRequest("duplicate attempt in report")
		}
		seen[k] = true
	}
	return nil
}

func (a ExecutionAttemptReport) validate(now time.Time, controllerRef, instanceID string, generation int64) error {
	bad := apierror.BadRequest("invalid execution attempt")
	if a.WorkspaceID == uuid.Nil || a.ProjectID == uuid.Nil || a.AgentID == uuid.Nil || (a.TaskID != nil && *a.TaskID == uuid.Nil) {
		return bad
	}
	if a.ControllerRef != controllerRef || a.InstanceID != instanceID || a.Generation != generation {
		return apierror.BadRequest("attempt controller, instance and generation must match the envelope")
	}
	if !ValidRuntimeRef(a.BindingRef) || !ValidExecutionToken(a.AttemptID) || !validOptionalToken(a.TurnID) {
		return bad
	}
	if !slices.Contains(executionStates, a.State) {
		return bad
	}
	rr := a.ResolvedRoute
	if !ValidExecutionToken(rr.Source) || rr.Revision < 0 || !validOptionalToken(rr.Digest) || !validOptionalToken(rr.ProfileRef) ||
		!validOptionalToken(rr.Harness) || !validOptionalToken(rr.Model) || !validOptionalToken(rr.Effort) {
		return bad
	}
	o := a.Observed
	if !ValidExecutionToken(o.Source) || o.ObservedAt.IsZero() || o.ObservedAt.After(now) ||
		!validOptionalToken(o.Harness) || !validOptionalToken(o.Model) || !validOptionalToken(o.Effort) {
		return bad
	}
	if p := a.Provenance; p != nil && (!p.Harness.validate(now) || !p.Model.validate(now) || !p.Effort.validate(now)) {
		return bad
	}
	return nil
}

// ExecutionFreshnessInput is everything freshness depends on, so the rule is
// one pure function shared by the read path and the tests.
type ExecutionFreshnessInput struct {
	State string
	// ModelKnown reports a non-null observed model for the current turn.
	ModelKnown bool
	// ObservedAt is when the model was observed. It is history, not a
	// liveness signal: a fresh heartbeat or report never renews it.
	ObservedAt        time.Time
	HeartbeatAt       time.Time
	ReportedAt        time.Time
	ReceivedAt        time.Time
	GenerationCurrent bool
	// ControllerLive is false when the controller explicitly stopped or has
	// no live grant/instance.
	ControllerLive bool
}

// ExecutionFreshness classifies one attempt at now. Age is judged by the
// server receive time AND the publisher's own reported time, so a late
// delivery cannot revive old data; a heartbeat never makes an unknown model
// known nor an old model observation current.
func ExecutionFreshness(now time.Time, in ExecutionFreshnessInput) string {
	window := time.Duration(ExecutionFreshnessSeconds) * time.Second
	switch {
	case ExecutionTerminal(in.State):
		return FreshnessHistory
	case !in.ControllerLive || now.Sub(in.HeartbeatAt) > window:
		return FreshnessOffline
	case !in.GenerationCurrent || now.Sub(in.ReportedAt) > window || now.Sub(in.ReceivedAt) > window:
		return FreshnessStale
	case in.ModelKnown && now.Sub(in.ObservedAt) > window:
		return FreshnessStale
	case !in.ModelKnown:
		return FreshnessUnknown
	}
	return FreshnessCurrent
}
