package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/eventbus"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// defaultTTLSeconds is the default event TTL when not specified (24 hours).
const defaultTTLSeconds = 86400

type eventBusService struct {
	eventRepo     repository.EventBusMessageRepository
	activityRepo  repository.ActivityLogRepository
	publisher     eventbus.Publisher
	workspaceRepo repository.WorkspaceRepository
	projectRepo   repository.ProjectRepository
	memoryService MemoryService
}

// NewEventBusService returns a new EventBusService backed by the given repositories.
func NewEventBusService(
	eventRepo repository.EventBusMessageRepository,
	activityRepo repository.ActivityLogRepository,
) EventBusService {
	return &eventBusService{
		eventRepo:    eventRepo,
		activityRepo: activityRepo,
	}
}

// SetEventBus configures the optional NATS JetStream event bus publisher.
// When set, Publish() will also publish events to NATS and Redis.
// workspaceRepo and projectRepo are needed to resolve UUIDs to slugs.
func (s *eventBusService) SetEventBus(
	publisher eventbus.Publisher,
	workspaceRepo repository.WorkspaceRepository,
	projectRepo repository.ProjectRepository,
) {
	s.publisher = publisher
	s.workspaceRepo = workspaceRepo
	s.projectRepo = projectRepo
}

// SetMemoryService wires an optional MemoryService so that Publish() can
// extract and persist memories from events when a MemoryHint is present.
func (s *eventBusService) SetMemoryService(ms MemoryService) {
	s.memoryService = ms
}

// Publish creates a new event bus message.
// It generates a UUID, sets timestamps, and calculates expires_at from the TTL.
// If an EventBus publisher is configured, the event is also published to NATS
// and broadcast to Redis.
func (s *eventBusService) Publish(ctx context.Context, input PublishEventInput) (*domain.EventBusMessage, error) {
	if input.Subject == "" {
		return nil, apierror.ValidationError(map[string]string{
			"subject": "subject is required",
		})
	}

	ttl := input.TTLSeconds
	if ttl <= 0 {
		ttl = defaultTTLSeconds
	}

	now := timeNow()
	expiresAt := now.Add(time.Duration(ttl) * time.Second)

	payloadJSON, err := json.Marshal(input.Payload)
	if err != nil {
		return nil, apierror.InternalError("failed to marshal event payload")
	}

	msg := &domain.EventBusMessage{
		ID:          uuid.New(),
		WorkspaceID: input.WorkspaceID,
		ProjectID:   input.ProjectID,
		TaskID:      input.TaskID,
		AgentID:     input.AgentID,
		EventType:   input.EventType,
		Subject:     input.Subject,
		Payload:     payloadJSON,
		Tags:        pq.StringArray(input.Tags),
		TTL:         fmt.Sprintf("%d seconds", ttl),
		CreatedAt:   now,
		ExpiresAt:   &expiresAt,
	}

	// If EventBus is available, publish through it (which also handles PG persist + Redis broadcast).
	if s.publisher != nil && s.workspaceRepo != nil && s.projectRepo != nil {
		workspaceSlug, projectSlug, err := s.resolveSlugs(ctx, input.WorkspaceID, input.ProjectID)
		if err != nil {
			log.Printf("[event_bus_service] WARNING: failed to resolve slugs for NATS publish: %v", err)
			// Fall through to direct PG write.
		} else {
			if pubErr := s.publisher.PublishEvent(ctx, msg, workspaceSlug, projectSlug); pubErr != nil {
				log.Printf("[event_bus_service] WARNING: failed to publish to event bus: %v", pubErr)
				// Fall through to direct PG write as fallback.
			} else {
				// Event was published to NATS, persisted to PG, and broadcast to Redis.
				s.extractMemory(ctx, msg, input.MemoryHint)
				return msg, nil
			}
		}
	}

	// Fallback: persist directly to PostgreSQL only.
	if err := s.eventRepo.Create(ctx, msg); err != nil {
		return nil, err
	}

	s.extractMemory(ctx, msg, input.MemoryHint)
	return msg, nil
}

// extractMemory delegates memory extraction to the MemoryService when one is configured.
// Errors are logged but never returned to the caller — memory extraction is non-fatal.
func (s *eventBusService) extractMemory(ctx context.Context, msg *domain.EventBusMessage, hint *domain.MemoryHint) {
	if s.memoryService == nil {
		return
	}
	if err := s.memoryService.ExtractFromEvent(ctx, msg, hint); err != nil {
		log.Printf("[event_bus_service] WARNING: memory extraction failed for event %s: %v", msg.ID, err)
	}
}

// List returns a paginated list of event bus messages for the given project.
func (s *eventBusService) List(ctx context.Context, projectID uuid.UUID, filter repository.EventBusMessageFilter, pg pagination.Params) (*pagination.Page[domain.EventBusMessage], error) {
	pg.Normalize()
	return s.eventRepo.List(ctx, projectID, filter, pg)
}

// GetContext retrieves context-relevant events from the event bus.
func (s *eventBusService) GetContext(ctx context.Context, projectID uuid.UUID, opts GetContextOptions) ([]domain.EventBusMessage, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	filter := repository.EventBusMessageFilter{
		EventType: opts.EventType,
		AgentID:   opts.AgentID,
		TaskID:    opts.TaskID,
		Tags:      opts.Tags,
	}

	pg := pagination.Params{
		Page:     1,
		PageSize: limit,
		SortBy:   "created_at",
		SortDir:  "desc",
	}

	page, err := s.eventRepo.List(ctx, projectID, filter, pg)
	if err != nil {
		return nil, err
	}

	return page.Items, nil
}

// GetByID retrieves a single event bus message by its ID.
func (s *eventBusService) GetByID(ctx context.Context, id uuid.UUID) (*domain.EventBusMessage, error) {
	msg, err := s.eventRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, apierror.NotFound("EventBusMessage")
	}
	return msg, nil
}

// CleanupExpired removes expired event bus messages and returns the count of deleted records.
func (s *eventBusService) CleanupExpired(ctx context.Context) (int64, error) {
	return s.eventRepo.DeleteExpired(ctx)
}

// resolveSlugs looks up workspace and project slugs from their UUIDs.
func (s *eventBusService) resolveSlugs(ctx context.Context, workspaceID, projectID uuid.UUID) (workspaceSlug, projectSlug string, err error) {
	workspace, err := s.workspaceRepo.GetByID(ctx, workspaceID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get workspace %s: %w", workspaceID, err)
	}
	if workspace == nil {
		return "", "", fmt.Errorf("workspace %s not found", workspaceID)
	}

	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return "", "", fmt.Errorf("failed to get project %s: %w", projectID, err)
	}
	if project == nil {
		return "", "", fmt.Errorf("project %s not found", projectID)
	}

	return workspace.Slug, project.Slug, nil
}
