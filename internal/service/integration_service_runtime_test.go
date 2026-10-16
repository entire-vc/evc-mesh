package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
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
	guard := ProviderGuard{
		Write:  func(json.RawMessage) error { return apierror.BadRequest("use the versioned runtime API") },
		Delete: func() error { return apierror.BadRequest("disable runtime through the versioned runtime API") },
	}
	// the guard answers for the extension id and for the legacy provider name
	for _, name := range []domain.IntegrationProvider{domain.IntegrationProviderAgentRuntime, domain.IntegrationProviderRunner} {
		svc := NewIntegrationService(runtimeGuardRepo{existing: &domain.IntegrationConfig{ID: id, Provider: name}},
			WithProviderGuard(guard, domain.IntegrationProviderAgentRuntime, domain.IntegrationProviderRunner))

		_, err := svc.Configure(ctx, domain.CreateIntegrationInput{WorkspaceID: uuid.New(), Provider: name, Config: json.RawMessage(`{}`), IsActive: true})
		require.ErrorContains(t, err, "versioned runtime API", name)
		_, err = svc.Update(ctx, id, domain.UpdateIntegrationInput{})
		require.ErrorContains(t, err, "versioned runtime API", name)
		require.ErrorContains(t, svc.Delete(ctx, id), "versioned runtime API", name)
	}
}
