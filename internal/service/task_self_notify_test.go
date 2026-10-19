package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// Nobody needs a notification about what they themselves just did. This pins
// notifyReviewer, which used to have no actor check at all.

func selfNotifyFixture(t *testing.T) (*taskService, *MockNotificationService, *domain.Task) {
	t.Helper()
	projRepo := NewMockProjectRepository()
	proj := &domain.Project{ID: uuid.New(), WorkspaceID: uuid.New()}
	require.NoError(t, projRepo.Create(context.Background(), proj))
	ns := NewMockNotificationService()
	svc := &taskService{projectRepo: projRepo, notifySvc: ns}

	assignee, reviewer, creator := uuid.New(), uuid.New(), uuid.New()
	rt := domain.AssigneeTypeUser
	task := &domain.Task{
		ID: uuid.New(), ProjectID: proj.ID, Title: "t",
		AssigneeID: &assignee, AssigneeType: domain.AssigneeTypeUser,
		ReviewerID: &reviewer, ReviewerType: &rt,
		CreatedBy: creator, CreatedByType: domain.ActorTypeUser,
	}
	return svc, ns, task
}

func asUser(id uuid.UUID) context.Context {
	return actorctx.WithActor(context.Background(), id, domain.ActorTypeUser)
}

func TestNotifyReviewer_ActorIsReviewer_Silent(t *testing.T) {
	svc, ns, task := selfNotifyFixture(t)
	svc.notifyReviewer(asUser(*task.ReviewerID), task, "task.ready_for_review", "x")
	assert.Empty(t, ns.Calls(), "reviewer moving/assigning themselves must not notify themselves")
}

func TestNotifyReviewer_OtherActor_Notifies(t *testing.T) {
	svc, ns, task := selfNotifyFixture(t)
	svc.notifyReviewer(asUser(uuid.New()), task, "task.reviewer_assigned", "x")
	calls := ns.Calls()
	require.Len(t, calls, 1)
	require.NotNil(t, calls[0].TargetUserID)
	assert.Equal(t, *task.ReviewerID, *calls[0].TargetUserID)
}
