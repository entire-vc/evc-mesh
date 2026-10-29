package domain

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Typed turn outcomes (spec an internal ticket §4, docs/api/typed-outcomes.md): how an
// agent's turn ended is recorded as state the runner reads, not as a marker
// line parsed out of a comment.

// ResultKind is what a closed card delivered. Mirrors the `kind:*` label.
type ResultKind string

const (
	ResultKindChange   ResultKind = "change"
	ResultKindDefect   ResultKind = "defect"
	ResultKindResearch ResultKind = "research"
	ResultKindContent  ResultKind = "content"
	ResultKindOps      ResultKind = "ops"
)

// Valid reports whether k is a known result kind.
func (k ResultKind) Valid() bool {
	switch k {
	case ResultKindChange, ResultKindDefect, ResultKindResearch, ResultKindContent, ResultKindOps:
		return true
	}
	return false
}

// TaskWaitKind names what a wait is waiting on.
type TaskWaitKind string

const (
	TaskWaitCard     TaskWaitKind = "card"
	TaskWaitMR       TaskWaitKind = "mr"
	TaskWaitPipeline TaskWaitKind = "pipeline"
	TaskWaitTime     TaskWaitKind = "time"
	TaskWaitDecision TaskWaitKind = "decision"
	TaskWaitHuman    TaskWaitKind = "human"
)

// Until states. MR waits take merged|green|changed, pipeline waits finished|green.
const (
	WaitUntilMerged   = "merged"
	WaitUntilGreen    = "green"
	WaitUntilChanged  = "changed"
	WaitUntilFinished = "finished"
)

// WaitResolution is how an active wait stopped being active.
type WaitResolution string

const (
	WaitResolutionMet        WaitResolution = "met"
	WaitResolutionSuperseded WaitResolution = "superseded"
	WaitResolutionCancelled  WaitResolution = "cancelled"
	WaitResolutionExpired    WaitResolution = "expired"
	WaitResolutionInvalid    WaitResolution = "invalid"
)

// Bounds of a time wait, measured from the moment it is declared.
const (
	MinTimeWait     = 5 * time.Minute
	MaxTimeWait     = 30 * 24 * time.Hour
	MaxWaitNoteLen  = 500
	MaxOutcomeNote  = 4096
	MaxQuestionLen  = 4000
	MaxSessionIDLen = 200
)

// Error codes of the typed-outcome commands (docs/api/typed-outcomes.md).
const (
	CodeInvalidTarget     = "invalid_target"
	CodeSelfWait          = "self_wait"
	CodeAlreadyMet        = "already_met"
	CodeTimeOutOfBounds   = "time_out_of_bounds"
	CodeNotAnAgent        = "not_an_agent"
	CodeMissingResultKind = "missing_result_kind"
	CodeMissingEvidence   = "missing_evidence"
	CodeNoActiveWait      = "no_active_wait"
)

// OutcomeError is a refusal the agent sees in the same turn: Code is one of the
// Code* constants above, Message says what to change.
type OutcomeError struct {
	Code    string
	Message string
}

func (e *OutcomeError) Error() string { return e.Code + ": " + e.Message }

func outcomeErr(code, msg string) *OutcomeError { return &OutcomeError{Code: code, Message: msg} }

// TaskWait is the one active wait of an agent on a card (spec §4.1).
type TaskWait struct {
	ID              uuid.UUID      `json:"id" db:"id"`
	TaskID          uuid.UUID      `json:"task_id" db:"task_id"`
	AgentID         uuid.UUID      `json:"agent_id" db:"agent_id"`
	SessionID       *string        `json:"session_id,omitempty" db:"session_id"`
	Kind            TaskWaitKind   `json:"kind" db:"kind"`
	TargetTaskID    *uuid.UUID     `json:"target_task_id,omitempty" db:"target_task_id"`
	TargetCondition *string        `json:"target_condition,omitempty" db:"target_condition"`
	VCSLinkID       *uuid.UUID     `json:"vcs_link_id,omitempty" db:"vcs_link_id"`
	Provider        *string        `json:"provider,omitempty" db:"provider"`
	ProjectPath     *string        `json:"project_path,omitempty" db:"project_path"`
	MRIID           *int           `json:"mr_iid,omitempty" db:"mr_iid"`
	PipelineID      *int64         `json:"pipeline_id,omitempty" db:"pipeline_id"`
	RequiredJobs    pq.StringArray `json:"required_jobs" db:"required_jobs"`
	Until           *string        `json:"until,omitempty" db:"until_state"`
	UntilAt         *time.Time     `json:"until_at,omitempty" db:"until_at"`
	// A time wait's evidence: the external event, where its deadline is
	// published, why the card must wait for it, and the offset until_at was
	// given in. EvidenceStatus is always "unverified" (see TimeEvidenceUnverified).
	EventRef          *string         `json:"event_ref,omitempty" db:"event_ref"`
	Source            *string         `json:"source,omitempty" db:"evidence_source"`
	Why               *string         `json:"why,omitempty" db:"evidence_why"`
	UntilAtOffset     *string         `json:"until_at_offset,omitempty" db:"until_at_offset"`
	EvidenceStatus    *string         `json:"evidence_status,omitempty" db:"evidence_status"`
	DecisionRequestID *uuid.UUID      `json:"decision_request_id,omitempty" db:"decision_request_id"`
	HumanGateRef      *string         `json:"human_gate_ref,omitempty" db:"human_gate_ref"`
	Note              string          `json:"note" db:"note"`
	CreatedAt         time.Time       `json:"created_at" db:"created_at"`
	ResolvedAt        *time.Time      `json:"resolved_at,omitempty" db:"resolved_at"`
	Resolution        *WaitResolution `json:"resolution,omitempty" db:"resolution"`
}

// Validate checks the shape of a wait about to be declared: its kind has a
// target, a card does not wait on itself, a timer stays inside its bounds. It
// does not look the target up — existence and terminal state are the
// service's part, which needs the store.
func (w *TaskWait) Validate(now time.Time) error {
	if len(w.Note) > MaxWaitNoteLen {
		return outcomeErr(CodeInvalidTarget, "note is longer than 500 characters")
	}
	switch w.Kind {
	case TaskWaitCard:
		if w.TargetTaskID == nil {
			return outcomeErr(CodeInvalidTarget, "a card wait needs target_task_id")
		}
		if *w.TargetTaskID == w.TaskID {
			return outcomeErr(CodeSelfWait, "a card cannot wait on itself")
		}
		if w.TargetCondition != nil && !DependencyCondition(*w.TargetCondition).Valid() {
			return outcomeErr(CodeInvalidTarget, "unknown target_condition")
		}
	case TaskWaitMR:
		if emptyStr(w.Provider) || emptyStr(w.ProjectPath) || w.MRIID == nil {
			return outcomeErr(CodeInvalidTarget, "an mr wait needs provider, project_path and mr_iid")
		}
		if w.Until == nil || !oneOfStr(*w.Until, WaitUntilMerged, WaitUntilGreen, WaitUntilChanged) {
			return outcomeErr(CodeInvalidTarget, "an mr wait needs until: merged, green or changed")
		}
	case TaskWaitPipeline:
		if emptyStr(w.ProjectPath) || w.PipelineID == nil {
			return outcomeErr(CodeInvalidTarget, "a pipeline wait needs project_path and pipeline_id")
		}
		if w.Until == nil || !oneOfStr(*w.Until, WaitUntilFinished, WaitUntilGreen) {
			return outcomeErr(CodeInvalidTarget, "a pipeline wait needs until: finished or green")
		}
	case TaskWaitTime:
		if w.Until != nil {
			return outcomeErr(CodeNaiveTime, "a time wait takes an absolute until_at, not a relative until")
		}
		if w.UntilAt == nil {
			return outcomeErr(CodeInvalidTarget, "a time wait needs until_at")
		}
		if d := w.UntilAt.Sub(now); d < MinTimeWait || d > MaxTimeWait {
			return outcomeErr(CodeTimeOutOfBounds, "until_at must be between 5 minutes and 30 days from now")
		}
		return validateTimeEvidence(w)
	case TaskWaitDecision:
		if w.DecisionRequestID == nil {
			return outcomeErr(CodeInvalidTarget, "a decision wait needs decision_request_id")
		}
	case TaskWaitHuman:
		if emptyStr(w.HumanGateRef) {
			return outcomeErr(CodeInvalidTarget, "a human wait needs human_gate_ref")
		}
	default:
		return outcomeErr(CodeInvalidTarget, "unknown wait kind")
	}
	return nil
}

// TurnOutcomeKind is how a turn ended (spec §4.2).
type TurnOutcomeKind string

const (
	TurnOutcomeNext      TurnOutcomeKind = "next"
	TurnOutcomeWait      TurnOutcomeKind = "wait"
	TurnOutcomeDone      TurnOutcomeKind = "done"
	TurnOutcomeReview    TurnOutcomeKind = "review"
	TurnOutcomeHandoff   TurnOutcomeKind = "handoff"
	TurnOutcomeCancelled TurnOutcomeKind = "cancelled"
)

// TurnOutcome is one append-only entry of the turn journal. The runner reads
// the latest entry of its session; none after a turn is the explicit
// no_outcome state.
type TurnOutcome struct {
	ID         uuid.UUID       `json:"id" db:"id"`
	TaskID     uuid.UUID       `json:"task_id" db:"task_id"`
	AgentID    uuid.UUID       `json:"agent_id" db:"agent_id"`
	SessionID  *string         `json:"session_id,omitempty" db:"session_id"`
	TurnID     *string         `json:"turn_id,omitempty" db:"turn_id"`
	Kind       TurnOutcomeKind `json:"kind" db:"kind"`
	WaitID     *uuid.UUID      `json:"wait_id,omitempty" db:"wait_id"`
	ResultKind *ResultKind     `json:"result_kind,omitempty" db:"result_kind"`
	Note       string          `json:"note" db:"note"`
	CreatedAt  time.Time       `json:"created_at" db:"created_at"`
}

// DecisionState is the life of a question to an agent (spec §4.3).
type DecisionState string

const (
	DecisionOpen      DecisionState = "open"
	DecisionAnswered  DecisionState = "answered"
	DecisionWithdrawn DecisionState = "withdrawn"
)

// DecisionRequest is a question one agent asks another, addressed by id from
// the team directory, never by @-handle.
type DecisionRequest struct {
	ID              uuid.UUID     `json:"id" db:"id"`
	TaskID          uuid.UUID     `json:"task_id" db:"task_id"`
	FromAgentID     uuid.UUID     `json:"from_agent_id" db:"from_agent_id"`
	ToAgentID       uuid.UUID     `json:"to_agent_id" db:"to_agent_id"`
	Question        string        `json:"question" db:"question"`
	State           DecisionState `json:"state" db:"state"`
	AnswerCommentID *uuid.UUID    `json:"answer_comment_id,omitempty" db:"answer_comment_id"`
	AnsweredAt      *time.Time    `json:"answered_at,omitempty" db:"answered_at"`
	CreatedAt       time.Time     `json:"created_at" db:"created_at"`
}

// CommentWakes reports whether a comment wakes anyone: metadata.wakes=false
// marks a note that creates no mention or handoff delivery and wakes no
// waiting runner. Absent, null, malformed or non-boolean means true.
func CommentWakes(meta json.RawMessage) bool {
	if len(meta) == 0 {
		return true
	}
	var m struct {
		Wakes *bool `json:"wakes"`
	}
	if err := json.Unmarshal(meta, &m); err != nil || m.Wakes == nil {
		return true
	}
	return *m.Wakes
}

func emptyStr(s *string) bool { return s == nil || *s == "" }

func oneOfStr(s string, vals ...string) bool {
	for _, v := range vals {
		if s == v {
			return true
		}
	}
	return false
}

// NewOutcomeError builds a refusal of a typed-outcome command.
func NewOutcomeError(code, msg string) *OutcomeError { return outcomeErr(code, msg) }

// WaitRuntimeRefPrefix marks a runtime_state written from a task_waits row:
// ref = "wait:<wait id>". Such a state is kept fresh by the wait itself, not
// by a runner's periodic PUT, so it goes stale only when its until passes.
const WaitRuntimeRefPrefix = "wait:"

// TurnCommand is one turn-ending command after its input has been checked:
// everything the store writes in one transaction. Kind is next, wait or
// handoff; Wait is set for wait, HandoffTo for handoff.
type TurnCommand struct {
	TaskID    uuid.UUID
	AgentID   uuid.UUID
	SessionID *string
	TurnID    *string
	Kind      TurnOutcomeKind
	Wait      *TaskWait
	HandoffTo *uuid.UUID
	Note      string
	// CommentBody is the server-written, human-readable comment of the turn.
	CommentBody string
	// RuntimeReason is the short text put on the card's runtime_state.
	RuntimeReason string
}

// TurnCommandTask is the card as the command left it.
type TurnCommandTask struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Status     string     `json:"status" db:"status"`
	AssigneeID *uuid.UUID `json:"assignee_id" db:"assignee_id"`
}

// TurnCommandResult is the success envelope of a typed-outcome command
// (docs/api/typed-outcomes.md §Commands).
type TurnCommandResult struct {
	Outcome   *TurnOutcome    `json:"outcome,omitempty"`
	Wait      *TaskWait       `json:"wait,omitempty"`
	CommentID uuid.UUID       `json:"comment_id"`
	Task      TurnCommandTask `json:"task"`
	Released  bool            `json:"released"`
}

// TimeEvidenceUnverified is the only evidence status the server writes: it
// checks the shape of a time wait's evidence and that a Mesh receipt exists,
// not that the source really publishes this deadline.
const TimeEvidenceUnverified = "unverified"

// MaxTimeEvidenceLen bounds event_ref, source and why of a time wait and the
// reason of a start_after write.
const MaxTimeEvidenceLen = 500

// minWhyLen keeps "why" from being a placeholder.
const minWhyLen = 10

// ParseEvidenceSource splits a time wait's source into an https URL or a Mesh
// receipt ("comment:<uuid>" / "artifact:<uuid>"). receiptKind is empty for a URL.
func ParseEvidenceSource(src string) (receiptKind string, receiptID uuid.UUID, err error) {
	for _, kind := range []string{"comment", "artifact"} {
		if rest, ok := strings.CutPrefix(src, kind+":"); ok {
			id, perr := uuid.Parse(rest)
			if perr != nil {
				return "", uuid.Nil, outcomeErr(CodeMissingEvidence, "source "+kind+": needs a UUID")
			}
			return kind, id, nil
		}
	}
	u, perr := url.Parse(src)
	if perr != nil || u.Scheme != "https" || u.Host == "" {
		return "", uuid.Nil, outcomeErr(CodeMissingEvidence, "source must be an https URL, comment:<uuid> or artifact:<uuid>")
	}
	return "", uuid.Nil, nil
}

// validateTimeEvidence refuses a time wait that names no external event with a
// published deadline. A generic retry or poll has no such event and is refused.
func validateTimeEvidence(w *TaskWait) error {
	for name, v := range map[string]*string{"event_ref": w.EventRef, "source": w.Source, "why": w.Why} {
		if emptyStr(v) {
			return outcomeErr(CodeMissingEvidence, "a time wait needs event_ref, source and why: the external event and where its deadline is published")
		}
		if len(*v) > MaxTimeEvidenceLen {
			return outcomeErr(CodeMissingEvidence, name+" is longer than 500 characters")
		}
	}
	if len(strings.TrimSpace(*w.Why)) < minWhyLen {
		return outcomeErr(CodeMissingEvidence, "why must say what happens at until_at")
	}
	if err := refuseGenericRetry(*w.EventRef); err != nil {
		return err
	}
	if emptyStr(w.UntilAtOffset) {
		return outcomeErr(CodeMissingEvidence, "until_at needs an explicit Z or UTC offset")
	}
	if _, _, err := ParseEvidenceSource(*w.Source); err != nil {
		return err
	}
	status := TimeEvidenceUnverified
	w.EvidenceStatus = &status
	return nil
}
