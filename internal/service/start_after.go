package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	pkgmetrics "github.com/entire-vc/evc-mesh/pkg/metrics"
)

// StartAfterService is the compare-clear of start_after. Optional capability,
// like ParkedWaitService, so existing adapters and mocks stay intact.
type StartAfterService interface {
	CompareClearStartAfter(context.Context, uuid.UUID, domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error)
}

type startAfterRepository interface {
	CheckStartAfterEvidenceSource(ctx context.Context, workspaceID uuid.UUID, source string, writer uuid.UUID) error
	CompareClearStartAfter(context.Context, uuid.UUID, domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error)
}

// prepareStartAfter is the one validator of a start_after write, called by
// Create, CreateSubtask and Update whatever the transport (REST, MCP via
// REST). existing is nil on create. On success the task carries the new
// date, its origin and the receipt the repository writes with it; on refusal
// nothing has been written.
//
//   - A person's date is human_explicit and needs no evidence.
//   - Anyone else (agent, extension, a caller with no actor) sets a date only
//     as an absolute RFC 3339 instant with Z or an offset, in the future,
//     with evidence of an external event (as a time wait), and can never
//     claim a human origin.
//   - Sending the date the card already has is a no-op, evidence or not.
//   - An explicit null from anyone but a person is refused: clearing is the
//     compare-clear endpoint, which proves it removes the schedule it read.
func (s *taskService) prepareStartAfter(ctx context.Context, existing, task *domain.Task) error {
	var current *time.Time
	if existing != nil {
		current = existing.StartAfter
		task.StartAfterOrigin, task.StartAfterReceiptID = existing.StartAfterOrigin, existing.StartAfterReceiptID
	}
	w := task.StartAfterWrite
	if w == nil {
		if !timePtrChanged(current, task.StartAfter) {
			task.StartAfter = current
			return nil
		}
		// A caller that changed the date without saying how is held to the
		// same rules as one that sent it bare.
		w = &domain.StartAfterWrite{Null: task.StartAfter == nil}
		if task.StartAfter != nil {
			w.Raw = task.StartAfter.Format(time.RFC3339Nano)
		}
	}
	actorID, kind := actorctx.FromContext(ctx)
	human := kind == domain.ActorTypeUser && actorID != uuid.Nil
	origin := domain.StartAfterAgentEvidence
	if human {
		origin = domain.StartAfterHumanExplicit
	}
	err := s.checkStartAfterWrite(ctx, existing, task, w, actorID, kind, human, origin)
	result := "accepted"
	var oe *domain.OutcomeError
	if errors.As(err, &oe) {
		result = oe.Code
	}
	if err == nil && task.StartAfterSet == nil {
		result = "unchanged"
	}
	pkgmetrics.RecordStartAfterWrite(origin, result)
	return err
}

func (s *taskService) checkStartAfterWrite(ctx context.Context, existing, task *domain.Task, w *domain.StartAfterWrite,
	actorID uuid.UUID, kind domain.ActorType, human bool, origin string) error {
	var current *time.Time
	if existing != nil {
		current = existing.StartAfter
	}
	if w.Null {
		task.StartAfter = current
		if current == nil {
			return nil
		}
		if !human {
			return domain.NewOutcomeError(domain.CodeClearNeedsCompare,
				"an agent clears start_after only through POST /tasks/:id/start-after/compare-clear")
		}
		removed := domain.StartAfterLegacyUnknown
		if existing.StartAfterOrigin != nil {
			removed = *existing.StartAfterOrigin
		}
		// A person's clear is audited like a compare-clear: the receipt names
		// the date and schedule it removed.
		id := uuid.New()
		key, reason := "patch:"+id.String(), "cleared by the person in a task update"
		task.StartAfterSet = &domain.StartAfterReceipt{
			ID: id, TaskID: task.ID, Op: "clear", StartAfter: *current, Origin: removed,
			ActorID: actorID, ActorType: kind, ClearedReceiptID: existing.StartAfterReceiptID,
			IdempotencyKey: &key, Reason: &reason,
		}
		task.StartAfter, task.StartAfterOrigin, task.StartAfterReceiptID = nil, nil, nil
		return nil
	}
	at, offset, err := domain.ParseStartAfter(w.Raw, human)
	if err != nil {
		task.StartAfter = current
		return err
	}
	if current != nil && current.Truncate(time.Microsecond).Equal(at.Truncate(time.Microsecond)) {
		task.StartAfter = current
		return nil
	}
	receipt := &domain.StartAfterReceipt{
		ID: uuid.New(), TaskID: task.ID, Op: "set", StartAfter: at, Origin: origin,
		ActorID: actorID, ActorType: kind,
	}
	if receipt.ActorType == "" {
		receipt.ActorType = domain.ActorTypeAgent
	}
	if offset != "" {
		receipt.SourceOffset = &offset
	}
	if !human {
		if w.Origin != "" && w.Origin != domain.StartAfterAgentEvidence {
			return domain.NewOutcomeError(domain.CodeOriginForbidden,
				"start_after_origin "+w.Origin+" cannot be set by an agent; a person's date is set by the person")
		}
		if !at.After(timeNow()) {
			return domain.NewOutcomeError(domain.CodeInvalidTarget, "start_after must be in the future")
		}
		if err := domain.ValidateStartAfterEvidence(w.Evidence); err != nil {
			return err
		}
		if err := s.checkStartAfterSource(ctx, task.ProjectID, w.Evidence.Source, actorID); err != nil {
			return err
		}
		status := domain.TimeEvidenceUnverified
		receipt.EventRef, receipt.Source, receipt.Why = &w.Evidence.EventRef, &w.Evidence.Source, &w.Evidence.Why
		receipt.EvidenceStatus = &status
	}
	task.StartAfter, task.StartAfterOrigin, task.StartAfterReceiptID = &at, &origin, &receipt.ID
	task.StartAfterSet = receipt
	return nil
}

func (s *taskService) checkStartAfterSource(ctx context.Context, projectID uuid.UUID, source string, writer uuid.UUID) error {
	repo, ok := s.taskRepo.(startAfterRepository)
	if !ok {
		// Without the receipt lookup only a URL source can be accepted.
		if kind, _, err := domain.ParseEvidenceSource(source); err != nil || kind != "" {
			return domain.NewOutcomeError(domain.CodeMissingEvidence, "a Mesh receipt source cannot be checked here; give an https URL")
		}
		return nil
	}
	wsID, ok := s.resolveProjectWorkspace(ctx, projectID)
	if !ok {
		return apierror.NotFound("Project")
	}
	return repo.CheckStartAfterEvidenceSource(ctx, wsID, source, writer)
}

// CompareClearStartAfter clears start_after only if it is still the exact
// schedule the caller read (see TaskRepo.CompareClearStartAfter).
func (s *taskService) CompareClearStartAfter(ctx context.Context, id uuid.UUID, in domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error) {
	repo, ok := s.taskRepo.(startAfterRepository)
	if !ok {
		return nil, apierror.ServiceUnavailable("start_after compare-clear unavailable")
	}
	actorID, kind := actorctx.FromContext(ctx)
	if err := in.Validate(kind == domain.ActorTypeUser && actorID != uuid.Nil); err != nil {
		return nil, err
	}
	res, err := repo.CompareClearStartAfter(ctx, id, in)
	if err != nil {
		return nil, err
	}
	if s.ctxCacheInv != nil && !res.Replayed {
		s.ctxCacheInv.Invalidate(ctx, id)
	}
	return res, nil
}
