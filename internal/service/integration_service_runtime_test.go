package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// runtimeGuardRepo embeds the interface so any call past the guard (Upsert,
// Update, Delete) panics: the refusals below must happen before the repository
// is asked to change anything.
type runtimeGuardRepo struct {
	repository.IntegrationRepository
	existing *domain.IntegrationConfig
}

func (r runtimeGuardRepo) GetByID(context.Context, uuid.UUID) (*domain.IntegrationConfig, error) {
	return r.existing, nil
}

func TestIntegrationServiceRefusesAgentRuntimeMutations(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	svc := NewIntegrationService(runtimeGuardRepo{existing: &domain.IntegrationConfig{ID: id, Provider: domain.IntegrationProviderAgentRuntime}})

	_, err := svc.Configure(ctx, domain.CreateIntegrationInput{WorkspaceID: uuid.New(), Provider: domain.IntegrationProviderAgentRuntime, Config: json.RawMessage(`{}`), IsActive: true})
	require.ErrorContains(t, err, "versioned runtime API")
	_, err = svc.Update(ctx, id, domain.UpdateIntegrationInput{})
	require.ErrorContains(t, err, "versioned runtime API")
	require.ErrorContains(t, svc.Delete(ctx, id), "versioned runtime API")
}
