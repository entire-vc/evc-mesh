package service

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	gitlabapi "github.com/entire-vc/evc-mesh/internal/integration/gitlab"
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
	ReleaseParkedWaitVerified(context.Context, uuid.UUID, domain.ReleaseParkedWait, func(context.Context, *domain.Task, domain.ParkedWaitPlan) (string, error)) (*domain.ParkedWaitResult, error)
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
	// Load configuration before reserving the release transaction's connection.
	// Do not contact the provider here. Configuration failures are deferred until
	// verify is invoked, so a concurrent committed receipt can still replay.
	var checker gitlabapi.MergeRequestChecker
	var checkerErr error
	if !registered.Result.Released && registered.Plan.Reason == "pipeline" {
		if ws, ok := s.resolveProjectWorkspace(ctx, registered.Plan.ProjectID); !ok {
			checkerErr = apierror.ServiceUnavailable("pipeline workspace unavailable")
		} else if checker, ok = s.resolveGitLabChecker(ctx, ws); !ok {
			checkerErr = apierror.ServiceUnavailable("GitLab verification unavailable")
		}
	}
	result, err := repo.ReleaseParkedWaitVerified(ctx, id, input, func(ctx context.Context, task *domain.Task, plan domain.ParkedWaitPlan) (string, error) {
		if task.Version != input.ExpectedVersion || humanGateReason(task) != "" {
			return "", apierror.Conflict("parked wait snapshot changed or human gate is armed")
		}
		status := ""
		if plan.Reason == "pipeline" {
			if plan.ProjectID != registered.Plan.ProjectID {
				return "", apierror.Conflict("parked wait snapshot changed")
			}
			if checkerErr != nil {
				return "", checkerErr
			}
			if checker == nil {
				return "", apierror.ServiceUnavailable("GitLab verification unavailable")
			}
			if plan.Condition.RequiredJobs != nil {
				jobs, ok := checker.(interface {
					PipelineRequiredJobsSucceeded(context.Context, string, int, []string) (bool, error)
				})
				if !ok {
					return "", apierror.ServiceUnavailable("GitLab jobs verification unavailable")
				}
				ready, checkErr := jobs.PipelineRequiredJobsSucceeded(ctx, plan.Condition.ProjectPath, plan.Condition.PipelineID, plan.Condition.RequiredJobs)
				if checkErr != nil {
					return "", apierror.ServiceUnavailable("GitLab jobs verification failed")
				}
				if !ready {
					return "", apierror.Conflict("required pipeline jobs have not succeeded")
				}
				status = "success"
			} else {
				pipeline, ok := checker.(interface {
					GetPipelineStatus(context.Context, string, int) (string, error)
				})
				if !ok {
					return "", apierror.ServiceUnavailable("GitLab pipeline verification unavailable")
				}
				pipelineStatus, pipelineErr := pipeline.GetPipelineStatus(ctx, plan.Condition.ProjectPath, plan.Condition.PipelineID)
				if pipelineErr != nil {
					return "", apierror.ServiceUnavailable("GitLab pipeline verification failed")
				}
				status = pipelineStatus
				if !slices.Contains([]string{"success", "failed", "canceled", "skipped"}, status) {
					return "", apierror.Conflict("pipeline is not terminal")
				}
			}
		}
		return status, nil
	})
	if err != nil || result.Replayed {
		return result, err
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, id)
	}
	// Activity is already persisted atomically by the repository. Publish the
	// usual notification without creating a second audit row.
	if task, err := s.taskRepo.GetByID(ctx, id); err == nil {
		changes := map[string]any{"source": "parked-wait-api", "reason": input.Trigger.Kind, "registration_id": result.RegistrationID, "activity_id": result.ActivityID, "trigger": input.Trigger}
		actor, kind := actorctx.FromContext(ctx)
		if ws, ok := s.resolveProjectWorkspace(ctx, task.ProjectID); ok {
			s.publishTaskEvent(ctx, ws, task.ProjectID, id, actor, kind, "task.moved", changes)
		}
		s.notifyAssignedAgent(ctx, task, "task.status_changed", changes)
	}
	return result, nil
}
