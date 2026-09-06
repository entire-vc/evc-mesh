package domain

import (
	"time"

	"github.com/google/uuid"
)

// ArmHumanGateSource records HOW a gate came to be armed. It is not decoration: the two
// sources have deliberately different validation strictness, and collapsing them would
// re-create the failure this whole card exists to remove.
type ArmHumanGateSource string

const (
	// ArmHumanGateSourceAPI is an explicit set_human_gate call (MCP tool or
	// POST /tasks/:id/human-gate). Fully validated: a missing author or a missing
	// recommended_default is a 422, because the caller is a program that can be fixed.
	ArmHumanGateSourceAPI ArmHumanGateSource = "api"
	// ArmHumanGateSourceMarker is the server's own enforceBlockingTriage translating a
	// "❓ Blocking @pavel" comment into the same call, with gate_author taken from the
	// comment's authenticated author.
	//
	// A marker that names no recommended_default is armed anyway, with the field left
	// NULL and a WARNING logged. Refusing it here would be strictly worse than the bug
	// being fixed: the marker is the channel that DELIVERS a live ask, and a rejected
	// arm is silent to its author — they post the question, believe it was handed over,
	// and the card keeps being fed (the exact shape of #58a6f4ff and #f421ad57).
	// Tightening this into a refusal is task 1.4 (#060ccaae), which first has to give
	// the author a way to see the refusal.
	ArmHumanGateSourceMarker ArmHumanGateSource = "marker"
)

// ArmHumanGateInput is the ONE input shape for arming a human gate. Before this type
// existed, "the card is waiting on a human" was recomputed in 21 places from comment
// text; the point of routing every arm through a single struct is that a client's
// is_human_gated reduces to reading task.human_gate, with the rest of the answer (who
// asked, what they asked, what happens by default, by when) already on the task.
type ArmHumanGateInput struct {
	TaskID uuid.UUID
	// Author is who raised the ask — an agent id or a user id, discriminated by
	// AuthorType. Required for BOTH sources: unlike RecommendedDefault, an arm with no
	// author is never the lesser evil, because an unattributed gate cannot be withdrawn
	// by its owner and has to wait for Pavel by construction.
	Author     uuid.UUID
	AuthorType ActorType
	// Reason is what was asked, in the author's own words.
	Reason string
	// RecommendedDefault is what the author will do if nobody answers. Required for
	// ArmHumanGateSourceAPI; optional (with a warning) for ArmHumanGateSourceMarker —
	// see ArmHumanGateSourceMarker's note.
	RecommendedDefault string
	// Deadline is when RecommendedDefault applies. Nil means no deadline; the
	// default-on-timeout sweep (task 1.4) must read nil as "out of scope", never as
	// "already expired".
	Deadline *time.Time
	// Class is hard (never timed out) or soft. Empty means HumanGateClassHard —
	// fail-closed, matching the column default and SetHumanGate's release behaviour.
	Class  HumanGateClass
	Source ArmHumanGateSource
}

// ArmHumanGateValidationError names the single field that failed, so the handler can
// return a 422 that says which one rather than a generic "invalid input". Kept as a
// typed error instead of a bare string so the service layer can map it without matching
// on message text.
type ArmHumanGateValidationError struct {
	Field   string
	Message string
}

func (e *ArmHumanGateValidationError) Error() string { return e.Field + ": " + e.Message }

// Validate applies the arm-time contract. Returns *ArmHumanGateValidationError so the
// caller can turn it into a 422 naming the field.
func (in *ArmHumanGateInput) Validate() error {
	if in.TaskID == uuid.Nil {
		return &ArmHumanGateValidationError{Field: "task_id", Message: "required"}
	}
	if in.Author == uuid.Nil {
		return &ArmHumanGateValidationError{
			Field:   "gate_author",
			Message: "required — an unattributed gate cannot be withdrawn by its owner",
		}
	}
	switch in.AuthorType {
	case ActorTypeUser, ActorTypeAgent, ActorTypeSystem:
	default:
		return &ArmHumanGateValidationError{
			Field:   "gate_author_type",
			Message: "must be one of user, agent, system",
		}
	}
	if in.Source == ArmHumanGateSourceAPI && in.RecommendedDefault == "" {
		return &ArmHumanGateValidationError{
			Field: "recommended_default",
			Message: "required — a gate with no stated default cannot time out, " +
				"so it can only ever be answered by a human",
		}
	}
	return nil
}

// Normalized returns a copy with the fail-closed defaults applied. Called by the service
// after Validate, so a caller that omits Class or Source still lands on the safe value
// rather than writing an empty string into a CHECK-constrained column.
func (in ArmHumanGateInput) Normalized() ArmHumanGateInput {
	if in.Class != HumanGateClassSoft {
		in.Class = HumanGateClassHard
	}
	if in.Source == "" {
		in.Source = ArmHumanGateSourceAPI
	}
	return in
}
