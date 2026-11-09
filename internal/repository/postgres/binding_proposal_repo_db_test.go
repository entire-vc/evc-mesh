package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Proves against real Postgres what the handler tests (store fake) cannot: the
// proposal round-trips with its sources and scope, MarkApplied is the single
// replay guard (one winner even when called twice), the change CHECK refuses a
// no-op proposal, and deleting the edge removes its proposals.
func TestBindingProposalRepo_RoundTripAndOneShotApply(t *testing.T) {
	f := newGraphDBFixture(t, 2)
	ctx := context.Background()
	var tasks []uuid.UUID
	require.NoError(t, f.db.SelectContext(ctx, &tasks, `SELECT id FROM tasks WHERE project_id=$1 ORDER BY task_number`, f.project))
	require.Len(t, tasks, 2)
	edge := uuid.New()
	f.exec(t, `INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type,condition) VALUES($1,$2,$3,'blocks','task_done')`, edge, tasks[0], tasks[1])

	repo := postgres.NewBindingProposalRepo(f.db)
	p := &domain.StoredBindingProposal{
		BindingProposal: domain.BindingProposal{
			ID: uuid.NewString(), EdgeID: edge, Old: domain.DependencyConditionTaskDone, New: domain.DependencyConditionContractReady,
			Sources: []string{"mesh:comment:a:b", "mr:example/product!1"}, Scope: "edge only", Generation: "v1:abc",
		},
		ProposedBy: f.user,
	}
	require.NoError(t, repo.Create(ctx, p))
	assert.False(t, p.CreatedAt.IsZero(), "created_at is returned from the insert")

	got, err := repo.Get(ctx, uuid.MustParse(p.ID))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, p.Sources, got.Sources)
	assert.Equal(t, "edge only", got.Scope)
	assert.Equal(t, domain.DependencyConditionContractReady, got.New)
	assert.Equal(t, "v1:abc", got.Generation)
	assert.False(t, got.Applied)
	assert.Nil(t, got.ConfirmedBy)

	missing, err := repo.Get(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, missing, "unknown id is (nil, nil)")

	won, err := repo.MarkApplied(ctx, uuid.MustParse(p.ID), f.user, time.Now())
	require.NoError(t, err)
	assert.True(t, won)
	again, err := repo.MarkApplied(ctx, uuid.MustParse(p.ID), f.user, time.Now())
	require.NoError(t, err)
	assert.False(t, again, "replay must lose: status moves pending -> applied once")
	none, err := repo.MarkApplied(ctx, uuid.New(), f.user, time.Now())
	require.NoError(t, err)
	assert.False(t, none, "unknown proposal is not applied")

	got, err = repo.Get(ctx, uuid.MustParse(p.ID))
	require.NoError(t, err)
	assert.True(t, got.Applied)
	require.NotNil(t, got.ConfirmedBy)
	assert.Equal(t, f.user, *got.ConfirmedBy)
	assert.NotNil(t, got.ConfirmedAt)

	noop := &domain.StoredBindingProposal{
		BindingProposal: domain.BindingProposal{ID: uuid.NewString(), EdgeID: edge, Old: domain.DependencyConditionTaskDone, New: domain.DependencyConditionTaskDone,
			Sources: []string{"x"}, Scope: "s", Generation: "g"},
		ProposedBy: f.user,
	}
	assert.Error(t, repo.Create(ctx, noop), "old == new is refused by the database")

	f.exec(t, `DELETE FROM task_dependencies WHERE id=$1`, edge)
	gone, err := repo.Get(ctx, uuid.MustParse(p.ID))
	require.NoError(t, err)
	assert.Nil(t, gone, "proposals go with their edge")
}
