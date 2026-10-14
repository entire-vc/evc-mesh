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
