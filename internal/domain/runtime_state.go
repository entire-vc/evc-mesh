package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RuntimeKind is what is physically happening with a card's work right now. It is
// orthogonal to the Mesh status: a todo card can be `working` (a lane holds a live
// checkout) and an in_progress card can be `waiting` on a pipeline.
type RuntimeKind string

const (
	RuntimeKindWorking RuntimeKind = "working"
	RuntimeKindWaiting RuntimeKind = "waiting"
	RuntimeKindQueued  RuntimeKind = "queued"
	RuntimeKindStopped RuntimeKind = "stopped"
)

// RuntimeStateSource says where an effective RuntimeState came from.
type RuntimeStateSource string

const (
	// RuntimeStateSourceExplicit: written through PUT /tasks/:id/runtime-state.
	RuntimeStateSourceExplicit RuntimeStateSource = "explicit"
	// RuntimeStateSourceCheckout: nothing was written, derived from a live checkout.
	RuntimeStateSourceCheckout RuntimeStateSource = "checkout"
)

// RuntimeStateStaleAfter is how long an explicit state may go without a refreshing
// PUT before readers are told it is stale. A flag is raised, the value is not
// dropped: a stale `working` is still the best evidence of who had the card.
const RuntimeStateStaleAfter = 15 * time.Minute

// ValidRuntimeKind reports whether k is one of the four known kinds.
func ValidRuntimeKind(k RuntimeKind) bool {
	switch k {
	case RuntimeKindWorking, RuntimeKindWaiting, RuntimeKindQueued, RuntimeKindStopped:
		return true
	}
	return false
}

// RuntimeReasonCode is the closed list of machine-readable reasons a card is held,
// waiting or stopped. The web turns a code (plus ReasonParams) into text in the
// viewer's language; the free-text Reason stays as an optional English note and is
// the fallback for records written without a code.
type RuntimeReasonCode string

const (
	ReasonReviewRefeedHold    RuntimeReasonCode = "review_refeed_hold"    // reviewed already; waits for a new commit or comment
	ReasonHumanGate           RuntimeReasonCode = "human_gate"            // waits for a human answer
	ReasonCheckedOutElsewhere RuntimeReasonCode = "checked_out_elsewhere" // another lane holds the checkout
	ReasonRecentlyDone        RuntimeReasonCode = "recently_done"         // closed recently; not fed again
	ReasonOutsideLaneScope    RuntimeReasonCode = "outside_lane_scope"    // not in the lane's selection
	ReasonRescueExhausted     RuntimeReasonCode = "rescue_exhausted"      // lane gave up on the card; the lead decides
	ReasonTurnQuarantined     RuntimeReasonCode = "turn_quarantined"      // lane turn quarantined; manual check needed
	ReasonGuardRefused        RuntimeReasonCode = "guard_refused"         // lane refused to take it; paused
	ReasonCompletionSignal    RuntimeReasonCode = "completion_signal"     // finished; waits for a human to close
	ReasonSupervisedTodo      RuntimeReasonCode = "supervised_todo"       // supervised; waits for a human to close
	ReasonHumanDecisionGate   RuntimeReasonCode = "human_decision_gate"   // pavel-gated: waits for a human decision
	ReasonUmbrella            RuntimeReasonCode = "umbrella"              // epic: waits for child cards
	ReasonTurnPending         RuntimeReasonCode = "turn_pending"          // lane turn not closed yet
	ReasonReviewCooldown      RuntimeReasonCode = "review_cooldown"       // just sent to review; params: minutes
	ReasonPause               RuntimeReasonCode = "pause"                 // operator pause
	ReasonPark                RuntimeReasonCode = "park"                  // explicitly parked
)

var validReasonCodes = map[RuntimeReasonCode]struct{}{
	ReasonReviewRefeedHold: {}, ReasonHumanGate: {}, ReasonCheckedOutElsewhere: {}, ReasonRecentlyDone: {},
	ReasonOutsideLaneScope: {}, ReasonRescueExhausted: {}, ReasonTurnQuarantined: {}, ReasonGuardRefused: {},
	ReasonCompletionSignal: {}, ReasonSupervisedTodo: {}, ReasonHumanDecisionGate: {},
	ReasonUmbrella: {}, ReasonTurnPending: {}, ReasonReviewCooldown: {},
	ReasonPause: {}, ReasonPark: {},
}

// ValidRuntimeReasonCode reports whether c is in the closed list.
func ValidRuntimeReasonCode(c RuntimeReasonCode) bool {
	_, ok := validReasonCodes[c]
	return ok
}

const (
	maxReasonParams     = 8
	maxReasonParamValue = 100
)

// RuntimeState is the value of tasks.runtime_state plus two read-time fields
// (Source, Stale) that are never persisted.
type RuntimeState struct {
	Kind   RuntimeKind `json:"kind"`
	Since  time.Time   `json:"since"`
	Until  *time.Time  `json:"until,omitempty"`
	Ref    *string     `json:"ref,omitempty"`
	Lane   *string     `json:"lane,omitempty"`
	Reason *string     `json:"reason,omitempty"`
	// ReasonCode is a value of the closed RuntimeReasonCode list; ReasonParams fill
	// its placeholders (e.g. {"minutes":"4"}). Both optional: old records have neither.
	ReasonCode   *string           `json:"reason_code,omitempty"`
	ReasonParams map[string]string `json:"reason_params,omitempty"`
	// UpdatedAt is set by the server on every write; staleness is measured from it.
	UpdatedAt time.Time `json:"updated_at"`

	// Computed on read, stripped before storing.
	Source RuntimeStateSource `json:"source,omitempty"`
	Stale  bool               `json:"stale"`
}

// RuntimeStateValidationError names the offending field so a 422 can say what to fix.
type RuntimeStateValidationError struct {
	Field   string
	Message string
}

func (e *RuntimeStateValidationError) Error() string { return e.Field + " " + e.Message }

// Normalize validates a caller-supplied state and stamps the server-owned fields.
// A zero Since defaults to now; Until must not precede Since.
func (s *RuntimeState) Normalize(now time.Time) error {
	if !ValidRuntimeKind(s.Kind) {
		return &RuntimeStateValidationError{Field: "kind", Message: "must be one of working, waiting, queued, stopped"}
	}
	if s.Since.IsZero() {
		s.Since = now
	}
	if s.Until != nil && s.Until.Before(s.Since) {
		return &RuntimeStateValidationError{Field: "until", Message: "must not be earlier than since"}
	}
	for _, f := range []struct {
		name string
		v    **string
	}{{"ref", &s.Ref}, {"lane", &s.Lane}, {"reason", &s.Reason}} {
		if *f.v == nil {
			continue
		}
		trimmed := strings.TrimSpace(**f.v)
		if trimmed == "" {
			*f.v = nil
			continue
		}
		if len(trimmed) > 500 {
			return &RuntimeStateValidationError{Field: f.name, Message: fmt.Sprintf("must be at most 500 characters, got %d", len(trimmed))}
		}
		*f.v = &trimmed
	}
	if s.ReasonCode != nil {
		code := strings.TrimSpace(*s.ReasonCode)
		switch {
		case code == "":
			s.ReasonCode = nil
		case !ValidRuntimeReasonCode(RuntimeReasonCode(code)):
			return &RuntimeStateValidationError{Field: "reason_code", Message: "is not a known reason code"}
		default:
			s.ReasonCode = &code
		}
	}
	if s.ReasonParams != nil && s.ReasonCode == nil {
		return &RuntimeStateValidationError{Field: "reason_params", Message: "require reason_code"}
	}
	if len(s.ReasonParams) > maxReasonParams {
		return &RuntimeStateValidationError{Field: "reason_params", Message: fmt.Sprintf("must have at most %d entries, got %d", maxReasonParams, len(s.ReasonParams))}
	}
	for k, v := range s.ReasonParams {
		if k == "" || len(k) > 32 || len(v) > maxReasonParamValue {
			return &RuntimeStateValidationError{Field: "reason_params", Message: fmt.Sprintf("keys must be 1-32 chars and values at most %d chars", maxReasonParamValue)}
		}
	}
	if len(s.ReasonParams) == 0 {
		s.ReasonParams = nil
	}
	s.UpdatedAt = now
	s.Source = ""
	s.Stale = false
	return nil
}

// Marshal returns the JSON persisted in tasks.runtime_state (read-time fields dropped).
func (s *RuntimeState) Marshal() ([]byte, error) {
	stored := *s
	stored.Source = ""
	stored.Stale = false
	return json.Marshal(struct {
		Kind         RuntimeKind       `json:"kind"`
		Since        time.Time         `json:"since"`
		Until        *time.Time        `json:"until,omitempty"`
		Ref          *string           `json:"ref,omitempty"`
		Lane         *string           `json:"lane,omitempty"`
		Reason       *string           `json:"reason,omitempty"`
		ReasonCode   *string           `json:"reason_code,omitempty"`
		ReasonParams map[string]string `json:"reason_params,omitempty"`
		UpdatedAt    time.Time         `json:"updated_at"`
	}{stored.Kind, stored.Since, stored.Until, stored.Ref, stored.Lane, stored.Reason, stored.ReasonCode, stored.ReasonParams, stored.UpdatedAt})
}

// ResolveRuntimeState returns the effective state of a card at `now`.
//
//   - A stored value wins; it is flagged Stale when its `until` has passed or it has
//     not been refreshed for RuntimeStateStaleAfter.
//   - With nothing stored, a live checkout (held, and not past checkout_expires) reads
//     as `working` since checkout_acquired_at, lane = the holder. This also covers
//     todo cards, whose status does not say "in progress" yet.
//   - Otherwise nil.
//
// raw is the tasks.runtime_state column (nil/empty = NULL). An unreadable stored
// value is treated as absent rather than failing the whole task read.
func ResolveRuntimeState(raw []byte, t *Task, now time.Time) *RuntimeState {
	if len(raw) > 0 && string(raw) != "null" {
		var s RuntimeState
		if err := json.Unmarshal(raw, &s); err == nil && ValidRuntimeKind(s.Kind) {
			s.Source = RuntimeStateSourceExplicit
			s.Stale = now.Sub(s.UpdatedAt) > RuntimeStateStaleAfter || (s.Until != nil && now.After(*s.Until))
			return &s
		}
	}
	if t.CheckedOutBy == nil || (t.CheckoutExpires != nil && !t.CheckoutExpires.After(now)) {
		return nil
	}
	since := now
	if t.CheckoutAcquiredAt != nil {
		since = *t.CheckoutAcquiredAt
	}
	lane := t.CheckedOutBy.String()
	return &RuntimeState{
		Kind:      RuntimeKindWorking,
		Since:     since,
		Lane:      &lane,
		UpdatedAt: since,
		Source:    RuntimeStateSourceCheckout,
	}
}
