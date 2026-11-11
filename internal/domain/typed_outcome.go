package domain

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
	// TaskWaitMerge is the runner's merge gate on a GitLab MR at the reviewed
	// head (after_sha): the runner merges and wakes the agent when merged or blocked.
	TaskWaitMerge TaskWaitKind = "merge"
	// TaskWaitRegistered is an event of a runner-owned registration the server
	// cannot observe: Registration names its sub-kind, RegistrationRef the entry.
	TaskWaitRegistered TaskWaitKind = "registered"
)

// Registrations a registered wait may name, with the shape of their ref.
// Card-scoped ones are armed for the waiting card itself: their ref is the
// card's 8-hex short id.
const (
	RegistrationOpsReview = "ops-review" // 64-hex registered request id
	RegistrationGmail     = "gmail"      // gmr_<12 hex>
	RegistrationReceipt   = "receipt"    // this card's 8-hex id
	RegistrationLifecycle = "lifecycle"  // this card's 8-hex id
	RegistrationStall     = "stall"      // this card's 8-hex id
	RegistrationRecovery  = "recovery"   // this card's 8-hex id
	RegistrationQueue     = "queue"      // this card's 8-hex id
	RegistrationNative    = "native"     // 64-hex registration
)

var (
	registrationRefShape = map[string]*regexp.Regexp{
		RegistrationOpsReview: regexp.MustCompile(`^[0-9a-f]{64}$`),
		RegistrationNative:    regexp.MustCompile(`^[0-9a-f]{64}$`),
		RegistrationGmail:     regexp.MustCompile(`^gmr_[0-9a-f]{12}$`),
	}
	cardScopedRegistrations = []string{RegistrationReceipt, RegistrationLifecycle, RegistrationStall, RegistrationRecovery, RegistrationQueue}
	headSHA                 = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Registrations lists every registration a registered wait accepts.
func Registrations() []string {
	return []string{RegistrationOpsReview, RegistrationGmail, RegistrationReceipt, RegistrationLifecycle,
		RegistrationStall, RegistrationRecovery, RegistrationQueue, RegistrationNative}
}

// CardScopedRegistration reports whether a registration is armed for the
// waiting card itself, so its ref is that card's short id.
func CardScopedRegistration(r string) bool { return slices.Contains(cardScopedRegistrations, r) }

// CIBaselineStatuses are the pipeline states a CI baseline (after_status) may
// name: GitLab's pipeline statuses and GitHub's spelling "cancelled".
var CIBaselineStatuses = []string{"created", "waiting_for_resource", "preparing", "pending", "running",
	"success", "failed", "canceled", "cancelled", "skipped", "manual", "scheduled"}

// MRBaselineOpened is the only baseline status of an mr wait until merged:
// the head the agent saw on the still-open MR.
const MRBaselineOpened = "opened"

// MaxHumanQuestionLen bounds the question of a human wait: the gate keeps at
// most this much of the ask as its reason.
const MaxHumanQuestionLen = 2000

// Until states. MR waits take merged|green|changed, pipeline waits finished|green|changed.
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
	// CodeGateNotArmed: ask_human posted the question but the human gate did
	// not arm (the comment-text gate is off, or the ask was suppressed).
	CodeGateNotArmed = "gate_not_armed"
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
	EventRef          *string    `json:"event_ref,omitempty" db:"event_ref"`
	Source            *string    `json:"source,omitempty" db:"evidence_source"`
	Why               *string    `json:"why,omitempty" db:"evidence_why"`
	UntilAtOffset     *string    `json:"until_at_offset,omitempty" db:"until_at_offset"`
	EvidenceStatus    *string    `json:"evidence_status,omitempty" db:"evidence_status"`
	DecisionRequestID *uuid.UUID `json:"decision_request_id,omitempty" db:"decision_request_id"`
	HumanGateRef      *string    `json:"human_gate_ref,omitempty" db:"human_gate_ref"`
	// The baseline an mr, pipeline or merge wait was declared against (`after=`):
	// the head SHA, and for a CI baseline the pipeline and its status then.
	AfterSHA        *string `json:"after_sha,omitempty" db:"after_sha"`
	AfterPipelineID *int64  `json:"after_pipeline_id,omitempty" db:"after_pipeline_id"`
	AfterStatus     *string `json:"after_status,omitempty" db:"after_status"`
	// A registered wait's sub-kind and its opaque reference.
	Registration    *string `json:"registration,omitempty" db:"registration"`
	RegistrationRef *string `json:"registration_ref,omitempty" db:"registration_ref"`
	// A human wait's person (username) and question.
	Addressee  *string         `json:"addressee,omitempty" db:"addressee"`
	Question   *string         `json:"question,omitempty" db:"question"`
	Note       string          `json:"note" db:"note"`
	CreatedAt  time.Time       `json:"created_at" db:"created_at"`
	ResolvedAt *time.Time      `json:"resolved_at,omitempty" db:"resolved_at"`
	Resolution *WaitResolution `json:"resolution,omitempty" db:"resolution"`
	// Who ended the wait through the resolve route, and what ended it.
	ResolvedBy   *uuid.UUID `json:"resolved_by,omitempty" db:"resolved_by"`
	ResolveCause *string    `json:"resolve_cause,omitempty" db:"resolve_cause"`
}

// MaxResolveCauseLen bounds the cause the resolver gives.
const MaxResolveCauseLen = 200

// ResolvableByRoute reports whether the resolve route may set r: every
// resolution but superseded, which only a newer wait of the same agent sets.
func (r WaitResolution) ResolvableByRoute() bool {
	switch r {
	case WaitResolutionMet, WaitResolutionCancelled, WaitResolutionExpired, WaitResolutionInvalid:
		return true
	}
	return false
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
		return validateBaseline(w)
	case TaskWaitPipeline:
		if emptyStr(w.ProjectPath) || w.PipelineID == nil {
			return outcomeErr(CodeInvalidTarget, "a pipeline wait needs project_path and pipeline_id")
		}
		if w.Until == nil || !oneOfStr(*w.Until, WaitUntilFinished, WaitUntilGreen, WaitUntilChanged) {
			return outcomeErr(CodeInvalidTarget, "a pipeline wait needs until: finished, green or changed")
		}
		if w.Provider != nil && !oneOfStr(*w.Provider, "gitlab", "github") {
			return outcomeErr(CodeInvalidTarget, "a pipeline wait's provider is gitlab or github")
		}
		if w.Provider != nil && *w.Provider == "github" && strings.Count(*w.ProjectPath, "/") != 1 {
			return outcomeErr(CodeInvalidTarget, "a GitHub pipeline wait needs project_path owner/repo and the Actions run id as pipeline_id")
		}
		return validateBaseline(w)
	case TaskWaitMerge:
		if emptyStr(w.ProjectPath) || !strings.Contains(*w.ProjectPath, "/") || w.MRIID == nil || *w.MRIID < 1 {
			return outcomeErr(CodeInvalidTarget, "a merge wait needs project_path (group/project) and mr_iid")
		}
		if w.Provider != nil && *w.Provider != "gitlab" {
			return outcomeErr(CodeInvalidTarget, "a merge wait is GitLab only: the runner's merge gate reads the configured GitLab")
		}
		if w.AfterSHA == nil || !headSHA.MatchString(*w.AfterSHA) {
			return outcomeErr(CodeInvalidTarget, "a merge wait needs after_sha: the full 40 or 64 hex-digit SHA of the reviewed head")
		}
		if w.Until != nil || w.AfterPipelineID != nil || w.AfterStatus != nil || len(w.RequiredJobs) > 0 {
			return outcomeErr(CodeInvalidTarget, "a merge wait takes project_path, mr_iid and after_sha only")
		}
	case TaskWaitRegistered:
		return validateRegistration(w)
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
		if emptyStr(w.Addressee) || emptyStr(w.Question) {
			return outcomeErr(CodeInvalidTarget, "a human wait needs the person asked and the question")
		}
		if utf8.RuneCountInString(*w.Question) > MaxHumanQuestionLen {
			return outcomeErr(CodeInvalidTarget, "question is longer than 2000 characters")
		}
	default:
		return outcomeErr(CodeInvalidTarget, "unknown wait kind")
	}
	return nil
}

// validateBaseline checks the explicit baseline of an mr or pipeline wait
// (`after=<sha>:opened` or `after=<sha>:<pipeline>:<status>`). No baseline is
// fine: the runner takes the state at the moment the wait is declared.
func validateBaseline(w *TaskWait) error {
	if w.AfterSHA == nil && w.AfterPipelineID == nil && w.AfterStatus == nil {
		return nil
	}
	if w.AfterSHA == nil || !headSHA.MatchString(*w.AfterSHA) {
		return outcomeErr(CodeInvalidTarget, "a baseline needs after_sha: the full 40 or 64 hex-digit head SHA")
	}
	switch *w.Until {
	case WaitUntilMerged:
		if w.AfterPipelineID != nil || w.AfterStatus == nil || *w.AfterStatus != MRBaselineOpened {
			return outcomeErr(CodeInvalidTarget, "the baseline of until=merged is after_sha with after_status opened, no after_pipeline_id")
		}
	case WaitUntilGreen, WaitUntilChanged:
		if w.AfterPipelineID == nil || *w.AfterPipelineID < 1 || w.AfterStatus == nil || !slices.Contains(CIBaselineStatuses, *w.AfterStatus) {
			return outcomeErr(CodeInvalidTarget, "a CI baseline needs after_sha, after_pipeline_id and after_status (a pipeline status: "+
				strings.Join(CIBaselineStatuses, ", ")+")")
		}
		if w.Kind == TaskWaitPipeline && *w.AfterPipelineID != *w.PipelineID {
			return outcomeErr(CodeInvalidTarget, "the baseline's after_pipeline_id must be the awaited pipeline_id")
		}
	default:
		return outcomeErr(CodeInvalidTarget, "until="+*w.Until+" takes no baseline; drop after_sha, after_pipeline_id and after_status")
	}
	return nil
}

// validateRegistration checks a registered wait: a known sub-kind and a ref
// of its shape. A card-scoped registration is armed for this card, so its ref
// is this card's short id and is filled in when omitted.
func validateRegistration(w *TaskWait) error {
	if emptyStr(w.Registration) || !slices.Contains(Registrations(), *w.Registration) {
		return outcomeErr(CodeInvalidTarget, "registration must be one of "+strings.Join(Registrations(), ", "))
	}
	reg := *w.Registration
	if CardScopedRegistration(reg) {
		own := w.TaskID.String()[:8]
		if w.RegistrationRef == nil || *w.RegistrationRef == "" {
			w.RegistrationRef = &own
		}
		if *w.RegistrationRef != own {
			return outcomeErr(CodeInvalidTarget, "a "+reg+" registration is armed for this card: registration_ref is its 8-hex id "+own)
		}
	} else if w.RegistrationRef == nil || !registrationRefShape[reg].MatchString(*w.RegistrationRef) {
		shape := "64 hex digits"
		if reg == RegistrationGmail {
			shape = "gmr_ and 12 hex digits"
		}
		return outcomeErr(CodeInvalidTarget, "a "+reg+" wait needs registration_ref: "+shape)
	}
	if w.Until != nil || w.AfterSHA != nil || w.AfterPipelineID != nil || w.AfterStatus != nil || len(w.RequiredJobs) > 0 {
		return outcomeErr(CodeInvalidTarget, "a registered wait takes registration and registration_ref only")
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
	// CommentID, when set, is the turn's comment already written through the
	// comment service (ask_human's question): no second comment is added.
	CommentID *uuid.UUID
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
