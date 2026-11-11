package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Page bounds of the turn-outcome read.
const (
	DefaultTurnOutcomeLimit = 20
	MaxTurnOutcomeLimit     = 100
)

// TypedOutcomeService is the runner's side of typed turn outcomes
// (docs/api/typed-outcomes.md): the reads it uses in place of parsing the last
// comment, and the resolve it calls when it observed a wait's event.
type TypedOutcomeService interface {
	ListTurnOutcomes(ctx context.Context, taskID uuid.UUID, sessionID *string, limit int) ([]domain.TurnOutcome, error)
	ActiveWait(ctx context.Context, taskID uuid.UUID, agentID *uuid.UUID) (*domain.TaskWait, error)
	// ResolveWait ends a wait whose event was observed. resolved is false when
	// the wait had already ended with the same resolution (an idempotent repeat).
	ResolveWait(ctx context.Context, taskID, waitID uuid.UUID, by WaitResolver, in ResolveWaitInput) (w *domain.TaskWait, resolved bool, err error)
}

// WaitResolver is the authenticated caller of the resolve route.
type WaitResolver struct {
	AgentID         uuid.UUID
	AuthWorkspaceID uuid.UUID
	Connector       bool
}

// ResolveWaitInput is the body of POST /tasks/:task_id/waits/:wait_id/resolve.
type ResolveWaitInput struct {
	Resolution string `json:"resolution"`
	Cause      string `json:"cause"`
}

// ControllerCheck reports whether the caller is a controller of workspace ws
// (postgres.RuntimeRepo.IsController).
type ControllerCheck func(ctx context.Context, ws uuid.UUID, by WaitResolver) (bool, error)

type typedOutcomeService struct {
	waits       repository.TaskWaitRepository
	outcomes    repository.TurnOutcomeRepository
	controllers ControllerCheck
}

// TypedOutcomeOption configures the service.
type TypedOutcomeOption func(*typedOutcomeService)

// WithControllerCheck lets a workspace's controller resolve any wait in it;
// without it only the wait's own agent may.
func WithControllerCheck(c ControllerCheck) TypedOutcomeOption {
	return func(s *typedOutcomeService) { s.controllers = c }
}

// NewTypedOutcomeService creates the service.
func NewTypedOutcomeService(waits repository.TaskWaitRepository, outcomes repository.TurnOutcomeRepository, opts ...TypedOutcomeOption) TypedOutcomeService {
	s := &typedOutcomeService{waits: waits, outcomes: outcomes}
	for _, o := range opts {
		o(s)
	}
	return s
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

// ResolveWait marks a wait ended by the event the runner observed. Only the
// wait's own agent (the lane key the runner turns with) or a controller of
// the card's workspace (its reporter key) may; a person may not. Idempotent:
// a repeat with the same resolution returns the stored row, another
// resolution of an ended wait is a conflict.
func (s *typedOutcomeService) ResolveWait(ctx context.Context, taskID, waitID uuid.UUID, by WaitResolver, in ResolveWaitInput) (*domain.TaskWait, bool, error) {
	if by.AgentID == uuid.Nil {
		return nil, false, apierror.Forbidden("only the wait's own agent or the workspace's controller resolves a wait")
	}
	res := domain.WaitResolution(strings.TrimSpace(in.Resolution))
	if res == "" {
		res = domain.WaitResolutionMet
	}
	fields := map[string]string{}
	if !res.ResolvableByRoute() {
		fields["resolution"] = "must be met, cancelled, expired or invalid"
	}
	cause := strings.TrimSpace(in.Cause)
	if cause == "" || utf8.RuneCountInString(cause) > domain.MaxResolveCauseLen {
		fields["cause"] = "is required, at most 200 characters: what ended the wait"
	}
	if len(fields) > 0 {
		return nil, false, apierror.ValidationError(fields)
	}
	w, ws, err := s.waits.GetForTask(ctx, taskID, waitID)
	if err != nil {
		return nil, false, err
	}
	if w == nil {
		return nil, false, apierror.NotFound("Wait")
	}
	if by.AgentID != w.AgentID {
		allowed := false
		if s.controllers != nil {
			if allowed, err = s.controllers(ctx, ws, by); err != nil {
				return nil, false, err
			}
		}
		if !allowed {
			return nil, false, apierror.Forbidden("only the wait's own agent or the workspace's controller resolves a wait")
		}
	}
	if w.ResolvedAt == nil {
		var resolved bool
		w, resolved, err = s.waits.ResolveWithCause(ctx, taskID, waitID, res, cause, by.AgentID)
		if err != nil || resolved {
			return w, resolved, err
		}
	}
	if w.Resolution != nil && *w.Resolution == res {
		return w, false, nil
	}
	got := ""
	if w.Resolution != nil {
		got = string(*w.Resolution)
	}
	return nil, false, apierror.Conflict("the wait already ended as " + got)
}
