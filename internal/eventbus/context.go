package eventbus

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// ProjectContext is a structured summary of events for a project,
// grouped by category for use by the get_context MCP tool.
type ProjectContext struct {
	Summaries []domain.EventBusMessage `json:"summaries"`
	Decisions []domain.EventBusMessage `json:"decisions"`
	Blockers  []domain.EventBusMessage `json:"blockers"`
	Recent    []domain.EventBusMessage `json:"recent"`
}

// GetContextOptions defines options for retrieving project context.
type GetContextOptions struct {
	TaskID    *uuid.UUID
	AgentID   *uuid.UUID
	EventType *domain.EventType
	Tags      []string
	Limit     int
}

// GetProjectContext reads events from PostgreSQL with filters and groups them
// into summaries, decisions (status_change, context_update), blockers (error),
// and recent events.
func (eb *EventBus) GetProjectContext(ctx context.Context, projectID uuid.UUID, opts GetContextOptions) (*ProjectContext, error) {
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

	page, err := eb.repo.List(ctx, projectID, filter, pg)
	if err != nil {
		return nil, err
	}

	result := &ProjectContext{
		Summaries: []domain.EventBusMessage{},
		Decisions: []domain.EventBusMessage{},
		Blockers:  []domain.EventBusMessage{},
		Recent:    []domain.EventBusMessage{},
	}

	for _, msg := range page.Items {
		switch msg.EventType {
		case domain.EventTypeSummary:
			result.Summaries = append(result.Summaries, msg)
		case domain.EventTypeStatusChange, domain.EventTypeContextUpdate:
			result.Decisions = append(result.Decisions, msg)
		case domain.EventTypeError:
			result.Blockers = append(result.Blockers, msg)
		default:
			result.Recent = append(result.Recent, msg)
		}
	}

	return result, nil
}
