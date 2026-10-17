package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func journalSetup(t *testing.T, mode string, tr domain.TransitionRule, mutate func(*domain.Task)) (svc *taskService, rules *MockRulesService, taskID, toID uuid.UUID) {
	t.Helper()
	wf := &domain.WorkflowRulesResponse{WorkflowRulesConfig: domain.WorkflowRulesConfig{
		EnforcementMode: mode,
		Transitions:     map[string]domain.TransitionRule{"review": tr},
	}}
	svc, taskRepo, statusRepo := setupTransitionGateService(wf)
	rules = svc.rulesConfigSvc.(*MockRulesService)
	projectID, fromID, toID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	svc.projectRepo.(*MockProjectRepository).items[projectID] = &domain.Project{ID: projectID, WorkspaceID: testDefaultWorkspaceID}
	statusRepo.items[fromID] = &domain.TaskStatus{ID: fromID, ProjectID: projectID, Category: domain.StatusCategoryReview, Name: "review", Slug: "review"}
	statusRepo.items[toID] = &domain.TaskStatus{ID: toID, ProjectID: projectID, Category: domain.StatusCategoryDone, Name: "done", Slug: "done"}
	task := &domain.Task{ID: taskID, ProjectID: projectID, StatusID: fromID, Title: "t"}
	if mutate != nil {
		mutate(task)
	}
	taskRepo.items[taskID] = task
	return
}

// Advisory violation must land in rule_violation_logs (it never did before).
func TestMoveTask_AdvisoryViolationWritesJournal(t *testing.T) {
	svc, rules, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementAdvisory,
		domain.TransitionRule{Allowed: []string{"in_progress"}}, nil)
	actor := uuid.New()
	ctx := actorctx.WithActor(context.Background(), actor, domain.ActorTypeAgent)
	require.NoError(t, svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toID}))
	require.Len(t, rules.violations, 1)
	v := rules.violations[0]
	assert.Equal(t, "allowed", v.ActionTaken)
	assert.Equal(t, actor, v.ActorID)
	assert.Equal(t, domain.RuleConfigTypeWorkflow, v.RuleType)
	assert.Equal(t, testDefaultWorkspaceID, v.WorkspaceID)
}

func TestMoveTask_StrictViolationWritesJournalAndBlocks(t *testing.T) {
	svc, rules, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict,
		domain.TransitionRule{Allowed: []string{"in_progress"}}, nil)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
	err := svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toID})
	require.Error(t, err)
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "workflow_transition_blocked", ae.Message)
	require.Len(t, rules.violations, 1)
	assert.Equal(t, "blocked", rules.violations[0].ActionTaken)
}

func TestMoveTask_NoViolationNoJournal(t *testing.T) {
	svc, rules, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementAdvisory,
		domain.TransitionRule{Allowed: []string{"done"}}, nil)
	ctx := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
	require.NoError(t, svc.MoveTask(ctx, taskID, MoveTaskInput{StatusID: &toID}))
	assert.Empty(t, rules.violations)
}

func TestTransitionActorConditions(t *testing.T) {
	author, reviewer, assignee, other := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	task := &domain.Task{PreReviewAssigneeID: &author, AssigneeID: &assignee, ReviewerID: &reviewer}
	ag := func(id uuid.UUID) transitionActor { return transitionActor{ID: id, Type: domain.ActorTypeAgent} }

	cases := []struct {
		name    string
		actor   transitionActor
		task    *domain.Task
		allowed []string
		want    bool
	}{
		{"author matches", ag(author), task, []string{"author"}, true},
		{"author rejects other", ag(other), task, []string{"author"}, false},
		{"not_author rejects author", ag(author), task, []string{"not_author"}, false},
		{"not_author accepts other", ag(other), task, []string{"not_author"}, true},
		{"reviewer matches", ag(reviewer), task, []string{"reviewer"}, true},
		{"reviewer rejects other", ag(other), task, []string{"reviewer"}, false},
		{"assignee matches", ag(assignee), task, []string{"assignee"}, true},
		{"reviewer or user: agent reviewer", ag(reviewer), task, []string{"user", "reviewer"}, true},
		{"type pattern still works", ag(other), task, []string{"agent"}, true},
		{"author falls back to assignee", ag(assignee), &domain.Task{AssigneeID: &assignee}, []string{"author"}, true},
		{"not_author with unknown author", ag(other), &domain.Task{}, []string{"not_author"}, true},
		{"nil task: conditions never match", ag(author), nil, []string{"author", "reviewer"}, false},
		{"nil actor id: conditions never match", ag(uuid.Nil), task, []string{"not_author"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, isTransitionActorAllowed(c.actor, c.task, c.allowed))
		})
	}
}

// review→done for a non-author reviewer, blocked for the author — in strict, end to end.
func TestMoveTask_StrictReviewDoneNotAuthor(t *testing.T) {
	author := uuid.New()
	tr := domain.TransitionRule{AllowedActors: []string{"not_author"}}
	set := func(tk *domain.Task) { tk.PreReviewAssigneeID = &author }

	svc, _, taskID, toID := journalSetup(t, domain.RuleConfigEnforcementStrict, tr, set)
	err := svc.MoveTask(actorctx.WithActor(context.Background(), author, domain.ActorTypeAgent), taskID, MoveTaskInput{StatusID: &toID})
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, "workflow_transition_blocked", ae.Message)

	svc, _, taskID, toID = journalSetup(t, domain.RuleConfigEnforcementStrict, tr, set)
	require.NoError(t, svc.MoveTask(actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent), taskID, MoveTaskInput{StatusID: &toID}))
}
