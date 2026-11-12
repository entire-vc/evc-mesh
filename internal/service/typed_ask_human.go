package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ask_human (docs/api/typed-outcomes.md §Commands 8): the typed form of the
// marker line `❓ Blocking @<person>: <question>`. The question is posted as
// that very line through the comment service, so the human gate is raised,
// delivered, triaged, answered and withdrawn exactly as for a hand-written
// marker; the command then records the agent's human wait on it, the outcome,
// the runtime state and the checkout release in one transaction.

// TurnHumanGate is the human-gate side of ask_human: the checks set_human_gate
// runs for an agent key, and the comment service that raises the gate.
type TurnHumanGate interface {
	CheckAgentGateArm(ctx context.Context, taskID uuid.UUID, addressee, supersedes, canonicalKey string) error
	Create(ctx context.Context, comment *domain.Comment) error
}

// TurnTaskReader reads the card back after the question, to see the gate armed.
type TurnTaskReader interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error)
}

// WithTasks lets wait_for read the card itself (the wake:manual label of a
// manual wait); WithHumanGate sets the same reader.
func (s *TurnCommandService) WithTasks(tasks TurnTaskReader) *TurnCommandService {
	s.tasks = tasks
	return s
}

// WithHumanGate enables ask_human; without it the command is unavailable.
func (s *TurnCommandService) WithHumanGate(gate TurnHumanGate, tasks TurnTaskReader) *TurnCommandService {
	s.humanGate, s.tasks = gate, tasks
	return s
}

// AskHumanInput is the body of POST /tasks/:task_id/ask-human.
type AskHumanInput struct {
	TurnCommon
	// To is the person's username (an @ prefix is ignored).
	To       string
	Question string
	// RecommendedDefault is what the agent does if nobody answers by Deadline.
	RecommendedDefault string
	Deadline           *time.Time
	// Soft marks a gate the default-on-timeout sweep may release.
	Soft         bool
	CanonicalKey string
	Supersedes   string
}

// personSlug is the username shape the Blocking marker can name.
var personSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$`)

const maxAskFieldLen = 500

// AskHuman asks a person and ends the caller's turn waiting on the answer.
func (s *TurnCommandService) AskHuman(ctx context.Context, taskID uuid.UUID, in AskHumanInput) (*domain.TurnCommandResult, error) {
	agentID, err := callerAgent(ctx)
	if err != nil {
		return nil, err
	}
	err = checkCommon(in.TurnCommon)
	if err != nil {
		return nil, err
	}
	slug := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(in.To), "@"))
	if slug == "" {
		return nil, refuse(GateRefusalAddresseeMissing, "to is required: the username of the person being asked")
	}
	if !personSlug.MatchString(slug) {
		return nil, refuse(domain.CodeInvalidTarget, "to must be a username (letters, digits, hyphens)")
	}
	question := strings.TrimSpace(in.Question)
	inline := strings.Join(strings.Fields(question), " ")
	fields := map[string]string{}
	switch {
	case question == "":
		fields["question"] = "is required"
	case utf8.RuneCountInString(question) > domain.MaxHumanQuestionLen:
		fields["question"] = "must be at most 2000 characters"
	case !in.Soft && blockingMarkerClassRegex.MatchString(inline):
		// The tag on the marker line is what makes a gate soft.
		fields["question"] = "must not carry a [soft] tag; send soft: true"
	}
	for name, v := range map[string]string{"recommended_default": in.RecommendedDefault, "canonical_key": in.CanonicalKey, "supersedes": in.Supersedes} {
		if utf8.RuneCountInString(strings.TrimSpace(v)) > maxAskFieldLen || strings.ContainsAny(strings.TrimSpace(v), "\r\n") {
			fields[name] = "must be one line of at most 500 characters"
		}
	}
	if in.Deadline != nil && !in.Deadline.After(s.now()) {
		fields["deadline"] = "must be in the future"
	}
	if len(fields) > 0 {
		return nil, apierror.ValidationError(fields)
	}
	if s.humanGate == nil || s.tasks == nil {
		return nil, apierror.ServiceUnavailable("ask_human is unavailable")
	}
	if _, _, err = s.store.TaskScope(ctx, taskID); err != nil {
		return nil, err
	}
	// Checked before anything is posted: a question raised by an agent that
	// cannot end its turn here would arm a gate nobody waits on.
	held, err := s.store.HoldsCheckout(ctx, taskID, agentID)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, apierror.Conflict("the caller does not hold this card's checkout")
	}
	err = s.humanGate.CheckAgentGateArm(ctx, taskID, slug, strings.TrimSpace(in.Supersedes), strings.TrimSpace(in.CanonicalKey))
	var refused *GateArmRefusedError
	if errors.As(err, &refused) {
		return nil, refuse(refused.Code, refused.Message)
	}
	if err != nil {
		return nil, err
	}

	comment := &domain.Comment{ID: uuid.New(), TaskID: taskID, AuthorID: agentID, AuthorType: domain.ActorTypeAgent,
		Body: askHumanBody(slug, inline, in)}
	meta := map[string]any{"command": "ask_human"}
	if k := strings.TrimSpace(in.CanonicalKey); k != "" {
		meta["canonical_key"] = k
	}
	if v := strings.TrimSpace(in.Supersedes); v != "" {
		meta["supersedes"] = v
	}
	if comment.Metadata, err = json.Marshal(meta); err != nil {
		return nil, err
	}
	err = s.humanGate.Create(ctx, comment)
	if err != nil {
		return nil, err
	}
	task, err := s.tasks.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil || !task.HumanGate || task.GateAuthor == nil || *task.GateAuthor != agentID {
		return nil, refuse(domain.CodeGateNotArmed, "the question is posted (comment "+comment.ID.String()+
			") but the human gate did not arm: comment-text gates are off here or the ask was suppressed (see the notice on the card)")
	}

	ref := "comment:" + comment.ID.String()
	w := &domain.TaskWait{TaskID: taskID, AgentID: agentID, Kind: domain.TaskWaitHuman, HumanGateRef: &ref,
		Addressee: &slug, Question: &question, Note: "asked @" + slug}
	err = w.Validate(s.now())
	if err != nil {
		return nil, err
	}
	res, err := s.store.CommitTurn(ctx, &domain.TurnCommand{TaskID: taskID, AgentID: agentID,
		SessionID: in.SessionID, TurnID: in.TurnID, Kind: domain.TurnOutcomeWait, Wait: w,
		Note: w.Note, CommentID: &comment.ID, RuntimeReason: clipRuntimeReason(describeWait(w))})
	if err != nil {
		return nil, err
	}
	s.after(ctx, taskID, domain.TurnOutcomeWait)
	return res, nil
}

// askHumanBody is the marker comment: the Blocking line the gate is armed
// from, then the optional lines the marker path reads (default, deadline,
// supersedes).
func askHumanBody(slug, question string, in AskHumanInput) string {
	head := "❓ Blocking @" + slug + ": " + question
	if in.Soft {
		// The class tag must be on the marker line; at its end the gate's
		// reason still starts with the question.
		head += " [soft]"
	}
	lines := []string{head}
	var extra []string
	if v := strings.TrimSpace(in.RecommendedDefault); v != "" {
		extra = append(extra, "Default: "+v)
	}
	if in.Deadline != nil {
		extra = append(extra, "Deadline: "+in.Deadline.UTC().Format(time.RFC3339))
	}
	if v := strings.TrimSpace(in.Supersedes); v != "" {
		extra = append(extra, "supersedes: "+v)
	}
	if len(extra) > 0 {
		lines = append(lines, "", strings.Join(extra, "\n"))
	}
	return strings.Join(lines, "\n")
}
