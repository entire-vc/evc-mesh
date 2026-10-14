package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// patchLikeWrite is what PATCH /tasks/:id does: snapshot, compare against the
// caller's expected values, then write conditioned on the snapshot's version.
func patchLikeWrite(ctx context.Context, svc TaskService, repoGet func(context.Context, uuid.UUID) (*domain.Task, error), id uuid.UUID, expUpdated *domain.Task, title string) error {
	snap, err := repoGet(ctx, id)
	if err != nil {
		return err
	}
	if err := CheckTaskPreconditions(snap, &expUpdated.UpdatedAt, &expUpdated.Version); err != nil {
		return err
	}
	snap.Title = title
	return svc.Update(ctx, snap)
}

// Two or more writers holding the same expected state: exactly one write lands,
// the rest get CASConflictError, and the row carries the winner's value only.
func TestTaskPreconditions_PostgresRaceExactlyOneWrite(t *testing.T) {
	_, repo, svc, id, _ := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	seen, err := repo.GetByID(ctx, id)
	require.NoError(t, err)

	const writers = 12
	var wins, conflicts atomic.Int32
	winner := make(chan string, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			title := "writer-" + uuid.NewString()[:8]
			<-start
			werr := patchLikeWrite(ctx, svc, repo.GetByID, id, seen, title)
			var cas *CASConflictError
			if werr == nil {
				wins.Add(1)
				winner <- title
			} else if assert.ErrorAs(t, werr, &cas, "writer %d", i) {
				conflicts.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, wins.Load(), "exactly one write may land")
	assert.EqualValues(t, writers-1, conflicts.Load())
	after, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, <-winner, after.Title, "the row holds the winner's value")
	assert.Equal(t, seen.Version+1, after.Version, "one write, one version bump")
}

// A refused PATCH leaves the stored row byte-for-byte as it was.
func TestTaskPreconditions_PostgresStaleExpectationLeavesRowUntouched(t *testing.T) {
	_, repo, svc, id, _ := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	before, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	stale := *before
	stale.Version--

	err = patchLikeWrite(ctx, svc, repo.GetByID, id, &stale, "must not land")

	var cas *CASConflictError
	require.ErrorAs(t, err, &cas)
	assert.Equal(t, before.Version, cas.CurrentVersion)
	assert.True(t, before.UpdatedAt.Equal(cas.CurrentUpdatedAt))
	after, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, before.Title, after.Title)
	assert.Equal(t, before.Version, after.Version)
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt))
}

// A matching expectation writes, and the write is visible.
func TestTaskPreconditions_PostgresMatchingExpectationWrites(t *testing.T) {
	_, repo, svc, id, _ := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	seen, err := repo.GetByID(ctx, id)
	require.NoError(t, err)

	require.NoError(t, patchLikeWrite(ctx, svc, repo.GetByID, id, seen, "landed"))

	after, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "landed", after.Title)
	assert.Equal(t, seen.Version+1, after.Version)
}

// /assign with the same expected version from several callers: one lands.
func TestTaskPreconditions_PostgresAssignRaceExactlyOneWrite(t *testing.T) {
	f, repo, _, id, agent := newCheckoutM1Fixture(t)
	svc := assignSvc(f, repo)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	seen, err := repo.GetByID(ctx, id)
	require.NoError(t, err)

	const writers = 12
	var wins, conflicts atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			aerr := svc.AssignTask(ctx, id, AssignTaskInput{
				AssigneeID: &agent, AssigneeType: domain.AssigneeTypeAgent, Source: domain.AssignmentSourceHuman,
				ExpectedUpdatedAt: &seen.UpdatedAt, ExpectedVersion: &seen.Version,
			})
			var cas *CASConflictError
			switch {
			case aerr == nil:
				wins.Add(1)
			case assert.ErrorAs(t, aerr, &cas, "writer %d", i):
				conflicts.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, wins.Load(), "exactly one assignment may land")
	assert.EqualValues(t, writers-1, conflicts.Load())
	after, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, after.AssigneeID)
	assert.Equal(t, agent, *after.AssigneeID)
	assert.Equal(t, seen.Version+1, after.Version)
}

// A stale expectation on /assign writes nothing.
func TestTaskPreconditions_PostgresAssignStaleExpectationLeavesRowUntouched(t *testing.T) {
	f, repo, _, id, agent := newCheckoutM1Fixture(t)
	svc := assignSvc(f, repo)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	before, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	stale := before.Version - 1

	err = svc.AssignTask(ctx, id, AssignTaskInput{
		AssigneeID: &agent, AssigneeType: domain.AssigneeTypeAgent, Source: domain.AssignmentSourceHuman, ExpectedVersion: &stale,
	})

	var cas *CASConflictError
	require.ErrorAs(t, err, &cas)
	after, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Nil(t, after.AssigneeID)
	assert.Equal(t, before.Version, after.Version)
}

// assignSvc is the fixture's task service plus the agent directory an
// assignment to an agent needs to validate its assignee.
func assignSvc(f *recurringActorTypeFixture, repo *postgres.TaskRepo) TaskService {
	return NewTaskService(repo, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db),
		WithProjectRepo(postgres.NewProjectRepo(f.db)), WithTaskAgentRepo(postgres.NewAgentRepo(f.db)),
		WithProjectMemberRepoTask(postgres.NewProjectMemberRepo(f.db)))
}
