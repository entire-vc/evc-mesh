package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TypedOutcomeCommands are the turn-ending commands ask_agent, answer_decision
// and finish_task (docs/api/typed-outcomes.md §Commands 3–5). Each validates
// its input first and then writes in one transaction; a refusal writes nothing.
type TypedOutcomeCommands interface {
	AskAgent(ctx context.Context, taskID uuid.UUID, in AskAgentInput) (*domain.CommandResult, error)
	AnswerDecision(ctx context.Context, requestID uuid.UUID, in AnswerDecisionInput) (*domain.AnswerDecisionResult, error)
	FinishTask(ctx context.Context, taskID uuid.UUID, in FinishTaskInput) (*domain.CommandResult, error)
}

// CommandSession is the optional runner context every task command copies
// onto its outcome and wait.
type CommandSession struct {
	SessionID *string `json:"session_id"`
	TurnID    *string `json:"turn_id"`
}

func (s CommandSession) validate() error {
	for name, v := range map[string]*string{"session_id": s.SessionID, "turn_id": s.TurnID} {
		if v != nil && utf8.RuneCountInString(*v) > domain.MaxSessionIDLen {
			return apierror.BadRequest(name + " is longer than 200 characters")
		}
	}
	return nil
}

// AskAgentInput is the body of POST /tasks/:task_id/decision-requests.
type AskAgentInput struct {
	CommandSession
	To       string `json:"to"`
	Question string `json:"question"`
}

// AnswerDecisionInput is the body of POST /decision-requests/:id/answer.
type AnswerDecisionInput struct {
	Answer string `json:"answer"`
}

// FinishTaskInput is the body of POST /tasks/:task_id/finish.
type FinishTaskInput struct {
	CommandSession
	ResultKind    string   `json:"result_kind"`
	Evidence      string   `json:"evidence"`
	EvidenceLinks []string `json:"evidence_links"`
	To            string   `json:"to"`
}

type typedOutcomeCommandRepository interface {
	AskAgent(context.Context, domain.AskAgentWrite) (*domain.AskAgentResult, error)
	AnswerDecision(context.Context, domain.AnswerDecisionWrite) (*domain.AnswerDecisionResult, error)
}

// commandAgent is the calling agent: the task commands end an agent's turn,
// so a person is refused (answer_decision accepts people separately).
func commandAgent(ctx context.Context) (uuid.UUID, error) {
	id, kind := actorctx.FromContext(ctx)
	if id == uuid.Nil || kind != domain.ActorTypeAgent {
		return uuid.Nil, apierror.Forbidden("this command ends an agent's turn: call it with an agent key")
	}
	return id, nil
}

func checkText(field, v string, limit int) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", apierror.BadRequest(field + " is required")
	}
	if utf8.RuneCountInString(v) > limit {
		return "", apierror.BadRequest(fmt.Sprintf("%s is longer than %d characters", field, limit))
	}
	return v, nil
}

func (s *taskService) AskAgent(ctx context.Context, taskID uuid.UUID, in AskAgentInput) (*domain.CommandResult, error) {
	caller, err := commandAgent(ctx)
	if err != nil {
		return nil, err
	}
	question, err := checkText("question", in.Question, domain.MaxQuestionLen)
	if err != nil {
		return nil, err
	}
	if verr := in.validate(); verr != nil {
		return nil, verr
	}
	repo, ok := s.taskRepo.(typedOutcomeCommandRepository)
	if !ok {
		return nil, apierror.ServiceUnavailable("typed-outcome commands unavailable")
	}
	res, err := repo.AskAgent(ctx, domain.AskAgentWrite{
		TaskID: taskID, Caller: caller, SessionID: in.SessionID, TurnID: in.TurnID,
		To: in.To, Question: question,
		CommentBody: func(req *domain.DecisionRequest, to *domain.AgentRef, reused bool) string {
			head := fmt.Sprintf("❔ **Question to %s** · request %#q", to.Name, req.ID)
			if reused {
				head += " (repeat: the open request is reused)"
			}
			return head + "\n\n" + req.Question
		},
	})
	if err != nil {
		return nil, err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, taskID)
	}
	out := &domain.CommandResult{Outcome: res.Outcome, Wait: res.Wait, CommentID: res.CommentID,
		Released: res.Released, DecisionRequest: res.Request, Reused: &res.Reused}
	out.Task = s.commandTaskRef(ctx, taskID)
	return out, nil
}

func (s *taskService) AnswerDecision(ctx context.Context, requestID uuid.UUID, in AnswerDecisionInput) (*domain.AnswerDecisionResult, error) {
	actor, err := commandAgent(ctx)
	if err != nil {
		return nil, apierror.Forbidden("only the addressee of the question may answer it")
	}
	answer, err := checkText("answer", in.Answer, domain.MaxQuestionLen)
	if err != nil {
		return nil, err
	}
	repo, ok := s.taskRepo.(typedOutcomeCommandRepository)
	if !ok {
		return nil, apierror.ServiceUnavailable("typed-outcome commands unavailable")
	}
	res, err := repo.AnswerDecision(ctx, domain.AnswerDecisionWrite{
		RequestID: requestID, ActorID: actor, ActorType: domain.ActorTypeAgent,
		CommentBody: func(req *domain.DecisionRequest) string {
			q := req.Question
			if utf8.RuneCountInString(q) > 300 {
				q = string([]rune(q)[:300]) + "…"
			}
			return fmt.Sprintf("💬 **Answer to question** %#q\n\n> %s\n\n%s", req.ID, strings.ReplaceAll(q, "\n", "\n> "), answer)
		},
	})
	if err != nil {
		return nil, err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, res.Request.TaskID)
	}
	// The asker's wait is met in the database; also tell its lane now, the
	// way a comment on its card would, instead of leaving it to the next poll.
	if s.agentNotifySvc != nil {
		if task, terr := s.taskRepo.GetByID(ctx, res.Request.TaskID); terr == nil && task != nil {
			var wsID uuid.UUID
			if ws, ok := s.resolveProjectWorkspace(ctx, task.ProjectID); ok {
				wsID = ws
			}
			s.agentNotifySvc.NotifyAgent(ctx, res.Request.FromAgentID, AgentNotification{
				EventType: "task.commented", Timestamp: timeNow(), WorkspaceID: wsID,
				Task: s.buildTaskSnapshot(ctx, task), AgentID: res.Request.FromAgentID,
				ActorID: actor, ActorType: string(domain.ActorTypeAgent), ActorName: actorctx.NameFromContext(ctx),
				Comment: map[string]any{"id": res.CommentID, "body": answer, "author_id": actor},
				Changes: map[string]any{"decision_request_id": res.Request.ID, "state": res.Request.State},
				TaskID:  task.ID, ProjectID: task.ProjectID,
			})
		}
	}
	return res, nil
}

func (s *taskService) FinishTask(ctx context.Context, taskID uuid.UUID, in FinishTaskInput) (*domain.CommandResult, error) {
	caller, err := commandAgent(ctx)
	if err != nil {
		return nil, err
	}
	kind := domain.ResultKind(strings.TrimSpace(in.ResultKind))
	if !kind.Valid() {
		return nil, domain.NewOutcomeError(domain.CodeMissingResultKind,
			"result_kind is required: change, defect, research, content or ops")
	}
	evidence := strings.TrimSpace(in.Evidence)
	if evidence == "" {
		return nil, domain.NewOutcomeError(domain.CodeMissingEvidence,
			"evidence is required: what proves the result (command output, links)")
	}
	if len(evidence) > domain.MaxEvidenceLen {
		return nil, apierror.BadRequest("evidence is longer than 4 KB; link the rest")
	}
	if len(in.EvidenceLinks) > domain.MaxEvidenceLinks {
		return nil, apierror.BadRequest("at most 20 evidence_links")
	}
	for _, l := range in.EvidenceLinks {
		u, perr := url.Parse(strings.TrimSpace(l))
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, apierror.BadRequest("evidence_links must be http(s) URLs")
		}
	}
	var category domain.StatusCategory
	var outcomeKind domain.TurnOutcomeKind
	switch in.To {
	case "done":
		category, outcomeKind = domain.StatusCategoryDone, domain.TurnOutcomeDone
	case "review":
		category, outcomeKind = domain.StatusCategoryReview, domain.TurnOutcomeReview
	default:
		return nil, apierror.BadRequest("to must be done or review")
	}
	if verr := in.validate(); verr != nil {
		return nil, verr
	}

	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, apierror.NotFound("Task")
	}
	status, err := s.firstStatusOfCategory(ctx, task.ProjectID, category)
	if err != nil {
		return nil, err
	}

	body := fmt.Sprintf("**Result: %s** · result kind %#q\n\n%s", in.To, kind, evidence)
	if len(in.EvidenceLinks) > 0 {
		body += "\n\nLinks:"
		for _, l := range in.EvidenceLinks {
			body += "\n- " + strings.TrimSpace(l)
		}
	}
	meta := fmt.Sprintf(`{"command":"finish_task","result_kind":%q,"to":%q}`, kind, in.To)
	finish := &domain.TaskFinish{
		ResultKind: kind, Caller: caller,
		Comment: &domain.Comment{AuthorID: caller, AuthorType: domain.ActorTypeAgent, Body: body, Metadata: []byte(meta)},
		Outcome: &domain.TurnOutcome{AgentID: caller, SessionID: in.SessionID, TurnID: in.TurnID,
			Kind: outcomeKind, ResultKind: &kind, Note: evidence},
	}
	heldByCaller := task.CheckedOutBy != nil && *task.CheckedOutBy == caller
	if err := s.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &status.ID, Source: "finish_task", Finish: finish}); err != nil {
		return nil, err
	}
	out := &domain.CommandResult{Outcome: finish.Outcome, CommentID: finish.Comment.ID}
	out.Task = s.commandTaskRef(ctx, taskID)
	if fresh, ferr := s.taskRepo.GetByID(ctx, taskID); ferr == nil && fresh != nil {
		out.Released = heldByCaller && fresh.CheckedOutBy == nil
	}
	return out, nil
}

// firstStatusOfCategory is the status a command moves a card into: the
// lowest-positioned status of that category in the card's project.
func (s *taskService) firstStatusOfCategory(ctx context.Context, projectID uuid.UUID, category domain.StatusCategory) (*domain.TaskStatus, error) {
	statuses, err := s.statusRepo.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var best *domain.TaskStatus
	for i := range statuses {
		st := &statuses[i]
		if st.Category == category && (best == nil || st.Position < best.Position) {
			best = st
		}
	}
	if best == nil {
		return nil, apierror.BadRequest("the project has no " + string(category) + " status")
	}
	return best, nil
}

func (s *taskService) commandTaskRef(ctx context.Context, taskID uuid.UUID) *domain.CommandTaskRef {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return &domain.CommandTaskRef{ID: taskID}
	}
	ref := &domain.CommandTaskRef{ID: taskID, Assignee: task.AssigneeID}
	if st, serr := s.statusRepo.GetByID(ctx, task.StatusID); serr == nil && st != nil {
		ref.Status = st.Slug
	}
	return ref
}
