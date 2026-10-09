package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

func TestTriageService_ListTriageTasks_DelegatesToQueue(t *testing.T) {
	repo := &MockTaskRepository{}
	svc := NewTriageService(repo)

	page, err := svc.ListTriageTasks(context.Background(), uuid.New(), pagination.Params{})
	require.NoError(t, err)
	assert.Equal(t, 0, page.TotalCount)
}

func TestTriageService_ListTriageTasks_PropagatesError(t *testing.T) {
	repo := &MockTaskRepository{errToReturn: errors.New("db down")}
	svc := NewTriageService(repo)

	_, err := svc.ListTriageTasks(context.Background(), uuid.New(), pagination.Params{})
	require.Error(t, err)
}
