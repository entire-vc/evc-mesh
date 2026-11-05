package domain

import (
	"fmt"
	"strings"
	"time"
)

// ConstraintClass is the closed list of reasons a card stopped that the
// operational-constraints contract (additive v1) names. A reader that meets a
// class it does not know must fall back to the free-text reason.
type ConstraintClass string

const (
	ConstraintMeshDependency  ConstraintClass = "mesh_dependency"
	ConstraintLocalWait       ConstraintClass = "local_wait"
	ConstraintCICapacity      ConstraintClass = "ci_capacity"
	ConstraintReviewRejection ConstraintClass = "review_rejection"
	ConstraintExpiredAuth     ConstraintClass = "expired_authority"
	ConstraintWriteACL        ConstraintClass = "write_acl"
)

// ValidConstraintClass reports whether c is one of the six known classes.
func ValidConstraintClass(c ConstraintClass) bool {
	switch c {
	case ConstraintMeshDependency, ConstraintLocalWait, ConstraintCICapacity,
		ConstraintReviewRejection, ConstraintExpiredAuth, ConstraintWriteACL:
		return true
	}
	return false
}

// Actor kinds of ConstraintActor.Kind.
const (
	ConstraintActorAgent   = "agent"
	ConstraintActorHuman   = "human"
	ConstraintActorUnknown = "unknown"
)

// CI states of ConstraintCI.State. They are three different situations and are
// never merged: capacity (provider queue is full), manual_required (a person
// must start the job) and unavailable (the provider cannot be read).
const (
	ConstraintCICapacityState = "capacity"
	ConstraintCIManual        = "manual_required"
	ConstraintCIUnavailable   = "unavailable"
)

// ConstraintPredicateUnknown is the literal wake predicate for an event the
// producer cannot name.
const ConstraintPredicateUnknown = "unknown"

const (
	maxConstraints          = 16
	maxConstraintID         = 128
	maxConstraintCode       = 64
	maxConstraintText       = 500
	maxConstraintRef        = 300
	maxConstraintJobs       = 16
	constraintFutureSkew    = 5 * time.Minute
	reasonCodeDependencyACL = "dependency_not_visible"
	reasonCodeDependency    = "dependency_unreleased"
)

// ConstraintActor is whose action lifts the constraint. It is not the assignee
// or the current lane by default; without evidence it is kind=unknown.
type ConstraintActor struct {
	Kind string  `json:"kind"`
	ID   *string `json:"id,omitempty"`
	Name *string `json:"name,omitempty"`
}

// ConstraintWakeTarget copies the existing typed-wait semantics; it is not a
// new watcher.
type ConstraintWakeTarget struct {
	TargetCondition *string  `json:"target_condition,omitempty"`
	Provider        *string  `json:"provider,omitempty"`
	ProjectPath     *string  `json:"project_path,omitempty"`
	MRIID           *int64   `json:"mr_iid,omitempty"`
	PipelineID      *int64   `json:"pipeline_id,omitempty"`
	RequiredJobs    []string `json:"required_jobs,omitempty"`
	Until           *string  `json:"until,omitempty"`
	UntilAt         *string  `json:"until_at,omitempty"`
}

// ConstraintWake is the exact existing predicate that lifts the constraint, or
// the literal "unknown".
type ConstraintWake struct {
	Predicate string                `json:"predicate"`
	Ref       *string               `json:"ref,omitempty"`
	Target    *ConstraintWakeTarget `json:"target,omitempty"`
}

// ConstraintSource says who observed the constraint.
type ConstraintSource struct {
	Kind string  `json:"kind"`
	Ref  *string `json:"ref,omitempty"`
}

// ConstraintCI is set only for class ci_capacity. Scope and Position come from
// provider evidence only; a lane queue position is never copied here.
type ConstraintCI struct {
	State    string  `json:"state"`
	Scope    *string `json:"scope,omitempty"`
	Position *int    `json:"position,omitempty"`
	Reason   *string `json:"reason,omitempty"`
	Provider *string `json:"provider,omitempty"`
}

// OperationalConstraint is one active reason the card is stopped.
type OperationalConstraint struct {
	ID           string            `json:"id"`
	Class        ConstraintClass   `json:"class"`
	ReasonCode   string            `json:"reason_code"`
	ReasonParams map[string]string `json:"reason_params,omitempty"`
	Reason       *string           `json:"reason,omitempty"`
	NextActor    ConstraintActor   `json:"next_actor"`
	Wake         ConstraintWake    `json:"wake"`
	Source       ConstraintSource  `json:"source"`
	ObservedAt   time.Time         `json:"observed_at"`
	// Stale is computed on read per observation and never persisted.
	Stale bool          `json:"stale"`
	CI    *ConstraintCI `json:"ci,omitempty"`
}

func constraintErr(i int, field, msg string) error {
	return &RuntimeStateValidationError{Field: fmt.Sprintf("constraints[%d].%s", i, field), Message: msg}
}

func trimBounded(s string, limit int) (string, bool) {
	t := strings.TrimSpace(s)
	return t, len(t) <= limit
}

func trimPtr(p **string, limit int) bool {
	if *p == nil {
		return true
	}
	t := strings.TrimSpace(**p)
	if t == "" {
		*p = nil
		return true
	}
	if len(t) > limit {
		return false
	}
	*p = &t
	return true
}

// normalizeConstraints validates a caller-supplied list in place with the same
// bounded approach as the rest of RuntimeState: a bad value is a 422 naming the
// field, never a silent drop.
func normalizeConstraints(cs []OperationalConstraint, now time.Time) error {
	if len(cs) > maxConstraints {
		return &RuntimeStateValidationError{Field: "constraints", Message: fmt.Sprintf("must have at most %d entries, got %d", maxConstraints, len(cs))}
	}
	seen := make(map[string]struct{}, len(cs))
	for i := range cs {
		c := &cs[i]
		var ok bool
		if c.ID, ok = trimBounded(c.ID, maxConstraintID); !ok || c.ID == "" {
			return constraintErr(i, "id", fmt.Sprintf("is required, at most %d characters", maxConstraintID))
		}
		if _, dup := seen[c.ID]; dup {
			return constraintErr(i, "id", "must be unique")
		}
		seen[c.ID] = struct{}{}
		if !ValidConstraintClass(c.Class) {
			return constraintErr(i, "class", "must be one of mesh_dependency, local_wait, ci_capacity, review_rejection, expired_authority, write_acl")
		}
		if c.ReasonCode, ok = trimBounded(c.ReasonCode, maxConstraintCode); !ok || c.ReasonCode == "" {
			return constraintErr(i, "reason_code", fmt.Sprintf("is required, at most %d characters", maxConstraintCode))
		}
		if !trimPtr(&c.Reason, maxConstraintText) {
			return constraintErr(i, "reason", fmt.Sprintf("must be at most %d characters", maxConstraintText))
		}
		if len(c.ReasonParams) > maxReasonParams {
			return constraintErr(i, "reason_params", fmt.Sprintf("must have at most %d entries", maxReasonParams))
		}
		for k, v := range c.ReasonParams {
			if k == "" || len(k) > 32 || len(v) > maxReasonParamValue {
				return constraintErr(i, "reason_params", fmt.Sprintf("keys must be 1-32 chars and values at most %d chars", maxReasonParamValue))
			}
		}
		if len(c.ReasonParams) == 0 {
			c.ReasonParams = nil
		}
		switch c.NextActor.Kind {
		case "":
			c.NextActor.Kind = ConstraintActorUnknown
		case ConstraintActorAgent, ConstraintActorHuman, ConstraintActorUnknown:
		default:
			return constraintErr(i, "next_actor.kind", "must be one of agent, human, unknown")
		}
		if !trimPtr(&c.NextActor.ID, maxConstraintRef) || !trimPtr(&c.NextActor.Name, maxConstraintRef) {
			return constraintErr(i, "next_actor", "id and name must be at most 300 characters")
		}
		if c.Wake.Predicate, ok = trimBounded(c.Wake.Predicate, maxConstraintRef); !ok || c.Wake.Predicate == "" {
			return constraintErr(i, "wake.predicate", "is required (use \"unknown\" when the event is not known)")
		}
		if !trimPtr(&c.Wake.Ref, maxConstraintRef) {
			return constraintErr(i, "wake.ref", "must be at most 300 characters")
		}
		if t := c.Wake.Target; t != nil {
			if len(t.RequiredJobs) > maxConstraintJobs {
				return constraintErr(i, "wake.target.required_jobs", fmt.Sprintf("must have at most %d entries", maxConstraintJobs))
			}
			for _, p := range []*string{t.TargetCondition, t.Provider, t.ProjectPath, t.Until, t.UntilAt} {
				if p != nil && len(*p) > maxConstraintRef {
					return constraintErr(i, "wake.target", "values must be at most 300 characters")
				}
			}
			for _, j := range t.RequiredJobs {
				if j == "" || len(j) > 100 {
					return constraintErr(i, "wake.target.required_jobs", "names must be 1-100 characters")
				}
			}
			if t.UntilAt != nil {
				if _, err := time.Parse(time.RFC3339, *t.UntilAt); err != nil {
					return constraintErr(i, "wake.target.until_at", "must be RFC3339")
				}
			}
		}
		if c.Source.Kind, ok = trimBounded(c.Source.Kind, maxConstraintCode); !ok || c.Source.Kind == "" {
			return constraintErr(i, "source.kind", "is required")
		}
		if !trimPtr(&c.Source.Ref, maxConstraintRef) {
			return constraintErr(i, "source.ref", "must be at most 300 characters")
		}
		// An unknown observation time is never promoted to a fresh one.
		if c.ObservedAt.IsZero() {
			return constraintErr(i, "observed_at", "is required")
		}
		if c.ObservedAt.After(now.Add(constraintFutureSkew)) {
			return constraintErr(i, "observed_at", "must not be in the future")
		}
		c.Stale = false
		if err := normalizeConstraintCI(i, c); err != nil {
			return err
		}
	}
	return nil
}

func normalizeConstraintCI(i int, c *OperationalConstraint) error {
	if c.CI == nil {
		return nil
	}
	if c.Class != ConstraintCICapacity {
		return constraintErr(i, "ci", "is only allowed for class ci_capacity")
	}
	ci := c.CI
	switch ci.State {
	case ConstraintCICapacityState, ConstraintCIManual, ConstraintCIUnavailable:
	default:
		return constraintErr(i, "ci.state", "must be one of capacity, manual_required, unavailable")
	}
	if !trimPtr(&ci.Scope, maxConstraintRef) || !trimPtr(&ci.Reason, maxConstraintText) || !trimPtr(&ci.Provider, maxConstraintRef) {
		return constraintErr(i, "ci", "values are too long")
	}
	if ci.Position != nil && *ci.Position < 0 {
		return constraintErr(i, "ci.position", "must not be negative")
	}
	// scope/position are provider evidence: without a provider, or when the
	// provider could not be read, they are not evidence and must be absent.
	if (ci.Scope != nil || ci.Position != nil) && (ci.Provider == nil || ci.State == ConstraintCIUnavailable) {
		return constraintErr(i, "ci", "scope and position need a provider and are not allowed while state is unavailable")
	}
	return nil
}

// constraintStale decides per observation, never from the state's updated_at.
// A local wait is refreshed by nobody, so only its until_at ends it; every other
// class goes stale after RuntimeStateStaleAfter.
func constraintStale(c OperationalConstraint, now time.Time) bool {
	if c.Wake.Target != nil && c.Wake.Target.UntilAt != nil {
		if at, err := time.Parse(time.RFC3339, *c.Wake.Target.UntilAt); err == nil && now.After(at) {
			return true
		}
	}
	if c.ObservedAt.IsZero() {
		return true
	}
	if c.Class == ConstraintLocalWait {
		return false
	}
	return now.Sub(c.ObservedAt) > RuntimeStateStaleAfter
}

// MarkConstraintsStale sets Stale on every constraint from its own observation.
func MarkConstraintsStale(cs []OperationalConstraint, now time.Time) {
	for i := range cs {
		cs[i].Stale = constraintStale(cs[i], now)
	}
}

// DependencyEdgeView is one blocks edge as the read side sees it. Visible says
// whether the caller may open the blocker; a closed blocker keeps its place as an
// explicit constraint but contributes no ref, title or owner.
type DependencyEdgeView struct {
	Edge    TaskDependency
	Visible bool
}

// DependencyConstraints derives mesh_dependency constraints from live edges.
// Released blocks edges and every non-blocks edge (relates_to, is_child_of) are
// not blockers and produce nothing. The server reads the edge itself, so the
// observation time is now.
func DependencyConstraints(edges []DependencyEdgeView, now time.Time) []OperationalConstraint {
	var out []OperationalConstraint
	for _, e := range edges {
		d := e.Edge
		if d.DependencyType != DependencyTypeBlocks || d.ConditionSatisfied {
			continue
		}
		edgeRef := d.ID.String()
		c := OperationalConstraint{
			ID:         "dep:" + edgeRef,
			Class:      ConstraintMeshDependency,
			ReasonCode: reasonCodeDependency,
			NextActor:  ConstraintActor{Kind: ConstraintActorUnknown},
			Source:     ConstraintSource{Kind: "task_dependency", Ref: &edgeRef},
			ObservedAt: now,
		}
		if e.Visible {
			ref := "card:" + d.DependsOnTaskID.String()
			cond := string(d.Condition)
			c.Wake = ConstraintWake{Predicate: "card", Ref: &ref, Target: &ConstraintWakeTarget{TargetCondition: &cond}}
			c.ReasonParams = map[string]string{"task_id": d.DependsOnTaskID.String(), "condition": cond}
		} else {
			c.ReasonCode = reasonCodeDependencyACL
			c.Wake = ConstraintWake{Predicate: ConstraintPredicateUnknown}
		}
		out = append(out, c)
	}
	return out
}

// MergeConstraints returns the union of stored (producer) and derived (server)
// constraints, de-duplicated by id and not by class, so two different constraints
// of one class both survive. On an id clash the server-derived one wins. Order is
// stable: derived first, then stored in their written order.
func MergeConstraints(stored, derived []OperationalConstraint) []OperationalConstraint {
	if len(stored) == 0 && len(derived) == 0 {
		return stored
	}
	out := make([]OperationalConstraint, 0, len(stored)+len(derived))
	seen := make(map[string]struct{}, len(stored)+len(derived))
	for _, c := range derived {
		if _, dup := seen[c.ID]; !dup {
			seen[c.ID] = struct{}{}
			out = append(out, c)
		}
	}
	for _, c := range stored {
		if _, dup := seen[c.ID]; !dup {
			seen[c.ID] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

// DependencyOnlyRuntimeState is what a card with live blockers shows when no
// producer wrote a state: a waiting state whose only content is the server's own
// evidence. It is flagged Source=dependency so a reader can tell it from a
// written one, and it is never stored.
func DependencyOnlyRuntimeState(derived []OperationalConstraint, edges []DependencyEdgeView, now time.Time) *RuntimeState {
	if len(derived) == 0 {
		return nil
	}
	since := now
	for _, e := range edges {
		if !e.Edge.CreatedAt.IsZero() && e.Edge.CreatedAt.Before(since) {
			since = e.Edge.CreatedAt
		}
	}
	return &RuntimeState{
		Kind: RuntimeKindWaiting, Since: since, UpdatedAt: now,
		Constraints: derived, Source: RuntimeStateSourceDependency,
	}
}
