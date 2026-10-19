package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type fakeBatchMover struct {
	got domain.MoveBatchInput
	res *domain.MoveBatchResult
	err error
}

func (f *fakeBatchMover) MoveBatchToProject(_ context.Context, in domain.MoveBatchInput) (*domain.MoveBatchResult, error) {
	f.got = in
	return f.res, f.err
}

func TestMoveToProjectBatch_ValidatesInputBeforeTouchingTheRepo(t *testing.T) {
	mover := &fakeBatchMover{res: &domain.MoveBatchResult{DryRun: true}}
	svc := NewTaskService(NewMockTaskRepository(), nil, nil, nil, WithTaskBatchMover(mover))
	mapping := map[uuid.UUID]uuid.UUID{uuid.New(): uuid.New()}
	ids := []uuid.UUID{uuid.New()}

	for name, in := range map[string]domain.MoveBatchInput{
		"no tasks":       {TaskIDs: nil, StatusMapping: mapping},
		"no mapping":     {TaskIDs: ids},
		"unknown leaf":   {TaskIDs: ids, StatusMapping: mapping, LeafMode: "cascade"},
		"too many tasks": {TaskIDs: make([]uuid.UUID, 51), StatusMapping: mapping},
	} {
		_, err := svc.MoveToProjectBatch(context.Background(), in)
		require.Error(t, err, name)
	}
	require.Empty(t, mover.got.TaskIDs, "repository must not be called on invalid input")

	_, err := svc.MoveToProjectBatch(context.Background(), domain.MoveBatchInput{TaskIDs: ids, StatusMapping: mapping, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, domain.MoveLeafRefuse, mover.got.LeafMode, "refuse is the default leaf mode")
}

func TestMoveToProjectBatch_WithoutMoverFailsClosed(t *testing.T) {
	svc := NewTaskService(NewMockTaskRepository(), nil, nil, nil)
	_, err := svc.MoveToProjectBatch(context.Background(), domain.MoveBatchInput{TaskIDs: []uuid.UUID{uuid.New()}, StatusMapping: map[uuid.UUID]uuid.UUID{uuid.New(): uuid.New()}})
	require.Error(t, err)
}
