package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The turn-ending commands wait_for, continue_next, hand_off and add_note
// (docs/api/typed-outcomes.md §Commands 1, 2, 6, 7). This side checks the input
// and the targets that live outside the database (MRs and pipelines on
// GitLab), writes the comment text, and hands the rest to the store, which
// writes everything in one transaction.

// TurnCommandStore is the transactional writer (postgres.TurnCommandRepo).
type TurnCommandStore interface {
	CommitTurn(ctx context.Context, cmd *domain.TurnCommand) (*domain.TurnCommandResult, error)
	AddNote(ctx context.Context, taskID, authorID uuid.UUID, authorType domain.ActorType, body string) (uuid.UUID, error)
	TaskScope(ctx context.Context, taskID uuid.UUID) (projectID, workspaceID uuid.UUID, err error)
	ResolveAgent(ctx context.Context, workspaceID uuid.UUID, to string) (id uuid.UUID, name string, found bool, err error)
}

// TurnVCSLinks reads the card's own VCS links.
type TurnVCSLinks interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.VCSLink, error)
	ListByTask(ctx context.Context, taskID uuid.UUID) ([]domain.VCSLink, error)
}

// TurnHooks are the task-service effects around a command: the same assignee
// checks assign_task runs, and the notifications sent after commit.
type TurnHooks interface {
	CheckHandoffAssignee(ctx context.Context, projectID, agentID uuid.UUID) error
	AfterTurn(ctx context.Context, taskID uuid.UUID, kind domain.TurnOutcomeKind)
}

// GitLabCheckerSource returns the GitLab client of a workspace.
type GitLabCheckerSource func(ctx context.Context, workspaceID uuid.UUID) (gitlabapi.MergeRequestChecker, bool)

// TurnCommon is carried by every task command.
type TurnCommon struct {
	SessionID *string
	TurnID    *string
}

// WaitForInput is the body of POST /tasks/:task_id/wait.
type WaitForInput struct {
	TurnCommon
	On              string
	TargetTaskID    *uuid.UUID
	TargetCondition *string
	VCSLinkID       *uuid.UUID
	Provider        *string
	ProjectPath     *string
	MRIID           *int
	PipelineID      *int64
	RequiredJobs    []string
	Until           *string
	UntilAt         *time.Time
	Note            string
}

// TurnCommandService runs the commands.
type TurnCommandService struct {
	store  TurnCommandStore
	links  TurnVCSLinks
	hooks  TurnHooks
	gitlab GitLabCheckerSource
	now    func() time.Time
}

// NewTurnCommandService creates the service. links, hooks and gitlab may be
// nil: without links or gitlab an MR or pipeline target cannot be confirmed
// and is refused; without hooks no assignee policy or notification runs.
func NewTurnCommandService(store TurnCommandStore, links TurnVCSLinks, hooks TurnHooks, gitlab GitLabCheckerSource) *TurnCommandService {
	return &TurnCommandService{store: store, links: links, hooks: hooks, gitlab: gitlab, now: time.Now}
}

func refuse(code, msg string) error { return domain.NewOutcomeError(code, msg) }

func checkCommon(c TurnCommon) error {
	for name, v := range map[string]*string{"session_id": c.SessionID, "turn_id": c.TurnID} {
		if v != nil && len(*v) > domain.MaxSessionIDLen {
			return apierror.ValidationError(map[string]string{name: "must be at most 200 characters"})
		}
	}
	return nil
}

// WaitFor declares the caller's wait on the card and releases its checkout.
func (s *TurnCommandService) WaitFor(ctx context.Context, taskID uuid.UUID, in WaitForInput) (*domain.TurnCommandResult, error) {
	agentID, err := callerAgent(ctx)
	if err != nil {
		return nil, err
	}
	err = checkCommon(in.TurnCommon)
	if err != nil {
		return nil, err
	}
	w := &domain.TaskWait{TaskID: taskID, AgentID: agentID, Kind: domain.TaskWaitKind(in.On), Note: strings.TrimSpace(in.Note)}
	switch w.Kind {
	case domain.TaskWaitCard:
		w.TargetTaskID, w.TargetCondition = in.TargetTaskID, in.TargetCondition
	case domain.TaskWaitMR:
		w.VCSLinkID, w.Provider, w.ProjectPath, w.MRIID, w.Until = in.VCSLinkID, in.Provider, in.ProjectPath, in.MRIID, in.Until
	case domain.TaskWaitPipeline:
		w.ProjectPath, w.PipelineID, w.RequiredJobs, w.Until = in.ProjectPath, in.PipelineID, in.RequiredJobs, in.Until
	case domain.TaskWaitTime:
		w.UntilAt = in.UntilAt
	default:
		// decision and human waits have their own commands (ask_agent, set_human_gate).
		return nil, refuse(domain.CodeInvalidTarget, "on must be card, mr, pipeline or time")
	}
	_, workspaceID, err := s.store.TaskScope(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if w.Kind == domain.TaskWaitMR && w.VCSLinkID != nil {
		err = s.fillFromLink(ctx, w)
		if err != nil {
			return nil, err
		}
	}
	err = w.Validate(s.now())
	if err != nil {
		return nil, err
	}
	switch w.Kind {
	case domain.TaskWaitMR:
		err = s.checkMR(ctx, workspaceID, w)
	case domain.TaskWaitPipeline:
		err = s.checkPipeline(ctx, workspaceID, w)
	}
	if err != nil {
		return nil, err
	}
	what := describeWait(w)
	body := "Waiting on " + what + "."
	if w.Note != "" {
		body += "\n\n" + w.Note
	}
	res, err := s.store.CommitTurn(ctx, &domain.TurnCommand{TaskID: taskID, AgentID: agentID,
		SessionID: in.SessionID, TurnID: in.TurnID, Kind: domain.TurnOutcomeWait, Wait: w,
		Note: w.Note, CommentBody: body, RuntimeReason: what})
	if err != nil {
		return nil, err
	}
	s.after(ctx, taskID, domain.TurnOutcomeWait)
	return res, nil
}

// ContinueNext records that the caller goes on with the card; the checkout is
// extended, not released.
func (s *TurnCommandService) ContinueNext(ctx context.Context, taskID uuid.UUID, common TurnCommon, note string) (*domain.TurnCommandResult, error) {
	agentID, err := callerAgent(ctx)
	if err != nil {
		return nil, err
	}
	err = checkCommon(common)
	if err != nil {
		return nil, err
	}
	note = strings.TrimSpace(note)
	if len(note) > domain.MaxOutcomeNote {
		return nil, apierror.ValidationError(map[string]string{"note": "must be at most 4096 characters"})
	}
	body := "Continuing with the next step."
	if note != "" {
		body = "Continuing: " + note
	}
	res, err := s.store.CommitTurn(ctx, &domain.TurnCommand{TaskID: taskID, AgentID: agentID,
		SessionID: common.SessionID, TurnID: common.TurnID, Kind: domain.TurnOutcomeNext, Note: note, CommentBody: body})
	if err != nil {
		return nil, err
	}
	s.after(ctx, taskID, domain.TurnOutcomeNext)
	return res, nil
}

// HandOff assigns the card to another agent, moves it to todo and releases the
// caller's checkout.
func (s *TurnCommandService) HandOff(ctx context.Context, taskID uuid.UUID, common TurnCommon, to, why string) (*domain.TurnCommandResult, error) {
	agentID, err := callerAgent(ctx)
	if err != nil {
		return nil, err
	}
	err = checkCommon(common)
	if err != nil {
		return nil, err
	}
	why = strings.TrimSpace(why)
	if why == "" || len(why) > domain.MaxQuestionLen {
		return nil, apierror.ValidationError(map[string]string{"why": "is required, at most 4000 characters"})
	}
	projectID, workspaceID, err := s.store.TaskScope(ctx, taskID)
	if err != nil {
		return nil, err
	}
	toID, toName, found, err := s.store.ResolveAgent(ctx, workspaceID, strings.TrimSpace(to))
	if err != nil {
		return nil, err
	}
	if !found || toID == agentID {
		return nil, refuse(domain.CodeNotAnAgent, fmt.Sprintf("%q is not another agent of the team directory", to))
	}
	if s.hooks != nil {
		err = s.hooks.CheckHandoffAssignee(ctx, projectID, toID)
		if err != nil {
			return nil, err
		}
	}
	res, err := s.store.CommitTurn(ctx, &domain.TurnCommand{TaskID: taskID, AgentID: agentID,
		SessionID: common.SessionID, TurnID: common.TurnID, Kind: domain.TurnOutcomeHandoff, HandoffTo: &toID,
		Note: truncateNote(why), CommentBody: "Handed over to " + toName + ".\n\n" + why})
	if err != nil {
		return nil, err
	}
	s.after(ctx, taskID, domain.TurnOutcomeHandoff)
	return res, nil
}

// AddNote writes a comment that wakes nobody. People may use it too.
func (s *TurnCommandService) AddNote(ctx context.Context, taskID uuid.UUID, text string) (uuid.UUID, error) {
	actorID, actorType := actorctx.FromContext(ctx)
	if actorID == uuid.Nil || (actorType != domain.ActorTypeAgent && actorType != domain.ActorTypeUser) {
		return uuid.Nil, apierror.Forbidden("an agent or a person may add a note")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return uuid.Nil, apierror.ValidationError(map[string]string{"text": "is required"})
	}
	return s.store.AddNote(ctx, taskID, actorID, actorType, text)
}

func (s *TurnCommandService) after(ctx context.Context, taskID uuid.UUID, kind domain.TurnOutcomeKind) {
	if s.hooks != nil {
		s.hooks.AfterTurn(ctx, taskID, kind)
	}
}

func callerAgent(ctx context.Context) (uuid.UUID, error) {
	id, kind := actorctx.FromContext(ctx)
	if id == uuid.Nil || kind != domain.ActorTypeAgent {
		return uuid.Nil, apierror.Forbidden("only an agent ends a turn with this command")
	}
	return id, nil
}

// fillFromLink copies an MR link's coordinates onto the wait. The link must be
// a pr link of this card.
func (s *TurnCommandService) fillFromLink(ctx context.Context, w *domain.TaskWait) error {
	if s.links == nil {
		return refuse(domain.CodeInvalidTarget, "VCS links are unavailable")
	}
	link, err := s.links.GetByID(ctx, *w.VCSLinkID)
	if err != nil {
		return err
	}
	if link == nil || link.TaskID != w.TaskID || link.LinkType != domain.VCSLinkTypePR {
		return refuse(domain.CodeInvalidTarget, "vcs_link_id is not an MR link of this card")
	}
	path, iid, ok := mrCoordinates(link)
	if !ok {
		return refuse(domain.CodeInvalidTarget, "the link's URL does not name a merge request")
	}
	provider := string(link.Provider)
	w.Provider, w.ProjectPath, w.MRIID = &provider, &path, &iid
	return nil
}

func mrCoordinates(link *domain.VCSLink) (projectPath string, iid int, ok bool) {
	if link.Provider != domain.VCSProviderGitLab {
		return "", 0, false
	}
	return gitlabapi.ParseMergeRequestURL(link.URL)
}

// checkMR confirms the MR exists and is still open: live from GitLab when the
// workspace has a client, else from a link of this card. Neither → refused,
// a wait on an MR nobody can see would never end.
func (s *TurnCommandService) checkMR(ctx context.Context, workspaceID uuid.UUID, w *domain.TaskWait) error {
	var link *domain.VCSLink
	if s.links != nil {
		links, err := s.links.ListByTask(ctx, w.TaskID)
		if err != nil {
			return err
		}
		for i := range links {
			l := &links[i]
			if path, iid, ok := mrCoordinates(l); ok && l.LinkType == domain.VCSLinkTypePR &&
				string(l.Provider) == *w.Provider && path == *w.ProjectPath && iid == *w.MRIID {
				link = l
				break
			}
		}
	}
	if link != nil && w.VCSLinkID == nil {
		w.VCSLinkID = &link.ID
	}
	target := fmt.Sprintf("MR %s!%d", *w.ProjectPath, *w.MRIID)
	if *w.Provider == string(domain.VCSProviderGitLab) && s.gitlab != nil {
		if checker, ok := s.gitlab(ctx, workspaceID); ok {
			state, err := checker.GetMergeRequestState(ctx, *w.ProjectPath, *w.MRIID)
			if err != nil {
				return refuse(domain.CodeInvalidTarget, target+" was not found on GitLab")
			}
			if state.Merged || state.State == "closed" {
				return refuse(domain.CodeAlreadyMet, target+" is already "+state.State)
			}
			return nil
		}
	}
	if link == nil {
		return refuse(domain.CodeInvalidTarget, target+" is neither linked to this card nor visible on GitLab")
	}
	if link.Status == domain.VCSLinkStatusMerged || link.Status == domain.VCSLinkStatusClosed {
		return refuse(domain.CodeAlreadyMet, target+" is already "+string(link.Status))
	}
	return nil
}

var terminalPipelineStatuses = []string{"success", "failed", "canceled", "skipped"}

func (s *TurnCommandService) checkPipeline(ctx context.Context, workspaceID uuid.UUID, w *domain.TaskWait) error {
	target := fmt.Sprintf("pipeline %s#%d", *w.ProjectPath, *w.PipelineID)
	var checker gitlabapi.MergeRequestChecker
	ok := false
	if s.gitlab != nil {
		checker, ok = s.gitlab(ctx, workspaceID)
	}
	pipelines, hasPipelines := checker.(interface {
		GetPipelineStatus(context.Context, string, int) (string, error)
	})
	if !ok || !hasPipelines {
		return refuse(domain.CodeInvalidTarget, "GitLab is not configured for this workspace; "+target+" cannot be confirmed")
	}
	status, err := pipelines.GetPipelineStatus(ctx, *w.ProjectPath, int(*w.PipelineID))
	if err != nil {
		return refuse(domain.CodeInvalidTarget, target+" was not found on GitLab")
	}
	if slices.Contains(terminalPipelineStatuses, status) {
		return refuse(domain.CodeAlreadyMet, target+" has already finished: "+status)
	}
	return nil
}

// describeWait is the human-readable target, used in the comment and as the
// card's runtime_state reason.
func describeWait(w *domain.TaskWait) string {
	switch w.Kind {
	case domain.TaskWaitCard:
		cond := string(domain.DependencyConditionTaskDone)
		if w.TargetCondition != nil {
			cond = *w.TargetCondition
		}
		return fmt.Sprintf("card %s (%s)", w.TargetTaskID.String()[:8], cond)
	case domain.TaskWaitMR:
		return fmt.Sprintf("MR %s!%d until %s", *w.ProjectPath, *w.MRIID, *w.Until)
	case domain.TaskWaitPipeline:
		s := fmt.Sprintf("pipeline %s#%d until %s", *w.ProjectPath, *w.PipelineID, *w.Until)
		if len(w.RequiredJobs) > 0 {
			s += " (jobs: " + strings.Join(w.RequiredJobs, ", ") + ")"
		}
		return s
	case domain.TaskWaitTime:
		return "time until " + w.UntilAt.UTC().Format(time.RFC3339)
	}
	return string(w.Kind)
}

func truncateNote(s string) string {
	if len(s) <= domain.MaxOutcomeNote {
		return s
	}
	return s[:domain.MaxOutcomeNote]
}
