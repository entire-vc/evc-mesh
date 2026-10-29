package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The store is the transactional writer; its SQL is covered by the postgres
// DB tests. Here it records what the service asked it to commit.
type turnStoreStub struct {
	project, workspace uuid.UUID
	scopeErr           error
	agents             map[string]uuid.UUID
	resolveErr         error
	commitErr          error
	committed          []*domain.TurnCommand
	notes              []string
}

func (s *turnStoreStub) CommitTurn(_ context.Context, cmd *domain.TurnCommand) (*domain.TurnCommandResult, error) {
	if s.commitErr != nil {
		return nil, s.commitErr
	}
	s.committed = append(s.committed, cmd)
	return &domain.TurnCommandResult{Released: cmd.Kind != domain.TurnOutcomeNext}, nil
}

func (s *turnStoreStub) AddNote(_ context.Context, _, _ uuid.UUID, _ domain.ActorType, body string) (uuid.UUID, error) {
	s.notes = append(s.notes, body)
	return uuid.New(), nil
}

func (s *turnStoreStub) TaskScope(context.Context, uuid.UUID) (projectID, workspaceID uuid.UUID, err error) {
	return s.project, s.workspace, s.scopeErr
}

func (s *turnStoreStub) ResolveAgent(_ context.Context, _ uuid.UUID, to string) (id uuid.UUID, name string, found bool, err error) {
	if s.resolveErr != nil {
		return uuid.Nil, "", false, s.resolveErr
	}
	id, found = s.agents[to]
	return id, to, found, nil
}

type turnLinksStub struct {
	byID    map[uuid.UUID]*domain.VCSLink
	byTask  []domain.VCSLink
	listErr error
}

func (l *turnLinksStub) GetByID(_ context.Context, id uuid.UUID) (*domain.VCSLink, error) {
	return l.byID[id], nil
}

func (l *turnLinksStub) ListByTask(context.Context, uuid.UUID) ([]domain.VCSLink, error) {
	return l.byTask, l.listErr
}

type turnHooksStub struct {
	checkErr error
	after    []domain.TurnOutcomeKind
}

func (h *turnHooksStub) CheckHandoffAssignee(context.Context, uuid.UUID, uuid.UUID) error {
	return h.checkErr
}

func (h *turnHooksStub) AfterTurn(_ context.Context, _ uuid.UUID, kind domain.TurnOutcomeKind) {
	h.after = append(h.after, kind)
}

// gitLabStub is the GitLab API boundary: "path!iid" → MR state, "path#id" → pipeline status.
type gitLabStub struct {
	mrs       map[string]string
	pipelines map[string]string
}

func (g gitLabStub) GetMergeRequestState(_ context.Context, path string, iid int) (gitlabapi.MergeRequestState, error) {
	st, ok := g.mrs[fmt.Sprintf("%s!%d", path, iid)]
	if !ok {
		return gitlabapi.MergeRequestState{}, errors.New("404")
	}
	return gitlabapi.MergeRequestState{Merged: st == "merged", State: st}, nil
}

func (g gitLabStub) GetPipelineStatus(_ context.Context, path string, id int) (string, error) {
	st, ok := g.pipelines[fmt.Sprintf("%s#%d", path, id)]
	if !ok {
		return "", errors.New("404")
	}
	return st, nil
}

// mrOnlyChecker has no pipeline API.
type mrOnlyChecker struct{}

func (mrOnlyChecker) GetMergeRequestState(context.Context, string, int) (gitlabapi.MergeRequestState, error) {
	return gitlabapi.MergeRequestState{State: "opened"}, nil
}

type turnEnv struct {
	svc   *TurnCommandService
	store *turnStoreStub
	links *turnLinksStub
	hooks *turnHooksStub
	task  uuid.UUID
	agent uuid.UUID
	ctx   context.Context
}

func newTurnEnv(gl GitLabCheckerSource) *turnEnv {
	e := &turnEnv{task: uuid.New(), agent: uuid.New()}
	e.store = &turnStoreStub{project: uuid.New(), workspace: uuid.New(), agents: map[string]uuid.UUID{}}
	e.links = &turnLinksStub{byID: map[uuid.UUID]*domain.VCSLink{}}
	e.hooks = &turnHooksStub{}
	e.svc = NewTurnCommandService(e.store, e.links, e.hooks, gl)
	e.ctx = actorctx.WithActor(context.Background(), e.agent, domain.ActorTypeAgent)
	return e
}

func liveGitLab() GitLabCheckerSource {
	g := gitLabStub{
		mrs:       map[string]string{"g/p!10": "opened", "g/p!11": "merged", "g/p!12": "closed"},
		pipelines: map[string]string{"g/p#500": "running", "g/p#501": "success"},
	}
	return func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) { return g, true }
}

// isAPI reports whether err is an API error with this status.
func isAPI(status int, err error) bool {
	var ae *apierror.Error
	return errors.As(err, &ae) && ae.Code == status
}

func outcomeCode(t *testing.T, err error) string {
	t.Helper()
	var oe *domain.OutcomeError
	require.ErrorAs(t, err, &oe, "want an OutcomeError, got %v", err)
	return oe.Code
}

func TestTurnCommandService_WaitForTargets(t *testing.T) {
	mr := func(iid int, until string) WaitForInput {
		return WaitForInput{On: "mr", Provider: ptr("gitlab"), ProjectPath: ptr("g/p"), MRIID: ptr(iid), Until: ptr(until)}
	}
	pipe := func(id int64) WaitForInput {
		return WaitForInput{On: "pipeline", ProjectPath: ptr("g/p"), PipelineID: ptr(id), Until: ptr("finished")}
	}
	refused := []struct {
		name string
		gl   GitLabCheckerSource
		in   WaitForInput
		code string
	}{
		{"unknown kind", liveGitLab(), WaitForInput{On: "decision"}, domain.CodeInvalidTarget},
		{"card without target", liveGitLab(), WaitForInput{On: "card"}, domain.CodeInvalidTarget},
		{"MR not on GitLab", liveGitLab(), mr(99, "merged"), domain.CodeInvalidTarget},
		{"MR merged", liveGitLab(), mr(11, "merged"), domain.CodeAlreadyMet},
		{"MR closed", liveGitLab(), mr(12, "merged"), domain.CodeAlreadyMet},
		{"MR without GitLab or link", nil, mr(10, "merged"), domain.CodeInvalidTarget},
		{"pipeline without GitLab", nil, pipe(500), domain.CodeInvalidTarget},
		{"pipeline, client has no pipeline API", func(context.Context, uuid.UUID) (gitlabapi.MergeRequestChecker, bool) {
			return mrOnlyChecker{}, true
		}, pipe(500), domain.CodeInvalidTarget},
		{"pipeline not on GitLab", liveGitLab(), pipe(999), domain.CodeInvalidTarget},
		{"pipeline finished", liveGitLab(), pipe(501), domain.CodeAlreadyMet},
		{"time too soon", liveGitLab(), WaitForInput{On: "time", UntilAt: ptr(time.Now().Add(time.Minute))}, domain.CodeTimeOutOfBounds},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			e := newTurnEnv(tc.gl)
			_, err := e.svc.WaitFor(e.ctx, e.task, tc.in)
			assert.Equal(t, tc.code, outcomeCode(t, err))
			assert.Empty(t, e.store.committed, "a refused wait reached the store")
			assert.Empty(t, e.hooks.after)
		})
	}

	accepted := []struct {
		name, comment string
		in            WaitForInput
	}{
		{"open MR", "Waiting on MR g/p!10 until merged.", mr(10, "merged")},
		{"running pipeline with jobs", "Waiting on pipeline g/p#500 until finished (jobs: test, lint).",
			WaitForInput{On: "pipeline", ProjectPath: ptr("g/p"), PipelineID: ptr(int64(500)), Until: ptr("finished"), RequiredJobs: []string{"test", "lint"}}},
		{"time with a note", "Waiting on time until", WaitForInput{On: "time", UntilAt: ptr(time.Now().Add(time.Hour)), Note: "  window  ",
			EventRef: ptr("nightly-export"), Source: ptr("https://ci.example/schedules/1"), Why: ptr("the export job starts at this time")}},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			e := newTurnEnv(liveGitLab())
			res, err := e.svc.WaitFor(e.ctx, e.task, tc.in)
			require.NoError(t, err)
			assert.True(t, res.Released)
			require.Len(t, e.store.committed, 1)
			cmd := e.store.committed[0]
			assert.Equal(t, domain.TurnOutcomeWait, cmd.Kind)
			assert.True(t, strings.HasPrefix(cmd.CommentBody, tc.comment), cmd.CommentBody)
			assert.NotContains(t, cmd.CommentBody, "⏳", "the server comment must not look like a legacy marker")
			assert.Equal(t, []domain.TurnOutcomeKind{domain.TurnOutcomeWait}, e.hooks.after)
		})
	}

	t.Run("card wait names the card and condition", func(t *testing.T) {
		e := newTurnEnv(nil)
		other := uuid.New()
		_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "card", TargetTaskID: &other})
		require.NoError(t, err)
		assert.Equal(t, "Waiting on card "+other.String()[:8]+" (task_done).", e.store.committed[0].CommentBody)
		_, err = e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "card", TargetTaskID: &other, TargetCondition: ptr("shipped"), Note: "deploy first"})
		require.NoError(t, err)
		assert.Equal(t, "Waiting on card "+other.String()[:8]+" (shipped).\n\ndeploy first", e.store.committed[1].CommentBody)
	})
}

func TestTurnCommandService_WaitForMRLinks(t *testing.T) {
	gitlabURL := "https://git.example.com/g/p/-/merge_requests/10"
	t.Run("vcs_link_id of this card fills the coordinates", func(t *testing.T) {
		e := newTurnEnv(liveGitLab())
		link := &domain.VCSLink{ID: uuid.New(), TaskID: e.task, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, URL: gitlabURL}
		e.links.byID[link.ID] = link
		_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "mr", VCSLinkID: &link.ID, Until: ptr("merged")})
		require.NoError(t, err)
		w := e.store.committed[0].Wait
		assert.Equal(t, "g/p", *w.ProjectPath)
		assert.Equal(t, 10, *w.MRIID)
	})

	bad := []struct {
		name string
		link func(task uuid.UUID) *domain.VCSLink
	}{
		{"missing link", func(uuid.UUID) *domain.VCSLink { return nil }},
		{"link of another card", func(uuid.UUID) *domain.VCSLink {
			return &domain.VCSLink{TaskID: uuid.New(), Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, URL: gitlabURL}
		}},
		{"not a pr link", func(task uuid.UUID) *domain.VCSLink {
			return &domain.VCSLink{TaskID: task, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypeCommit, URL: gitlabURL}
		}},
		{"URL names no MR", func(task uuid.UUID) *domain.VCSLink {
			return &domain.VCSLink{TaskID: task, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, URL: "https://git.example.com/g/p"}
		}},
		{"not GitLab", func(task uuid.UUID) *domain.VCSLink {
			return &domain.VCSLink{TaskID: task, Provider: domain.VCSProviderGitHub, LinkType: domain.VCSLinkTypePR, URL: "https://github.com/g/p/pull/10"}
		}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			e := newTurnEnv(liveGitLab())
			id := uuid.New()
			if l := tc.link(e.task); l != nil {
				e.links.byID[id] = l
			}
			_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "mr", VCSLinkID: &id, Until: ptr("merged")})
			assert.Equal(t, domain.CodeInvalidTarget, outcomeCode(t, err))
			assert.Empty(t, e.store.committed)
		})
	}

	t.Run("no links source", func(t *testing.T) {
		e := newTurnEnv(liveGitLab())
		e.svc.links = nil
		id := uuid.New()
		_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "mr", VCSLinkID: &id, Until: ptr("merged")})
		assert.Equal(t, domain.CodeInvalidTarget, outcomeCode(t, err))
	})

	// Without GitLab the card's own link is the evidence.
	for _, tc := range []struct {
		status domain.VCSLinkStatus
		code   string
	}{{domain.VCSLinkStatusOpen, ""}, {domain.VCSLinkStatusMerged, domain.CodeAlreadyMet}, {domain.VCSLinkStatusClosed, domain.CodeAlreadyMet}} {
		t.Run("fallback to link "+string(tc.status), func(t *testing.T) {
			e := newTurnEnv(nil)
			link := domain.VCSLink{ID: uuid.New(), TaskID: e.task, Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, URL: gitlabURL, Status: tc.status}
			e.links.byTask = []domain.VCSLink{{Provider: domain.VCSProviderGitLab, LinkType: domain.VCSLinkTypePR, URL: "https://git.example.com/g/p/-/merge_requests/3"}, link}
			_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "mr", Provider: ptr("gitlab"), ProjectPath: ptr("g/p"), MRIID: ptr(10), Until: ptr("merged")})
			if tc.code != "" {
				assert.Equal(t, tc.code, outcomeCode(t, err))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, link.ID, *e.store.committed[0].Wait.VCSLinkID, "the matching link is recorded on the wait")
		})
	}

	t.Run("listing links fails", func(t *testing.T) {
		e := newTurnEnv(nil)
		e.links.listErr = errors.New("db down")
		_, err := e.svc.WaitFor(e.ctx, e.task, WaitForInput{On: "mr", Provider: ptr("gitlab"), ProjectPath: ptr("g/p"), MRIID: ptr(10), Until: ptr("merged")})
		assert.EqualError(t, err, "db down")
	})
}

func TestTurnCommandService_CallerAndStoreErrors(t *testing.T) {
	e := newTurnEnv(liveGitLab())
	user := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	other := uuid.New()
	card := WaitForInput{On: "card", TargetTaskID: &other}

	_, err := e.svc.WaitFor(user, e.task, card)
	assert.True(t, isAPI(http.StatusForbidden, err), "a person does not end an agent turn: %v", err)
	_, err = e.svc.ContinueNext(user, e.task, TurnCommon{}, "")
	assert.True(t, isAPI(http.StatusForbidden, err))
	_, err = e.svc.HandOff(user, e.task, TurnCommon{}, "b", "why")
	assert.True(t, isAPI(http.StatusForbidden, err))

	long := TurnCommon{SessionID: ptr(strings.Repeat("s", domain.MaxSessionIDLen+1))}
	for _, call := range []func() error{
		func() error {
			_, werr := e.svc.WaitFor(e.ctx, e.task, WaitForInput{TurnCommon: long, On: "card", TargetTaskID: &other})
			return werr
		},
		func() error { _, cerr := e.svc.ContinueNext(e.ctx, e.task, long, ""); return cerr },
		func() error { _, herr := e.svc.HandOff(e.ctx, e.task, long, "b", "why"); return herr },
	} {
		assert.True(t, isAPI(apierror.ValidationError(nil).Code, call()), "an over-long session_id is refused")
	}

	e.store.scopeErr = apierror.NotFound("Task")
	_, err = e.svc.WaitFor(e.ctx, e.task, card)
	assert.True(t, isAPI(http.StatusNotFound, err))
	_, err = e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "b", "why")
	assert.True(t, isAPI(http.StatusNotFound, err))
	e.store.scopeErr = nil

	e.store.commitErr = apierror.Conflict("the caller does not hold this card's checkout")
	e.store.agents["b"] = uuid.New()
	_, err = e.svc.WaitFor(e.ctx, e.task, card)
	assert.True(t, isAPI(http.StatusConflict, err))
	_, err = e.svc.ContinueNext(e.ctx, e.task, TurnCommon{}, "")
	assert.True(t, isAPI(http.StatusConflict, err))
	_, err = e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "b", "why")
	assert.True(t, isAPI(http.StatusConflict, err))
	assert.Empty(t, e.hooks.after, "nothing is announced for a command that did not commit")
}

func TestTurnCommandService_ContinueNext(t *testing.T) {
	e := newTurnEnv(nil)
	res, err := e.svc.ContinueNext(e.ctx, e.task, TurnCommon{SessionID: ptr("s1")}, "")
	require.NoError(t, err)
	assert.False(t, res.Released)
	assert.Equal(t, "Continuing with the next step.", e.store.committed[0].CommentBody)

	_, err = e.svc.ContinueNext(e.ctx, e.task, TurnCommon{}, "  tests next  ")
	require.NoError(t, err)
	assert.Equal(t, "Continuing: tests next", e.store.committed[1].CommentBody)
	assert.Equal(t, "tests next", e.store.committed[1].Note)

	_, err = e.svc.ContinueNext(e.ctx, e.task, TurnCommon{}, strings.Repeat("n", domain.MaxOutcomeNote+1))
	assert.True(t, isAPI(apierror.ValidationError(nil).Code, err))
	assert.Len(t, e.store.committed, 2)
	assert.Equal(t, []domain.TurnOutcomeKind{domain.TurnOutcomeNext, domain.TurnOutcomeNext}, e.hooks.after)
}

func TestTurnCommandService_HandOff(t *testing.T) {
	e := newTurnEnv(nil)
	b := uuid.New()
	e.store.agents["Brook"] = b
	e.store.agents["me"] = e.agent

	_, err := e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "Brook", "   ")
	assert.True(t, isAPI(apierror.ValidationError(nil).Code, err), "why is required")
	_, err = e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "nobody", "needs a frontend lane")
	assert.Equal(t, domain.CodeNotAnAgent, outcomeCode(t, err))
	_, err = e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "me", "needs a frontend lane")
	assert.Equal(t, domain.CodeNotAnAgent, outcomeCode(t, err), "no hand-off to oneself")

	e.store.resolveErr = errors.New("db down")
	_, err = e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "Brook", "needs a frontend lane")
	assert.EqualError(t, err, "db down")
	e.store.resolveErr = nil

	e.hooks.checkErr = apierror.Forbidden("the agent accepts no assignments")
	_, err = e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "Brook", "needs a frontend lane")
	assert.True(t, isAPI(http.StatusForbidden, err), "assign_task's assignee checks apply")
	assert.Empty(t, e.store.committed)
	e.hooks.checkErr = nil

	why := strings.Repeat("w", domain.MaxQuestionLen)
	res, err := e.svc.HandOff(e.ctx, e.task, TurnCommon{}, " Brook ", why)
	require.NoError(t, err)
	assert.True(t, res.Released)
	cmd := e.store.committed[0]
	assert.Equal(t, b, *cmd.HandoffTo)
	assert.Equal(t, "Handed over to Brook.\n\n"+why, cmd.CommentBody)
	assert.Equal(t, why, cmd.Note)
	_, err = e.svc.HandOff(e.ctx, e.task, TurnCommon{}, "Brook", why+"w")
	assert.True(t, isAPI(apierror.ValidationError(nil).Code, err), "why is at most 4000 characters")
	assert.Equal(t, []domain.TurnOutcomeKind{domain.TurnOutcomeHandoff}, e.hooks.after)
}

func TestTurnCommandService_AddNote(t *testing.T) {
	e := newTurnEnv(nil)
	system := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeSystem)
	_, err := e.svc.AddNote(system, e.task, "x")
	assert.True(t, isAPI(http.StatusForbidden, err))
	_, err = e.svc.AddNote(e.ctx, e.task, "  ")
	assert.True(t, isAPI(apierror.ValidationError(nil).Code, err))

	user := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	id, err := e.svc.AddNote(user, e.task, " a person's note ")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)
	assert.Equal(t, []string{"a person's note"}, e.store.notes)
	assert.Empty(t, e.store.committed, "a note is not a turn outcome")
}

func TestTurnHooksFor(t *testing.T) {
	assert.Panics(t, func() { TurnHooksFor(nil) })

	repo := NewMockTaskRepository()
	agent := uuid.New()
	task := &domain.Task{ID: uuid.New(), ProjectID: uuid.New(), AssigneeID: &agent, AssigneeType: domain.AssigneeTypeAgent}
	repo.items[task.ID] = task
	hooks := TurnHooksFor(&taskService{taskRepo: repo})
	ctx := actorctx.WithActor(context.Background(), agent, domain.ActorTypeAgent)

	// Every effect is optional on a bare service; the point is it neither
	// panics nor fails on a missing task.
	hooks.AfterTurn(ctx, task.ID, domain.TurnOutcomeHandoff)
	hooks.AfterTurn(ctx, task.ID, domain.TurnOutcomeNext)
	hooks.AfterTurn(ctx, uuid.New(), domain.TurnOutcomeWait)

	var unresolved *AssigneeUnresolvedError
	err := (&taskService{}).ensureAssigneeProjectMember(ctx, task.ProjectID, &agent, domain.AssigneeType("bot"))
	require.ErrorAs(t, err, &unresolved, "control: the shared check refuses an untyped principal")
	assert.NoError(t, hooks.CheckHandoffAssignee(context.Background(), task.ProjectID, uuid.Nil), "a nil principal is a no-op")
}
