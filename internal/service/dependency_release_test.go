package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// blockerSetup describes the blocker of one typed edge in the auto-transition
// and backlog-advisory readers below.
type blockerSetup struct {
	category domain.StatusCategory
	shipped  bool
	deployed bool
}

// The same table drives both Go readers (auto-unblock and the backlog advisory):
// a typed edge follows its condition, a default edge keeps done-or-cancelled.
var releaseReaderCases = []struct {
	name    string
	cond    domain.DependencyCondition
	blocker blockerSetup
	release bool
}{
	{"closed edge, cancelled blocker releases (unchanged)", domain.DependencyConditionTaskDone, blockerSetup{category: domain.StatusCategoryCancelled}, true},
	{"closed edge, open blocker holds", domain.DependencyConditionTaskDone, blockerSetup{category: domain.StatusCategoryInProgress}, false},
	{"accepted edge, cancelled blocker holds", domain.DependencyConditionAccepted, blockerSetup{category: domain.StatusCategoryCancelled}, false},
	{"accepted edge, done blocker releases", domain.DependencyConditionAccepted, blockerSetup{category: domain.StatusCategoryDone}, true},
	{"shipped edge, cancelled blocker holds", domain.DependencyConditionShipped, blockerSetup{category: domain.StatusCategoryCancelled}, false},
	{"shipped edge, done blocker releases", domain.DependencyConditionShipped, blockerSetup{category: domain.StatusCategoryDone}, true},
	{"shipped edge, is_shipped open blocker releases", domain.DependencyConditionShipped, blockerSetup{category: domain.StatusCategoryInProgress, shipped: true}, true},
	{"deployed edge, merged (done) blocker holds", domain.DependencyConditionDeployed, blockerSetup{category: domain.StatusCategoryDone}, false},
	{"deployed edge, deploy recorded releases", domain.DependencyConditionDeployed, blockerSetup{category: domain.StatusCategoryDone, deployed: true}, true},
	{"deployed edge, is_shipped releases", domain.DependencyConditionDeployed, blockerSetup{category: domain.StatusCategoryInProgress, shipped: true}, true},
}

func TestAutoTransition_TypedEdgesFollowTheirCondition(t *testing.T) {
	for _, tc := range releaseReaderCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, taskRepo, statusRepo, depRepo := buildAutoTransitionFixture()
			projectID := uuid.New()
			blockerStatus := seedStatus(statusRepo, projectID, tc.blocker.category, "Blocker status")
			backlog := seedStatus(statusRepo, projectID, domain.StatusCategoryBacklog, "Backlog")
			todo := seedStatus(statusRepo, projectID, domain.StatusCategoryTodo, "To Do")

			blocker := seedTask(taskRepo, projectID, blockerStatus.ID, nil, "blocker")
			blocker.IsShipped = tc.blocker.shipped
			dependent := seedTask(taskRepo, projectID, backlog.ID, nil, "dependent")
			dependent.Labels = []string{"park:dependency"}
			dep := &domain.TaskDependency{ID: uuid.New(), TaskID: dependent.ID, DependsOnTaskID: blocker.ID,
				DependencyType: domain.DependencyTypeBlocks, Condition: tc.cond}
			depRepo.items[dep.ID] = dep
			depRepo.deployed[blocker.ID] = tc.blocker.deployed

			require.NoError(t, svc.CheckDependencyResolution(ctx, blocker.ID))

			if tc.release {
				assert.Equal(t, todo.ID, taskRepo.items[dependent.ID].StatusID, "dependent leaves backlog")
			} else {
				assert.Equal(t, backlog.ID, taskRepo.items[dependent.ID].StatusID, "dependent stays held")
			}
		})
	}
}

func TestBacklogPromotionAdvisory_TypedEdgesFollowTheirCondition(t *testing.T) {
	for _, tc := range releaseReaderCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newBacklogHarness()
			projectID := uuid.New()
			backlog := h.addStatus(t, projectID, "Backlog", domain.StatusCategoryBacklog)
			blockerStatus := h.addStatus(t, projectID, "Blocker status", tc.blocker.category)

			blocker := h.addTask(t, projectID, blockerStatus.ID)
			blocker.IsShipped = tc.blocker.shipped
			task := h.addTask(t, projectID, backlog.ID)
			dep := &domain.TaskDependency{ID: uuid.New(), TaskID: task.ID, DependsOnTaskID: blocker.ID,
				DependencyType: domain.DependencyTypeBlocks, Condition: tc.cond}
			require.NoError(t, h.depRepo.Create(context.Background(), dep))
			h.depRepo.deployed[blocker.ID] = tc.blocker.deployed

			decisions, err := h.svc.SweepAdvisory(context.Background())
			require.NoError(t, err)
			got := decisionFor(t, decisions, task.ID)
			assert.Equal(t, tc.release, got.Promote, got.Reason)
		})
	}
}

func TestFillDeploymentsQueriesOnlyDeployedEdges(t *testing.T) {
	depRepo := NewMockTaskDependencyRepository()
	deployedBlocker, acceptedBlocker := uuid.New(), uuid.New()
	depRepo.deployed[deployedBlocker], depRepo.deployed[acceptedBlocker] = true, true
	deps := []domain.TaskDependency{
		{DependsOnTaskID: deployedBlocker, DependencyType: domain.DependencyTypeBlocks, Condition: domain.DependencyConditionDeployed},
		{DependsOnTaskID: acceptedBlocker, DependencyType: domain.DependencyTypeBlocks, Condition: domain.DependencyConditionAccepted},
	}
	states := map[uuid.UUID]domain.BlockerState{
		deployedBlocker: {Category: domain.StatusCategoryDone},
		acceptedBlocker: {Category: domain.StatusCategoryDone},
	}
	require.NoError(t, fillDeployments(context.Background(), depRepo, deps, states))
	assert.True(t, states[deployedBlocker].HasDeployment)
	assert.False(t, states[acceptedBlocker].HasDeployment, "edges of other conditions never read the deploy flag")
}

func TestHasUnresolvedBlockers_TypedEdges(t *testing.T) {
	blocker := uuid.New()
	edge := func(c domain.DependencyCondition) []domain.TaskDependency {
		return []domain.TaskDependency{{DependsOnTaskID: blocker, DependencyType: domain.DependencyTypeBlocks, Condition: c}}
	}
	cancelled := map[uuid.UUID]domain.BlockerState{blocker: {Category: domain.StatusCategoryCancelled}}
	assert.False(t, hasUnresolvedBlockers(edge(domain.DependencyConditionTaskDone), cancelled), "closed: cancelled clears, as before")
	assert.True(t, hasUnresolvedBlockers(edge(domain.DependencyConditionAccepted), cancelled), "accepted: cancelled is not acceptance")
	assert.True(t, hasUnresolvedBlockers(edge(domain.DependencyConditionShipped), cancelled))
	assert.True(t, hasUnresolvedBlockers(edge(domain.DependencyConditionAccepted), map[uuid.UUID]domain.BlockerState{}), "unknown blocker holds")
}

// The deployment mark is recorded in metadata.deployed, so a deploy reported on a
// pr link tells the resolver to look at the dependents of that card.
func TestPatchMetadata_DeployedTriggersDependencyResolution(t *testing.T) {
	svc, _, link := patchMetaSetup(domain.VCSLinkTypePR)
	res := &fakeResolver{}
	svc.depResolver = res
	ctx := context.Background()

	_, err := svc.PatchMetadata(ctx, link.TaskID, link.ID, map[string]any{"deployed": map[string]any{"sha": sha("3"), "environment": "production"}})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{link.TaskID}, res.calls)

	_, err = svc.PatchMetadata(ctx, link.TaskID, link.ID, map[string]any{"ship_sha": sha("4")})
	require.NoError(t, err)
	assert.Len(t, res.calls, 1, "a patch that does not touch deployed resolves nothing")
}

type fakeResolver struct{ calls []uuid.UUID }

func (f *fakeResolver) CheckDependencyResolution(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, id)
	return nil
}

func TestTaskDependencyService_Create_ShippedConditionAndUnknownNames(t *testing.T) {
	svc, _, taskRepo := setupTaskDependencyService()
	a, b := uuid.New(), uuid.New()
	addTask(taskRepo, a)
	addTask(taskRepo, b)
	ctx := context.Background()

	dep := &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks, Condition: domain.DependencyConditionShipped}
	require.NoError(t, svc.Create(ctx, dep))
	assert.Equal(t, domain.DependencyConditionShipped, dep.Condition)
	assert.Nil(t, dep.ConditionMetAt)

	for _, bad := range []domain.DependencyCondition{"closed", "satisfied", "SHIPPED"} {
		err := svc.Create(ctx, &domain.TaskDependency{TaskID: a, DependsOnTaskID: b, DependencyType: domain.DependencyTypeBlocks, Condition: bad})
		require.Error(t, err, string(bad))
		assert.Equal(t, 400, err.(*apierror.Error).Code, string(bad))
	}

	c := uuid.New()
	addTask(taskRepo, c)
	err := svc.Create(ctx, &domain.TaskDependency{TaskID: a, DependsOnTaskID: c, DependencyType: domain.DependencyTypeRelatesTo, Condition: domain.DependencyConditionShipped})
	require.Error(t, err, "a typed condition on a non-blocks edge is refused")
	assert.Equal(t, 400, err.(*apierror.Error).Code)
}
