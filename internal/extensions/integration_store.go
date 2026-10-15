package extensions

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// IntegrationConnections adapts integration_configs (provider = extension id)
// to ConnectionStore.
type IntegrationConnections struct {
	Repo repository.IntegrationRepository
}

func (s IntegrationConnections) Connection(ctx context.Context, workspaceID uuid.UUID, extensionID string) (*Connection, error) {
	row, err := s.Repo.GetByProvider(ctx, workspaceID, domain.IntegrationProvider(extensionID))
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	settings := map[string]any{}
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &settings); err != nil {
			return nil, fmt.Errorf("connection %s/%s: bad config: %w", workspaceID, extensionID, err)
		}
	}
	return &Connection{Settings: settings, IsActive: row.IsActive}, nil
}

// IntegrationConsents adapts the consent recorded on integration_configs to
// ConsentStore. A connection with no recorded consent (made before consent was
// stored) keeps what the manifest declares, as a registry without a store does.
type IntegrationConsents struct {
	Repo     repository.ExtensionConsentRepository
	Declared func(extensionID string) []string
}

func (s IntegrationConsents) Consented(ctx context.Context, workspaceID uuid.UUID, extensionID string) ([]string, error) {
	c, err := s.Repo.GetConsent(ctx, workspaceID, extensionID)
	if err != nil {
		return nil, err
	}
	if c != nil {
		return c.Capabilities, nil
	}
	if s.Declared != nil {
		return s.Declared(extensionID), nil
	}
	return nil, nil
}
