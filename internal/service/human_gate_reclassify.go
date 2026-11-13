package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// markerGateReclassifier lists the tasks whose live human gate is hard-classified and
// moves one to soft. Implemented by postgres.TaskRepo; kept out of
// repository.TaskRepository so the one sweep that needs it does not widen every mock
// of that interface.
type markerGateReclassifier interface {
	// ListMarkerArmedHardGateIDs lists one page (id > after, at most limit, by id) of
	// the live hard gates whose recorded arm source is a marker.
	ListMarkerArmedHardGateIDs(ctx context.Context, after uuid.UUID, limit int) ([]uuid.UUID, error)
	// ReclassifyHumanGateSoft sets class soft and the recommended default in one
	// statement (and a deadline if the gate had none), only while the gate is still
	// armed hard by the marker comment the caller checked, with its author and reason;
	// ok false when it no longer is, e.g. an API re-arm or a newer re-ask won. Returns
	// the gate's deadline.
	ReclassifyHumanGateSoft(ctx context.Context, taskID, author, marker uuid.UUID, reason, recommendedDefault string) (deadline *time.Time, ok bool, err error)
}

// ReconcileMarkerGateClasses gives every live gate armed by a "❓ Blocking" marker
// the class that marker gets under the current rule (markerGateClass, task
// this change). Gates armed before the rule existed were armed hard for want of a
// "[soft]" tag; this lets their stated default apply instead of leaving them open
// for weeks. Only ever hard→soft: an arm already computes the hard verdict itself,
// and a hard verdict is never weakened here.
//
// A gate is touched only when it provably came from its live marker: the server
// recorded the marker as its arm source (tasks.human_gate_source) and that very comment
// as the one that armed it (human_gate_source_comment_id), the marker's author is the
// gate's author and the gate's stored reason is the one that marker writes. An
// API arm (set_human_gate, explicit class) keeps its class whatever author and reason
// it carries. So does a gate with no recorded source (armed before the column existed,
// or by a raw PATCH flip): nothing proves it came from a marker (the API-arm hint is
// best-effort, so its absence proves nothing), and an unproven gate stays hard; its
// person answers it. The task must also carry a default its asker stated, which the
// default-on-timeout sweep will apply.
//
// Called on the default-on-timeout sweep's tick, before it, so a reclassified gate
// whose deadline has already passed is decided in the same pass.
func (s *commentService) ReconcileMarkerGateClasses(ctx context.Context) (int, error) {
	repo, ok := s.taskRepo.(markerGateReclassifier)
	if !ok || s.commentRepo == nil {
		return 0, nil
	}
	var n int
	after := uuid.Nil
	for {
		ids, err := repo.ListMarkerArmedHardGateIDs(ctx, after, reconcileGatePageSize)
		if err != nil {
			return n, err
		}
		for _, id := range ids {
			if s.reclassifyMarkerGate(ctx, repo, id) {
				n++
			}
		}
		if len(ids) < reconcileGatePageSize {
			return n, nil
		}
		after = ids[len(ids)-1]
	}
}

// reconcileGatePageSize bounds one ListMarkerArmedHardGateIDs read; every page is walked.
var reconcileGatePageSize = 200

func (s *commentService) reclassifyMarkerGate(ctx context.Context, repo markerGateReclassifier, taskID uuid.UUID) bool {
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil || !task.HumanGate || task.HumanGateClass != domain.HumanGateClassHard ||
		task.GateAuthor == nil || task.GateReason == nil {
		return false
	}
	scan, err := s.scanHumanGateOwnership(ctx, taskID, nil)
	if err != nil || !scan.found || scan.threadTruncated || scan.rawArmPrecedesMarker ||
		scan.markerAuthorID != *task.GateAuthor {
		return false
	}
	marker, err := s.commentRepo.GetByID(ctx, scan.markerCommentID)
	if err != nil || marker == nil {
		return false
	}
	slug := ""
	for _, sl := range scan.markerSlugs {
		if gateReasonFromMarker(marker.Body, sl) == *task.GateReason {
			slug = sl
			break
		}
	}
	if slug == "" {
		return false
	}
	class, rule := markerGateClass(marker.Body, slug, task.Labels)
	if class != domain.HumanGateClassSoft {
		return false
	}
	// The default that applies is the one the ask states (markerDefault), whatever the
	// arm stored before that rule: a "Default:" line of the comment's context instead of
	// the ask's own, or the system fallback (or nothing) when the ask's default was inline
	// on the marker line, which the old line-anchored parser missed. The stored value is
	// therefore never a precondition.
	def := markerDefault(marker.Body, slug)
	if def == "" || def == domain.DefaultMarkerRecommendedDefault {
		return false
	}
	// The UPDATE is guarded on this marker's id: a newer re-ask that re-armed the gate in
	// the meantime (same author, same question line, other content) is not judged by
	// what this one says.
	deadline, ok, err := repo.ReclassifyHumanGateSoft(ctx, taskID, *task.GateAuthor, marker.ID, *task.GateReason, def)
	if err != nil || !ok {
		if err != nil {
			log.Printf("[human-gate] WARNING: reclassify task %s soft failed: %v", taskID, err)
		}
		return false
	}
	if task.RecommendedDefault == nil || def != *task.RecommendedDefault {
		// Not the values: a default is free text its author wrote.
		log.Printf("[human-gate] task %s: stored default replaced by the ask's own", taskID)
	}
	task.RecommendedDefault = &def
	task.GateDeadline = deadline
	log.Printf("[human-gate] task %s: gate from marker %s reclassified hard→soft (%s)", taskID, marker.ID, rule)
	s.postGateReclassifiedSoft(ctx, task, rule)
	return true
}

func (s *commentService) postGateReclassifiedSoft(ctx context.Context, task *domain.Task, rule string) {
	deadline := "—"
	if task.GateDeadline != nil {
		deadline = task.GateDeadline.UTC().Format(time.RFC3339)
	}
	now := timeNow()
	c := &domain.Comment{
		ID:           uuid.New(),
		TaskID:       task.ID,
		AuthorID:     systemActorID,
		AuthorType:   domain.ActorTypeSystem,
		SystemCode:   SysCodePtr(SysCodeGateReclassifiedSoft),
		SystemParams: SysParamsJSON(map[string]any{"rule": rule, "deadline": deadline, "default": *task.RecommendedDefault}),
		Body: fmt.Sprintf("🔁 Gate reclassified soft: the question matches no hard rule (%s) and its author stated a default. "+
			"Unless answered first, the default applies at %s: \"%s\".", rule, deadline, *task.RecommendedDefault),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.commentRepo.Create(ctx, c); err != nil {
		log.Printf("[human-gate] WARNING: reclassification notice on task %s failed: %v", task.ID, err)
		return
	}
	if s.ctxCacheInv != nil {
		s.ctxCacheInv.Invalidate(ctx, task.ID)
	}
}
