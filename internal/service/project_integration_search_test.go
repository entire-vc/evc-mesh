package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func TestSearchTR_ShareNotFound(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	svc := NewProjectIntegrationService(repo)

	_, err := svc.SearchTR(context.Background(), "nonexistent", "", 20)
	require.Error(t, err)
	var apiErr *apierror.Error
	assert.True(t, errors.As(err, &apiErr), "expected apierror.Error, got %T: %v", err, err)
	assert.Equal(t, 404, apiErr.Code)
}

func TestSearchTR_RepoError(t *testing.T) {
	repo := NewMockProjectIntegrationRepository()
	repo.err = errors.New("db down")
	svc := NewProjectIntegrationService(repo)

	_, err := svc.SearchTR(context.Background(), "mesh", "", 20)
	require.Error(t, err)
	assert.EqualError(t, err, "db down")
}
