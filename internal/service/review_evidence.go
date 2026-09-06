package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// midPipelineConfig returns the project's mid-pipeline block, or nil when the
// project has no workflow rules, no mid_pipeline block, or the rules service is
// not wired.
//
// Nil is the OFF answer for every flag on the block (all the accessors are
// nil-safe), which makes "we could not read the config" and "the config says
// off" the same outcome on purpose. That is fail-OPEN, and it is the right
// direction here: both flags tighten a gate the whole fleet moves work through,
// so an unreadable config must not start refusing transitions estate-wide. The
// cost of the opposite choice is a fleet-wide stall on a rules-service blip; the
// cost of this one is that a gate stays as loose as it already is today.
func (s *taskService) midPipelineConfig(ctx context.Context, projectID uuid.UUID) *domain.MidPipelineConfig {
	if s.rulesConfigSvc == nil {
		return nil
	}
	wfResp, err := s.rulesConfigSvc.GetProjectWorkflowRules(ctx, projectID, nil)
	if err != nil || wfResp == nil {
		return nil
	}
	return wfResp.MidPipeline
}

// reviewEvidence is the set of evidence signals a task carries, gathered once so
// the decision below is a pure function of them.
type reviewEvidence struct {
	HasArtifact   bool
	HasVCSLink    bool
	HasPassingDoD bool
	HasAnyComment bool
	HasURLComment bool
}

// hasStrictEvidence reports whether the task carries evidence in the strict
// sense: something a reviewer can actually go and look at.
func (e reviewEvidence) hasStrictEvidence() bool {
	return e.HasArtifact || e.HasVCSLink || e.HasPassingDoD || e.HasURLComment
}

// hasLooseEvidence is the pre-existing gate condition, preserved verbatim: an
// artifact, a VCS link, or any comment at all.
func (e reviewEvidence) hasLooseEvidence() bool {
	return e.HasArtifact || e.HasVCSLink || e.HasAnyComment
}

// hasAnyPassingDoDCheck reports whether at least one DoD gate is recorded as
// passing.
//
// Deliberately "at least one passing", not "all passing" and not "any check
// present at all". A pending check is a check that has not answered yet and a
// failing one is a check that answered no — neither is evidence, and counting
// the mere presence of the map would let a caller satisfy the gate by declaring
// gates it has not run. Requiring ALL of them to pass would be a different rule
// (a definition-of-done gate rather than an evidence gate) and belongs on the
// move to done, where the done-evidence gate already lives.
func hasAnyPassingDoDCheck(checks domain.DodChecks) bool {
	for _, c := range checks {
		if c.Status == domain.DodCheckPass {
			return true
		}
	}
	return false
}

// gatherReviewEvidence collects the evidence signals for a task.
//
// The comment probes are only issued when the cheap fields on the task itself
// have not already settled the question, so the common path (a task with a PR
// linked) costs no extra query.
func (s *taskService) gatherReviewEvidence(ctx context.Context, task *domain.Task, strict bool) reviewEvidence {
	ev := reviewEvidence{
		HasArtifact:   task.ArtifactCount > 0,
		HasVCSLink:    task.VCSLinkCount > 0,
		HasPassingDoD: hasAnyPassingDoDCheck(task.DodChecks),
	}
	if ev.HasArtifact || ev.HasVCSLink {
		return ev
	}
	if s.commentRepo == nil {
		return ev
	}

	if strict {
		if ev.HasPassingDoD {
			return ev
		}
		// A read error leaves HasURLComment false, which would REFUSE the move.
		// Flip it to true instead: "we could not look" must not be reported as
		// "we looked and there was nothing". The same reasoning as the fail-open
		// config read above, applied one level down.
		hasURL, err := s.commentRepo.HasCommentWithURL(ctx, task.ID)
		if err != nil {
			ev.HasURLComment = true
			return ev
		}
		ev.HasURLComment = hasURL
		return ev
	}

	hasAny, err := s.commentRepo.HasAnyComment(ctx, task.ID)
	if err != nil {
		// Preserves the pre-existing behaviour exactly: the old gate discarded
		// the error and only refused when the probe positively answered "no".
		ev.HasAnyComment = true
		return ev
	}
	ev.HasAnyComment = hasAny
	return ev
}

// passesReviewEvidenceGate reports whether the task may move to review, and
// which condition decided it.
//
// Strict is returned rather than re-derived by the caller so the config is read
// once per move instead of twice on the refusal path — and, more importantly, so
// the message the caller renders cannot describe a different condition from the
// one that actually refused, which a second read could produce if the config
// changed in between.
func (s *taskService) passesReviewEvidenceGate(ctx context.Context, task *domain.Task) (ok, strict bool) {
	strict = s.midPipelineConfig(ctx, task.ProjectID).ReviewStrict()
	ev := s.gatherReviewEvidence(ctx, task, strict)
	if strict {
		return ev.hasStrictEvidence(), true
	}
	return ev.hasLooseEvidence(), false
}
