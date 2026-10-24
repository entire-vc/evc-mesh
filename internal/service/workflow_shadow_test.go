package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func TestRepoFromVCSURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://gitlab.example.invalid/entire-vc/evc-mesh/-/merge_requests/12", "entire-vc/evc-mesh"},
		{"https://gitlab.example.invalid/a/b/c/-/merge_requests/3", "a/b/c"},
		{"https://github.com/Entire-VC/Repo/pull/7", "entire-vc/repo"},
		{"https://example.com/nothing", ""},
	}
	for _, c := range cases {
		in, want := c.in, c.want
		if got := RepoFromVCSURL(in); got != want {
			t.Errorf("RepoFromVCSURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestModelFamily(t *testing.T) {
	for in, want := range map[string]string{"claude-sonnet-5-5": "anthropic", "gpt-6.1-sol": "openai", "gemini-3": "google", "": "", "mystery": ""} {
		if got := ModelFamily(in); got != want {
			t.Errorf("ModelFamily(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDecideShadowReview(t *testing.T) {
	rule := domain.OnEnterAction{Reviewer: "by_repo", ReviewerPick: "other_family", AcceptorLabel: "verify", MergeBy: "author"}
	author, claudeB, codexA, codexNoRepo := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := "entire-vc/evc-mesh"
	cands := []ShadowReviewCandidate{
		{AgentID: author, Slug: "linus", Family: "anthropic", Repos: []string{repo}},
		{AgentID: claudeB, Slug: "bob", Family: "anthropic", Repos: []string{repo}},
		{AgentID: codexNoRepo, Slug: "cora", Family: "openai", Repos: []string{"other/x"}},
		{AgentID: codexA, Slug: "charles", Family: "openai", Repos: []string{"ENTIRE-VC/evc-mesh"}},
	}

	d := DecideShadowReview(rule, &author, "anthropic", repo, cands)
	if d.PickedID == nil || *d.PickedID != codexA {
		t.Fatalf("want charles (other family, covers repo), got %+v", d)
	}
	if d.AcceptorLabel != "verify" || d.MergeBy != "author" {
		t.Errorf("route fields lost: %+v", d)
	}

	// known-broken inputs must NOT pick anybody
	if d := DecideShadowReview(rule, &author, "anthropic", "", cands); d.PickedID != nil {
		t.Errorf("no repo must not pick: %+v", d)
	}
	if d := DecideShadowReview(rule, &author, "", repo, cands); d.PickedID != nil {
		t.Errorf("unknown author family must not pick: %+v", d)
	}
	if d := DecideShadowReview(rule, &author, "anthropic", repo, cands[:3]); d.PickedID != nil {
		t.Errorf("only same-family/no-repo candidates must not pick: %+v", d)
	}
	// author is never their own reviewer even when the family rule is off
	solo := domain.OnEnterAction{Reviewer: "by_repo"}
	if d := DecideShadowReview(solo, &author, "anthropic", repo, cands[:1]); d.PickedID != nil {
		t.Errorf("author picked as own reviewer: %+v", d)
	}
}

type shadowLinks struct {
	repository.VCSLinkRepository
	rows []repository.MergedPROpenTask
}

func (l shadowLinks) ListOpenTasksWithMergedPR(_ context.Context, limit, offset int) ([]repository.MergedPROpenTask, error) {
	if offset >= len(l.rows) {
		return nil, nil
	}
	end := offset + limit
	if end > len(l.rows) {
		end = len(l.rows)
	}
	return l.rows[offset:end], nil
}

type shadowRules struct{ on, lift map[uuid.UUID]bool }

func (r shadowRules) GetProjectWorkflowRules(_ context.Context, id uuid.UUID, _ *uuid.UUID) (*domain.WorkflowRulesResponse, error) {
	resp := &domain.WorkflowRulesResponse{}
	resp.MidPipeline = &domain.MidPipelineConfig{CloseOnMerge: r.on[id], LiftBacklogBlockers: r.lift[id]}
	return resp, nil
}

type shadowProjects struct {
	repository.ProjectRepository
	ws uuid.UUID
}

func (p shadowProjects) GetByID(_ context.Context, id uuid.UUID) (*domain.Project, error) {
	return &domain.Project{ID: id, WorkspaceID: p.ws}, nil
}

type shadowActivity struct {
	repository.ActivityLogRepository
	got []*domain.ActivityLog
}

func (a *shadowActivity) Create(_ context.Context, e *domain.ActivityLog) error {
	a.got = append(a.got, e)
	return nil
}

func TestCloseOnMergeShadowSweep_OnlyEnabledProjects(t *testing.T) {
	on, off := uuid.New(), uuid.New()
	rows := []repository.MergedPROpenTask{
		{TaskID: uuid.New(), ProjectID: on, LinkURL: "u1"},
		{TaskID: uuid.New(), ProjectID: off, LinkURL: "u2"},
		{TaskID: uuid.New(), ProjectID: on, LinkURL: "u3"},
	}
	act := &shadowActivity{}
	svc := NewCloseOnMergeShadowService(shadowLinks{rows: rows}, act, shadowProjects{ws: uuid.New()}, shadowRules{on: map[uuid.UUID]bool{on: true}})
	n, err := svc.Sweep(context.Background())
	if err != nil || n != 2 || len(act.got) != 2 {
		t.Fatalf("n=%d err=%v entries=%d, want 2 journal entries for the enabled project only", n, err, len(act.got))
	}
	for _, e := range act.got {
		if e.Action != "workflow.shadow_close_on_merge" {
			t.Errorf("action %q", e.Action)
		}
	}
}

func TestCloseOnMergeShadowSweep_PagesPastDisabledProjects(t *testing.T) {
	on, off := uuid.New(), uuid.New()
	var rows []repository.MergedPROpenTask
	for i := 0; i < closeOnMergeShadowBatch+5; i++ { // a full first page of disabled cards
		rows = append(rows, repository.MergedPROpenTask{TaskID: uuid.New(), ProjectID: off})
	}
	rows = append(rows, repository.MergedPROpenTask{TaskID: uuid.New(), ProjectID: on})
	act := &shadowActivity{}
	svc := NewCloseOnMergeShadowService(shadowLinks{rows: rows}, act, shadowProjects{ws: uuid.New()}, shadowRules{on: map[uuid.UUID]bool{on: true}})
	n, err := svc.Sweep(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, enabled project behind a full page of disabled ones must still be journaled", n, err)
	}
}

func shadowRouteEntries(env *onEnterAssignEnv, id uuid.UUID) int {
	n := 0
	for _, e := range env.activityRepo.items {
		if e.EntityID == id && e.Action == "workflow.shadow_review_route" {
			n++
		}
	}
	return n
}

func TestShadowReviewRoute_JournaledEvenWithExplicitAssignee(t *testing.T) {
	rule := map[string]domain.OnEnterAction{"qualified": {Reviewer: "by_repo", ReviewerPick: "other_family", AcceptorLabel: "verify", MergeBy: "author"}}
	env := setupOnEnterAssignEnv(t, rule)
	id := env.seedTask(env.ownerID)
	require.NoError(t, env.svc.MoveTask(context.Background(), id, MoveTaskInput{StatusID: &env.qualifiedID, AssigneeID: &env.targetID}))
	if got := shadowRouteEntries(env, id); got != 1 {
		t.Fatalf("explicit-assignee move journaled %d shadow entries, want 1", got)
	}
	if *env.tasks.items[id].AssigneeID != env.targetID {
		t.Errorf("shadow mode must not alter the explicit assignment")
	}

	plain := setupOnEnterAssignEnv(t, nil)
	pid := plain.seedTask(plain.ownerID)
	plain.move(t, pid)
	if got := shadowRouteEntries(plain, pid); got != 0 {
		t.Errorf("no rule configured but %d shadow entries written", got)
	}
}

func TestShadowReviewRoute_AuthorIsPreMoveAssignee(t *testing.T) {
	rule := map[string]domain.OnEnterAction{"qualified": {Reviewer: "by_repo", ReviewerPick: "other_family", AcceptorLabel: "verify", MergeBy: "author"}}
	env := setupOnEnterAssignEnv(t, rule)
	agents := env.svc.agentRepo.(*MockAgentRepository)
	claude, gpt := "claude-sonnet-5-5", "gpt-6.1-sol"
	agents.items[env.ownerID].Model = &claude // held the card before review
	agents.items[env.targetID].Model = &gpt   // takes it on the move
	creator := uuid.New()
	agents.items[creator] = &domain.Agent{ID: creator, WorkspaceID: testDefaultWorkspaceID, Slug: "lead", Model: &gpt}
	id := env.seedTask(env.ownerID)
	env.tasks.items[id].CreatedBy = creator
	require.NoError(t, env.svc.MoveTask(context.Background(), id, MoveTaskInput{StatusID: &env.qualifiedID, AssigneeID: &env.targetID}))
	for _, e := range env.activityRepo.items {
		if e.EntityID == id && e.Action == "workflow.shadow_review_route" {
			var ch map[string]interface{}
			require.NoError(t, json.Unmarshal(e.Changes, &ch))
			if ch["author_family"] != "anthropic" {
				t.Fatalf("author_family=%v, want anthropic (pre-move assignee, not created_by or the new assignee)", ch["author_family"])
			}
			return
		}
	}
	t.Fatal("no shadow_review_route entry")
}

type pagedAgents struct {
	repository.AgentRepository
	all []domain.Agent
}

func (p pagedAgents) List(_ context.Context, _ uuid.UUID, _ repository.AgentFilter, pg pagination.Params) (*pagination.Page[domain.Agent], error) {
	lo := (pg.Page - 1) * pg.PageSize
	if lo > len(p.all) {
		lo = len(p.all)
	}
	hi := lo + pg.PageSize
	if hi > len(p.all) {
		hi = len(p.all)
	}
	return &pagination.Page[domain.Agent]{Items: p.all[lo:hi], TotalCount: len(p.all), Page: pg.Page, PageSize: pg.PageSize}, nil
}

func TestShadowCandidates_ReadsPastTheFirstPage(t *testing.T) {
	var all []domain.Agent
	for i := 0; i < pagination.MaxPageSize+30; i++ {
		all = append(all, domain.Agent{ID: uuid.New(), Slug: "a"})
	}
	all[len(all)-1].Slug = "late-reviewer"
	svc := &taskService{agentRepo: pagedAgents{all: all}}
	cands := svc.shadowCandidates(context.Background(), uuid.New())
	if len(cands) != len(all) {
		t.Fatalf("got %d candidates, want %d (agents past the first page were dropped)", len(cands), len(all))
	}
	if cands[len(cands)-1].Slug != "late-reviewer" {
		t.Errorf("last candidate %q", cands[len(cands)-1].Slug)
	}
}

func liftEntries(e *parityEnv, id uuid.UUID) int {
	n := 0
	for _, a := range e.activityRepo.items {
		if a.EntityID == id && a.Action == "workflow.shadow_lift_blocker" {
			n++
		}
	}
	return n
}

func TestLiftBacklogBlockersShadow_JournalsOnlyPromotableCardsOfOptedInProjects(t *testing.T) {
	e := newParityEnv(t)
	rules := shadowRules{lift: map[uuid.UUID]bool{e.projectID: true}}
	e.svc = NewBacklogPromotionAdvisoryService(e.taskRepo, e.statusRepo, e.depRepo, e.activityRepo, e.projectRepo, WithLiftBlockersShadow(rules))

	promotable := e.task(t, nil)
	held := e.task(t, func(x *domain.Task) { x.AssigneeType = domain.AssigneeTypeUser })
	if _, err := e.svc.SweepAdvisory(context.Background()); err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	if got := liftEntries(e, promotable.ID); got != 1 {
		t.Errorf("promotable card journaled %d lift entries, want 1", got)
	}
	if got := liftEntries(e, held.ID); got != 0 {
		t.Errorf("held card journaled %d lift entries, want 0", got)
	}
	if e.taskRepo.items[promotable.ID].StatusID != e.backlog.ID {
		t.Errorf("shadow mode moved the card out of backlog")
	}

	off := newParityEnv(t)
	off.svc = NewBacklogPromotionAdvisoryService(off.taskRepo, off.statusRepo, off.depRepo, off.activityRepo, off.projectRepo, WithLiftBlockersShadow(shadowRules{}))
	p2 := off.task(t, nil)
	if _, err := off.svc.SweepAdvisory(context.Background()); err != nil {
		t.Fatalf("SweepAdvisory: %v", err)
	}
	if got := liftEntries(off, p2.ID); got != 0 {
		t.Errorf("project without the flag journaled %d lift entries", got)
	}
}

// shrinkingLinks models the real query: a card disappears from the result once
// its shadow entry is journaled.
type shrinkingLinks struct {
	repository.VCSLinkRepository
	rows []repository.MergedPROpenTask
	act  *shadowActivity
}

func (l shrinkingLinks) ListOpenTasksWithMergedPR(_ context.Context, limit, offset int) ([]repository.MergedPROpenTask, error) {
	done := make(map[uuid.UUID]bool, len(l.act.got))
	for _, e := range l.act.got {
		done[e.EntityID] = true
	}
	var left []repository.MergedPROpenTask
	for _, r := range l.rows {
		if !done[r.TaskID] {
			left = append(left, r)
		}
	}
	if offset >= len(left) {
		return nil, nil
	}
	end := offset + limit
	if end > len(left) {
		end = len(left)
	}
	return left[offset:end], nil
}

func TestCloseOnMergeShadowSweep_NoCardSkippedWhileResultSetShrinks(t *testing.T) {
	on, off := uuid.New(), uuid.New()
	// one card of a project that is not enabled sits at the head and is never journaled
	rows := []repository.MergedPROpenTask{{TaskID: uuid.New(), ProjectID: off, LinkURL: "head"}}
	for i := 0; i < 450; i++ {
		rows = append(rows, repository.MergedPROpenTask{TaskID: uuid.New(), ProjectID: on, LinkURL: "u"})
	}
	act := &shadowActivity{}
	svc := NewCloseOnMergeShadowService(shrinkingLinks{rows: rows, act: act}, act, shadowProjects{ws: uuid.New()}, shadowRules{on: map[uuid.UUID]bool{on: true}})
	n, err := svc.Sweep(context.Background())
	if err != nil || n != 450 || len(act.got) != 450 {
		t.Fatalf("n=%d err=%v entries=%d, want all 450 enabled cards journaled in one sweep", n, err, len(act.got))
	}
}

func TestRedactPRURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://git.example.com/g/p/-/merge_requests/7":                                   "https://git.example.com/g/p/-/merge_requests/7",
		"https://oauth2:s3cret@git.example.com/g/p/-/merge_requests/7?private_token=abc#n": "https://git.example.com/g/p/-/merge_requests/7",
		"not a url": "[unparseable url]",
	} {
		if got := redactPRURL(in); got != want {
			t.Errorf("redactPRURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCloseOnMergeShadow_JournaledURLCarriesNoCredentials(t *testing.T) {
	on := uuid.New()
	rows := []repository.MergedPROpenTask{{TaskID: uuid.New(), ProjectID: on, LinkURL: "https://u:pw@git.example.com/g/p/-/merge_requests/1?token=zzz"}}
	act := &shadowActivity{}
	svc := NewCloseOnMergeShadowService(shadowLinks{rows: rows}, act, shadowProjects{ws: uuid.New()}, shadowRules{on: map[uuid.UUID]bool{on: true}})
	if _, err := svc.Sweep(context.Background()); err != nil || len(act.got) != 1 {
		t.Fatalf("err=%v entries=%d", err, len(act.got))
	}
	body := string(act.got[0].Changes)
	for _, leak := range []string{"pw", "zzz", "token"} {
		if strings.Contains(body, leak) {
			t.Errorf("journal entry leaks %q: %s", leak, body)
		}
	}
}
