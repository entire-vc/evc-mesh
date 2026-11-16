package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func TestTriageService_ListTriageTasks_ForcesQueuePredicateAndOrder(t *testing.T) {
	repo := &MockTaskRepository{}
	svc := NewTriageService(repo)
	proj := uuid.New()

	page, err := svc.ListTriageTasks(context.Background(), repository.TaskQuery{WorkspaceID: uuid.New(), ProjectID: &proj, Sort: repository.TaskQuerySortDueDate}, pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
	require.NotNil(t, repo.lastTaskQuery)
	assert.True(t, repo.lastTaskQuery.Gated, "the inbox is always the gated queue")
	assert.Equal(t, repository.TaskQuerySortTriage, repo.lastTaskQuery.Sort, "the inbox keeps its own order")
	assert.Equal(t, &proj, repo.lastTaskQuery.ProjectID, "the caller's narrowing is kept")
}

func TestTriageService_QueryTasks_PassesQueryThrough(t *testing.T) {
	repo := &MockTaskRepository{}
	svc := NewTriageService(repo)
	q := repository.TaskQuery{WorkspaceID: uuid.New(), Sort: repository.TaskQuerySortPriority}

	_, err := svc.QueryTasks(context.Background(), q, pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, q, *repo.lastTaskQuery)
}

func TestTriageService_ListTriageTasks_PropagatesError(t *testing.T) {
	repo := &MockTaskRepository{errToReturn: errors.New("db down")}
	svc := NewTriageService(repo)

	_, err := svc.ListTriageTasks(context.Background(), repository.TaskQuery{WorkspaceID: uuid.New()}, pagination.Params{})
	require.Error(t, err)
}
