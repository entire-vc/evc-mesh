package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Page bounds of the turn-outcome read.
const (
	DefaultTurnOutcomeLimit = 20
	MaxTurnOutcomeLimit     = 100
)

// TypedOutcomeService is the read side of typed turn outcomes the runner uses
// in place of parsing the last comment (docs/api/typed-outcomes.md). The
// commands that write waits, outcomes and decision requests come later.
type TypedOutcomeService interface {
	ListTurnOutcomes(ctx context.Context, taskID uuid.UUID, sessionID *string, limit int) ([]domain.TurnOutcome, error)
	ActiveWait(ctx context.Context, taskID uuid.UUID, agentID *uuid.UUID) (*domain.TaskWait, error)
}

type typedOutcomeService struct {
	waits    repository.TaskWaitRepository
	outcomes repository.TurnOutcomeRepository
}

// NewTypedOutcomeService creates the service.
func NewTypedOutcomeService(waits repository.TaskWaitRepository, outcomes repository.TurnOutcomeRepository) TypedOutcomeService {
	return &typedOutcomeService{waits: waits, outcomes: outcomes}
}

func (s *typedOutcomeService) ListTurnOutcomes(ctx context.Context, taskID uuid.UUID, sessionID *string, limit int) ([]domain.TurnOutcome, error) {
	if limit <= 0 {
		limit = DefaultTurnOutcomeLimit
	}
	if limit > MaxTurnOutcomeLimit {
		limit = MaxTurnOutcomeLimit
	}
	return s.outcomes.ListByTask(ctx, taskID, sessionID, limit)
}

func (s *typedOutcomeService) ActiveWait(ctx context.Context, taskID uuid.UUID, agentID *uuid.UUID) (*domain.TaskWait, error) {
	return s.waits.GetActive(ctx, taskID, agentID)
}
