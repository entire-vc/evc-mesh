package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Task #8f191cf7 (rule 3, #994e02f4 / #d81f8b30): a card the executor filed for
// itself, moved to review with no set_reviewer configured, used to sit in review
// on the executor — a review nobody was asked to do. It goes to the project lead.
func TestMoveToReview_SelfCreatedCardGoesToLead(t *testing.T) {
	cases := []struct {
		name       string
		createdBy  func(builder uuid.UUID) uuid.UUID
		leadIsSelf bool
		wantLead   bool
	}{
		{"self-created, lead differs", func(b uuid.UUID) uuid.UUID { return b }, false, true},
		{"created by someone else keeps the builder", func(uuid.UUID) uuid.UUID { return uuid.New() }, false, false},
		{"self-created but the builder IS the lead", func(b uuid.UUID) uuid.UUID { return b }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := &domain.WorkflowRulesResponse{WorkflowRulesConfig: domain.WorkflowRulesConfig{
				Transitions: map[string]domain.TransitionRule{"in_progress": {Allowed: []string{"review"}}},
			}}
			rules := &domain.EffectiveAssignmentRules{
				DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "garfield", Source: "project"},
			}
			svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(wf, rules)

			projectID, workspaceID := uuid.New(), testDefaultWorkspaceID
			builderID, leadID := uuid.New(), uuid.New()
			oldID, newID, taskID := uuid.New(), uuid.New(), uuid.New()
			statusRepo.items[oldID] = &domain.TaskStatus{ID: oldID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress"}
			statusRepo.items[newID] = &domain.TaskStatus{ID: newID, ProjectID: projectID, Category: domain.StatusCategoryReview, Name: "review"}
			projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
			agentRepo.items[builderID] = &domain.Agent{ID: builderID, WorkspaceID: workspaceID, Slug: "builder"}
			agentRepo.items[leadID] = &domain.Agent{ID: leadID, WorkspaceID: workspaceID, Slug: "garfield"}
			if tc.leadIsSelf {
				rules.DefaultAssignee.Value = "builder"
			}
			taskRepo.items[taskID] = &domain.Task{
				ID: taskID, ProjectID: projectID, StatusID: oldID,
				AssigneeID: &builderID, AssigneeType: domain.AssigneeTypeAgent,
				CreatedBy: tc.createdBy(builderID), CreatedByType: domain.ActorTypeAgent,
			}

			require.NoError(t, svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &newID}))

			got := taskRepo.items[taskID].AssigneeID
			require.NotNil(t, got)
			if tc.wantLead {
				assert.Equal(t, leadID, *got, "self-created card must go to the lead")
			} else {
				assert.Equal(t, builderID, *got)
			}
		})
	}
}

// A failed workflow lookup says nothing about whether a reviewer is configured, so
// the lead fallback must not fire on it.
func TestMoveToReview_WorkflowLookupErrorSkipsLeadFallback(t *testing.T) {
	rules := &domain.EffectiveAssignmentRules{
		DefaultAssignee: &domain.EffectiveAssignmentRule{Value: "garfield", Source: "project"},
	}
	svc, taskRepo, statusRepo, agentRepo, projRepo := setupTaskServiceWithWorkflow(nil, rules)
	svc.rulesConfigSvc.(*MockRulesService).workflowErr = errors.New("db down")

	projectID, workspaceID := uuid.New(), testDefaultWorkspaceID
	builderID, leadID := uuid.New(), uuid.New()
	oldID, newID, taskID := uuid.New(), uuid.New(), uuid.New()
	statusRepo.items[oldID] = &domain.TaskStatus{ID: oldID, ProjectID: projectID, Category: domain.StatusCategoryInProgress, Name: "in_progress"}
	statusRepo.items[newID] = &domain.TaskStatus{ID: newID, ProjectID: projectID, Category: domain.StatusCategoryReview, Name: "review"}
	projRepo.items[projectID] = &domain.Project{ID: projectID, WorkspaceID: workspaceID}
	agentRepo.items[builderID] = &domain.Agent{ID: builderID, WorkspaceID: workspaceID, Slug: "builder"}
	agentRepo.items[leadID] = &domain.Agent{ID: leadID, WorkspaceID: workspaceID, Slug: "garfield"}
	taskRepo.items[taskID] = &domain.Task{
		ID: taskID, ProjectID: projectID, StatusID: oldID,
		AssigneeID: &builderID, AssigneeType: domain.AssigneeTypeAgent,
		CreatedBy: builderID, CreatedByType: domain.ActorTypeAgent,
	}

	require.NoError(t, svc.MoveTask(context.Background(), taskID, MoveTaskInput{StatusID: &newID}))
	require.NotNil(t, taskRepo.items[taskID].AssigneeID)
	assert.Equal(t, builderID, *taskRepo.items[taskID].AssigneeID)
}
