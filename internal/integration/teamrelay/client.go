package teamrelay

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Publisher is the interface consumed by the artifact upload hook.
type Publisher interface {
	Publish(ctx context.Context, taskID uuid.UUID, artifactName string, content []byte, contentType string) error
}

type client struct {
	piRepo   repository.ProjectIntegrationRepository
	taskRepo repository.TaskRepository
	projRepo repository.ProjectRepository
}

// NewClient creates a new Team Relay publisher client.
func NewClient(piRepo repository.ProjectIntegrationRepository, taskRepo repository.TaskRepository, projRepo repository.ProjectRepository) Publisher {
	return &client{piRepo: piRepo, taskRepo: taskRepo, projRepo: projRepo}
}

func transportEnabled() bool {
	return os.Getenv("MESH_TEAMRELAY_TRANSPORT_ENABLED") == "true"
}

// Publish pushes an artifact to the Team Relay share for the project the task belongs to.
// It is best-effort: all errors are logged but never returned to the caller.
func (c *client) Publish(ctx context.Context, taskID uuid.UUID, artifactName string, content []byte, contentType string) error {
	// Resolve task → project.
	task, err := c.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil // best-effort; task gone or DB error
	}

	pi, err := c.piRepo.Get(ctx, task.ProjectID, "team_relay")
	if err != nil || pi == nil || !pi.Enabled {
		return nil // integration absent or disabled — silent no-op
	}

	var settings domain.TeamRelaySettings
	if err := json.Unmarshal(pi.Settings, &settings); err != nil {
		log.Printf("teamrelay: bad settings for project %s: %v", task.ProjectID, err)
		return nil
	}

	proj, err := c.projRepo.GetByID(ctx, task.ProjectID)
	if err != nil || proj == nil {
		return nil
	}

	taskShortID := taskID.String()[:8]
	filePath := BuildPath(
		settings.Subfolder,
		proj.Slug,
		settings.IncludeProjectSlug,
		taskShortID,
		task.Title,
		artifactName,
		contentType,
		time.Now(),
	)

	if !transportEnabled() {
		log.Printf("teamrelay: would publish to relay: %s (transport disabled)", filePath)
		return nil
	}

	// TODO: endpoint+auth finalize after relay-side B-choice.
	// relay reality (Daedalus) = POST /v1/web/shares/{slug}/files?path=...
	// auth TBD (B1 custom header / B2 bearer PAT)
	return transport(ctx, settings.ShareID, filePath, content, contentType, pi.AgentKey)
}

// transport is the real outbound HTTP call — left as stub for Phase 1.
func transport(_ context.Context, _ string, filePath string, _ []byte, _ string, _ string) error {
	log.Printf("teamrelay: transport stub called for path %s — not yet implemented", filePath)
	return nil
}
