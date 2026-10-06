package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func checkoutGenerationFixture(t *testing.T) (repo *TaskRepo, taskID, owner uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run PostgreSQL lease controls")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err, "configured PostgreSQL controls must not silently skip")
	t.Cleanup(func() { _ = db.Close() })
	ws, project, status := uuid.New(), uuid.New(), uuid.New()
	owner, taskID = uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'checkout fixture',$2,$3)`, ws, ws.String(), uuid.New())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DELETE FROM workspaces WHERE id=$1", ws) })
	_, err = db.Exec(`INSERT INTO projects(id,workspace_id,name,slug) VALUES($1,$2,'checkout fixture',$3)`, project, ws, project.String())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO task_statuses(id,project_id,name,slug,category) VALUES($1,$2,'Todo','todo','todo')`, status, project)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'fixture',$3,'fixture','test')`, owner, ws, owner.String())
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by,created_by_type) VALUES($1,$2,$3,'checkout fixture',1,$4,'system')`, taskID, project, status, uuid.Nil)
	require.NoError(t, err)
	return NewTaskRepo(db), taskID, owner
}

func generationScope() domain.CheckoutScope {
	session, request := uuid.New(), uuid.New()
	return domain.CheckoutScope{SessionID: &session, RequestID: &request}
}

func TestCheckoutGeneration_ConcurrentAcquire(t *testing.T) {
	for _, sameAgent := range []bool{false, true} {
		t.Run(map[bool]string{false: "different-agent", true: "same-agent-different-session"}[sameAgent], func(t *testing.T) {
			repo, id, a := checkoutGenerationFixture(t)
			b := a
			if !sameAgent {
				b = uuid.New()
				_, err := repo.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) SELECT $1,workspace_id,'second',$2,'fixture','test' FROM agents WHERE id=$3`, b, b.String(), a)
				require.NoError(t, err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, holder := range []uuid.UUID{a, b} {
				go func(holder uuid.UUID) {
					<-start
					_, err := repo.AcquireCheckout(context.Background(), id, holder, uuid.New(), time.Now().Add(time.Hour), generationScope())
					results <- err
				}(holder)
			}
			close(start)
			wins := 0
			for range 2 {
				if err := <-results; err == nil {
					wins++
				} else {
					require.ErrorIs(t, err, ErrCheckoutConflict)
				}
			}
			require.Equal(t, 1, wins)
			fresh, err := repo.GetByID(context.Background(), id)
			require.NoError(t, err)
			require.NotNil(t, fresh.CheckedOutBy)
			require.EqualValues(t, 1, fresh.CheckoutGeneration)
		})
	}
}

func TestCheckoutGeneration_StaleReleaseAndExtend(t *testing.T) {
	repo, id, owner := checkoutGenerationFixture(t)
	ctx := context.Background()
	scope := generationScope()
	a, err := repo.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(time.Hour), scope)
	require.NoError(t, err)
	retry, err := repo.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(2*time.Hour), scope)
	require.NoError(t, err)
	require.False(t, retry.Changed)
	retry.Changed = true
	require.Equal(t, a, retry)
	expectedA := domain.CheckoutExpectation{Holder: &owner, SessionID: scope.SessionID, Generation: a.Generation}
	released, err := repo.CompareReleaseCheckout(ctx, id, expectedA)
	require.NoError(t, err)
	require.Equal(t, *a.Token, *released.Token)
	released, err = repo.CompareReleaseCheckout(ctx, id, expectedA)
	require.NoError(t, err)
	require.Nil(t, released, "duplicate release must not claim another successful mutation")
	released, err = repo.CompareReleaseCheckout(ctx, uuid.New(), expectedA)
	require.NoError(t, err)
	require.Nil(t, released)
	b, err := repo.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(time.Hour), generationScope())
	require.NoError(t, err)
	_, err = repo.CompareReleaseCheckout(ctx, id, expectedA)
	require.ErrorIs(t, err, ErrInvalidCheckoutToken)
	_, err = repo.ExtendScopedCheckout(ctx, id, expectedA, time.Now().Add(2*time.Hour))
	require.ErrorIs(t, err, ErrInvalidCheckoutToken)
	fresh, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, *b.Token, *fresh.CheckoutToken)
	require.Equal(t, b.Generation, fresh.CheckoutGeneration)
	require.Equal(t, *b.ExpiresAt, *fresh.CheckoutExpires)
	extended, err := repo.ExtendScopedCheckout(ctx, id, domain.CheckoutExpectation{Holder: &owner, SessionID: b.SessionID, Generation: b.Generation}, time.Now().Add(2*time.Hour))
	require.NoError(t, err, "positive scoped extend control")
	require.Greater(t, extended.ExpiresAt.Unix(), b.ExpiresAt.Unix())
	_, err = repo.CompareReleaseCheckout(ctx, id, domain.CheckoutExpectation{Token: b.Token})
	require.NoError(t, err, "positive token cleanup control")
}

func TestCheckoutGeneration_LegacyAdaptersAndErrors(t *testing.T) {
	repo, id, holder := checkoutGenerationFixture(t)
	ctx := context.Background()
	token := uuid.New()
	require.NoError(t, repo.AtomicCheckout(ctx, id, holder, token, time.Now().Add(time.Hour)))
	require.NoError(t, repo.AtomicCheckout(ctx, id, holder, uuid.New(), time.Now().Add(2*time.Hour)))
	fresh, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, token, *fresh.CheckoutToken, "legacy retry retains its token")
	require.Error(t, repo.ForceReleaseCheckout(ctx, id), "unscoped force adapter refuses to mutate")
	require.NoError(t, repo.ReleaseCheckout(ctx, id, token))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = repo.AcquireCheckout(cancelled, id, holder, token, time.Now(), generationScope())
	require.ErrorIs(t, err, context.Canceled)
	_, err = repo.CompareReleaseCheckout(cancelled, id, domain.CheckoutExpectation{Token: &token})
	require.ErrorIs(t, err, context.Canceled)
	_, err = repo.ExtendScopedCheckout(cancelled, id, domain.CheckoutExpectation{Token: &token}, time.Now())
	require.ErrorIs(t, err, context.Canceled)
}
