package domain

import (
	"strings"

	"github.com/google/uuid"
)

// Inputs and state of the typed-outcome commands ask_agent, answer_decision
// and finish_task (docs/api/typed-outcomes.md §Commands 3–5). Each command is
// one transaction: a refusal writes nothing.

// MaxEvidenceLen bounds the evidence text of finish_task (4 KB).
const (
	MaxEvidenceLen   = 4096
	MaxEvidenceLinks = 20
)

// ResultKindLabelPrefix is the label family mirrored from tasks.result_kind.
const ResultKindLabelPrefix = "kind:"

// MirrorResultKindLabel returns labels with every kind:* label replaced by the
// one for k, placed where the first kind:* label stood (appended when there was
// none). Other labels keep their order.
func MirrorResultKindLabel(labels []string, k ResultKind) []string {
	want := ResultKindLabelPrefix + string(k)
	out := make([]string, 0, len(labels)+1)
	placed := false
	for _, l := range labels {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), ResultKindLabelPrefix) {
			if !placed {
				out = append(out, want)
				placed = true
			}
			continue
		}
		out = append(out, l)
	}
	if !placed {
		out = append(out, want)
	}
	return out
}

// TaskFinish is what finish_task writes inside the move's own transaction, so
// the status, the result kind, the label, the closing comment, the outcome,
// the cancelled wait and the checkout release land together or not at all.
type TaskFinish struct {
	ResultKind ResultKind
	// Caller is the finishing agent: a checkout held by anyone else refuses
	// the move, and Caller's active wait on the card is cancelled.
	Caller uuid.UUID
	// Labels is the task's label set with the kind:* mirror. Filled by the
	// move from the row it read, so a concurrent label change is a conflict,
	// never a silent overwrite.
	Labels  []string
	Comment *Comment
	// Outcome is appended with the task's id; ID and CreatedAt are filled in.
	Outcome *TurnOutcome
}

// AskAgentWrite is the ask_agent transaction: open (or reuse) the request,
// declare the caller's decision wait, append the outcome, write the comment
// and release the caller's checkout.
type AskAgentWrite struct {
	TaskID    uuid.UUID
	Caller    uuid.UUID
	SessionID *string
	TurnID    *string
	// To is the addressee as the caller named it: an agent id or a name/slug
	// from the team directory of the card's workspace.
	To       string
	Question string
	// CommentBody renders the server comment from the resolved request.
	CommentBody func(req *DecisionRequest, to *AgentRef, reused bool) string
}

// AgentRef is the addressee as resolved from the directory.
type AgentRef struct {
	ID   uuid.UUID `json:"id" db:"id"`
	Name string    `json:"name" db:"name"`
	Slug string    `json:"slug" db:"slug"`
}

// AskAgentResult is what ask_agent wrote.
type AskAgentResult struct {
	Request   *DecisionRequest `json:"decision_request"`
	Reused    bool             `json:"reused"`
	To        AgentRef         `json:"to"`
	Wait      *TaskWait        `json:"wait"`
	Outcome   *TurnOutcome     `json:"outcome"`
	CommentID uuid.UUID        `json:"comment_id"`
	Released  bool             `json:"released"`
}

// AnswerDecisionWrite is the answer_decision transaction.
type AnswerDecisionWrite struct {
	RequestID uuid.UUID
	ActorID   uuid.UUID
	ActorType ActorType
	// CommentBody renders the answer comment from the locked request.
	CommentBody func(req *DecisionRequest) string
}

// AnswerDecisionResult is what answer_decision wrote. MetWaits are the asker's
// decision waits it resolved as met.
type AnswerDecisionResult struct {
	Request   *DecisionRequest `json:"decision_request"`
	CommentID uuid.UUID        `json:"comment_id"`
	MetWaits  []uuid.UUID      `json:"-"`
}

// CommandTaskRef is the task part of a command response.
type CommandTaskRef struct {
	ID       uuid.UUID  `json:"id"`
	Status   string     `json:"status"`
	Assignee *uuid.UUID `json:"assignee_id"`
}

// CommandResult is the success envelope of the task commands.
type CommandResult struct {
	Outcome   *TurnOutcome    `json:"outcome,omitempty"`
	Wait      *TaskWait       `json:"wait,omitempty"`
	CommentID uuid.UUID       `json:"comment_id"`
	Task      *CommandTaskRef `json:"task,omitempty"`
	Released  bool            `json:"released"`
	// ask_agent only.
	DecisionRequest *DecisionRequest `json:"decision_request,omitempty"`
	Reused          *bool            `json:"reused,omitempty"`
}
