package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

func newCheckoutM1Fixture(t *testing.T) (fixture *recurringActorTypeFixture, taskRepo *postgres.TaskRepo, taskSvc TaskService, taskID, agentID uuid.UUID) {
	t.Helper()
	// CI must fail on a misconfigured database, never skip the race controls.
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		db, err := sql.Open("postgres", dsn)
		require.NoError(t, err)
		require.NoError(t, db.Ping())
		require.NoError(t, db.Close())
	}
	oldClock := timeNow
	timeNow = time.Now
	t.Cleanup(func() { timeNow = oldClock })
	f := newRecurringActorTypeFixture(t)
	repo := postgres.NewTaskRepo(f.db)
	svc := NewTaskService(repo, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db), WithProjectRepo(postgres.NewProjectRepo(f.db)))
	owner := uuid.New()
	_, err := f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'checkout fixture',$3,'fixture-hash','test')`, owner, f.workspaceID, owner.String())
	require.NoError(t, err)
	task := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID, Title: "lease fixture", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, svc.Create(actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem), task))
	t.Cleanup(func() { _, _ = f.db.Exec("DELETE FROM activity_log WHERE workspace_id=$1", f.workspaceID) })
	return f, repo, svc, task.ID, owner
}

func checkoutScope() domain.CheckoutScope {
	session, request := uuid.New(), uuid.New()
	return domain.CheckoutScope{SessionID: &session, RequestID: &request}
}

func TestCheckoutM1_PostgresDelayedCleanupAndHeartbeat(t *testing.T) {
	f, repo, svc, id, owner := newCheckoutM1Fixture(t)
	ctx := agentContext(owner)
	scopeA := checkoutScope()
	a, err := svc.CheckoutTask(ctx, id, 30, nil, scopeA)
	require.NoError(t, err)
	retry, err := svc.CheckoutTask(ctx, id, 120, nil, scopeA)
	require.NoError(t, err)
	require.Equal(t, a, retry, "stable request returns the complete original lease")
	require.NoError(t, svc.ReleaseCheckout(ctx, id, a.CheckoutToken))
	require.NoError(t, svc.ReleaseCheckout(ctx, id, a.CheckoutToken))
	_, err = svc.CheckoutTask(ctx, id, 30, nil, scopeA)
	require.Error(t, err, "retired request cannot resurrect a released lease")
	b, err := svc.CheckoutTask(ctx, id, 30, nil, checkoutScope())
	require.NoError(t, err)
	require.Greater(t, b.Generation, a.Generation)
	require.Error(t, svc.ReleaseCheckout(ctx, id, a.CheckoutToken))
	require.Error(t, svc.SelfReleaseCheckout(ctx, id, domain.CheckoutExpectation{SessionID: a.SessionID, Generation: a.Generation}))
	require.Error(t, svc.SelfReleaseCheckout(ctx, id), "unscoped cleanup must not touch a session-scoped lease")
	_, err = svc.ExtendCheckout(ctx, id, a.CheckoutToken, 240)
	require.Error(t, err)
	_, err = svc.ExtendCheckout(ctx, id, uuid.Nil, 240, domain.CheckoutExpectation{SessionID: a.SessionID, Generation: a.Generation})
	require.Error(t, err)
	_, err = f.db.Exec(`INSERT INTO project_rules(id,project_id,rule_type,config,enforcement_mode) VALUES($1,$2,'workflow','{"mid_pipeline":{"heartbeat_extends_checkout":true}}','advisory')`, uuid.New(), f.projectID)
	require.NoError(t, err)
	n, err := repo.ExtendCheckoutsOnHeartbeat(ctx, owner)
	require.NoError(t, err)
	require.Zero(t, n, "old bulk heartbeat cannot extend the new session")
	fresh, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, b.CheckoutToken, *fresh.CheckoutToken)
	require.Equal(t, b.Generation, fresh.CheckoutGeneration)
	require.Equal(t, b.ExpiresAt, *fresh.CheckoutExpires)
	var acquired, released int
	require.NoError(t, f.db.Get(&acquired, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.checkout_acquired'`, id))
	require.NoError(t, f.db.Get(&released, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.checkout_released'`, id))
	require.Equal(t, 2, acquired)
	require.Equal(t, 1, released, "duplicate/stale release does not claim to have released B")
	_, err = svc.ExtendCheckout(ctx, id, uuid.Nil, 60, domain.CheckoutExpectation{SessionID: b.SessionID, Generation: b.Generation})
	require.NoError(t, err, "positive scoped heartbeat control")
}

func TestCheckoutM1_PostgresAdminRecoveryAudit(t *testing.T) {
	f, repo, svc, id, owner := newCheckoutM1Fixture(t)
	lease, err := svc.CheckoutTask(agentContext(owner), id, 30, nil, checkoutScope())
	require.NoError(t, err)
	expected := domain.CheckoutExpectation{Holder: &owner, SessionID: lease.SessionID, Generation: lease.Generation, Reason: "recover stopped session"}
	require.Error(t, svc.ForceReleaseCheckout(agentContext(uuid.New()), id, expected))
	admin := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	stale := expected
	stale.Generation++
	require.Error(t, svc.ForceReleaseCheckout(admin, id, stale))
	fresh, err := repo.GetByID(admin, id)
	require.NoError(t, err)
	require.Equal(t, lease.CheckoutToken, *fresh.CheckoutToken)
	require.NoError(t, svc.ForceReleaseCheckout(admin, id, expected))
	require.NoError(t, svc.ForceReleaseCheckout(admin, id, expected))
	var count int
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.checkout_force_released' AND changes->>'reason'=$2 AND changes->>'previous_holder'=$3 AND (changes->>'generation')::bigint=$4 AND NOT changes ? 'checkout_token' AND NOT changes::text LIKE $5`, id, expected.Reason, owner.String(), lease.Generation, "%"+lease.CheckoutToken.String()+"%"))
	require.Equal(t, 1, count)
}

type checkoutCommitBarrier struct {
	repository.TaskRepository
	ready     chan struct{}
	committed chan error
	status    uuid.UUID
}

func (r *checkoutCommitBarrier) Update(ctx context.Context, task *domain.Task) error {
	if err := r.TaskRepository.Update(ctx, task); err != nil {
		return err
	}
	if task.StatusID == r.status {
		close(r.ready)
		return <-r.committed
	}
	return nil
}

func TestCheckoutM1_PostgresTerminalCommitThenNewAcquire(t *testing.T) {
	f, repo, svc, id, a := newCheckoutM1Fixture(t)
	ctx := agentContext(a)
	old, err := svc.CheckoutTask(ctx, id, 30, nil, checkoutScope())
	require.NoError(t, err)
	review := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.projectID, Name: "Review", Slug: "review", Category: domain.StatusCategoryReview, Color: "#ffffff", Position: 2}
	require.NoError(t, postgres.NewTaskStatusRepo(f.db).Create(ctx, review))
	barrier := &checkoutCommitBarrier{TaskRepository: repo, ready: make(chan struct{}), committed: make(chan error, 1), status: review.ID}
	mover := NewTaskService(barrier, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db), WithProjectRepo(postgres.NewProjectRepo(f.db)))
	newLease := make(chan *domain.CheckoutLease, 1)
	go func() {
		<-barrier.ready
		_, releaseErr := repo.CompareReleaseCheckout(ctx, id, domain.CheckoutExpectation{Token: &old.CheckoutToken})
		if releaseErr != nil {
			barrier.committed <- releaseErr
			return
		}
		lease, acquireErr := repo.AcquireCheckout(ctx, id, a, uuid.New(), time.Now().Add(time.Hour), checkoutScope())
		newLease <- lease
		barrier.committed <- acquireErr
	}()
	require.NoError(t, mover.MoveTask(ctx, id, MoveTaskInput{StatusID: &review.ID}))
	lease := <-newLease
	fresh, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, *lease.Token, *fresh.CheckoutToken)
	require.Equal(t, lease.Generation, fresh.CheckoutGeneration)
	require.Equal(t, *lease.ExpiresAt, *fresh.CheckoutExpires)
	var falseReleases int
	require.NoError(t, f.db.Get(&falseReleases, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.checkout_released_auto'`, id))
	require.Zero(t, falseReleases)
}
