package service

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Optional capability keeps existing task writers and repository adapters intact.
type ParkedWaitService interface {
	RegisterParkedWait(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error)
	ReleaseParkedWait(context.Context, uuid.UUID, domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error)
}

type parkedWaitRepository interface {
	RegisterParkedWait(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error)
	GetParkedWait(context.Context, uuid.UUID, uuid.UUID) (*domain.RegisteredParkedWait, error)
	ReleaseParkedWait(context.Context, uuid.UUID, domain.ReleaseParkedWait, string) (*domain.ParkedWaitResult, error)
}

func (s *taskService) RegisterParkedWait(ctx context.Context, id uuid.UUID, plan domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
	repo, ok := s.taskRepo.(parkedWaitRepository)
	if !ok {
		return nil, apierror.ServiceUnavailable("parked wait registration unavailable")
	}
	if _, err := repo.GetParkedWait(ctx, id, plan.ID); err == nil {
		return repo.RegisterParkedWait(ctx, id, plan)
	}
	if checkErr := s.checkParkedHumanGate(ctx, id, plan.ExpectedVersion); checkErr != nil {
		return nil, checkErr
	}
	return repo.RegisterParkedWait(ctx, id, plan)
}

// The human-policy read must match the exact version subsequently locked by
// the repository. A policy mutation in between makes the transaction conflict.
func (s *taskService) checkParkedHumanGate(ctx context.Context, id uuid.UUID, version int64) error {
	task, err := s.taskRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if task.Version != version || humanGateReason(task) != "" {
		return apierror.Conflict("parked wait snapshot changed or human gate is armed")
	}
	return nil
}

func (s *taskService) ReleaseParkedWait(ctx context.Context, id uuid.UUID, input domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error) {
	repo, ok := s.taskRepo.(parkedWaitRepository)
	if !ok {
		return nil, apierror.ServiceUnavailable("parked wait release unavailable")
	}
	registered, err := repo.GetParkedWait(ctx, id, input.RegistrationID)
	if err != nil {
		return nil, err
	}
	status := ""
	// A committed receipt is replayable even after another repark or loss of
	// external connectivity. The repository still verifies actor/request equality.
	if !registered.Result.Released {
		if checkErr := s.checkParkedHumanGate(ctx, id, input.ExpectedVersion); checkErr != nil {
			return nil, checkErr
		}
		if registered.Plan.Reason == "pipeline" {
			ws, ok := s.resolveProjectWorkspace(ctx, registered.Plan.ProjectID)
			if !ok {
				return nil, apierror.ServiceUnavailable("pipeline workspace unavailable")
			}
			checker, ok := s.resolveGitLabChecker(ctx, ws)
			if !ok {
				return nil, apierror.ServiceUnavailable("GitLab verification unavailable")
			}
			pipeline, ok := checker.(interface {
				GetPipelineStatus(context.Context, string, int) (string, error)
			})
			if !ok {
				return nil, apierror.ServiceUnavailable("GitLab pipeline verification unavailable")
			}
			status, err = pipeline.GetPipelineStatus(ctx, registered.Plan.Condition.ProjectPath, registered.Plan.Condition.PipelineID)
			if err != nil {
				return nil, apierror.ServiceUnavailable("GitLab pipeline verification failed")
			}
			if !slices.Contains([]string{"success", "failed", "canceled", "skipped"}, status) {
				return nil, apierror.Conflict("pipeline is not terminal")
			}
		}
	}
	result, err := repo.ReleaseParkedWait(ctx, id, input, status)
	if err != nil || result.Replayed {
		return result, err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, id)
	}
	// Activity is already persisted atomically by the repository. Publish the
	// usual notification without creating a second audit row.
	if task, err := s.taskRepo.GetByID(ctx, id); err == nil {
		changes := map[string]any{"source": "parked-wait-api", "reason": registered.Plan.Reason, "registration_id": result.RegistrationID, "activity_id": result.ActivityID, "trigger": input.Trigger}
		actor, kind := actorctx.FromContext(ctx)
		if ws, ok := s.resolveProjectWorkspace(ctx, task.ProjectID); ok {
			s.publishTaskEvent(ctx, ws, task.ProjectID, id, actor, kind, "task.moved", changes)
		}
		s.notifyAssignedAgent(ctx, task, "task.status_changed", changes)
	}
	return result, nil
}
