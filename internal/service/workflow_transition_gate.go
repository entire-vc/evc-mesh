package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// applyTransitionGate checks WorkflowRulesConfig.Transitions for the status move.
// Returns enforcement mode and a violation message; empty violation = allowed.
// If rulesConfigSvc is nil or config is empty, returns advisory + no violation (allow-all).
func (s *taskService) applyTransitionGate(ctx context.Context, task *domain.Task, oldStatusID uuid.UUID, newStatus *domain.TaskStatus) (enfMode, violation string) {
	if s.rulesConfigSvc == nil {
		return domain.RuleConfigEnforcementAdvisory, ""
	}
	wfResp, err := s.rulesConfigSvc.GetProjectWorkflowRules(ctx, task.ProjectID, nil)
	if err != nil || wfResp == nil || len(wfResp.Transitions) == 0 {
		return domain.RuleConfigEnforcementAdvisory, ""
	}

	oldStatus, err := s.statusRepo.GetByID(ctx, oldStatusID)
	if err != nil || oldStatus == nil {
		return domain.RuleConfigEnforcementAdvisory, ""
	}

	_, actorType := actorctx.FromContext(ctx)
	return checkTransitionGate(oldStatus.Name, newStatus.Name, actorType, wfResp.WorkflowRulesConfig)
}

// checkTransitionGate is the pure-function core of the transition gate.
// Returns enforcement mode and violation message; empty violation = allowed.
// Empty Transitions map → allow-all (backward-compatible).
func checkTransitionGate(fromStatus, toStatus string, actorType domain.ActorType, cfg domain.WorkflowRulesConfig) (enfMode, violation string) {
	enfMode = cfg.EnforcementMode
	if enfMode == "" {
		enfMode = domain.RuleConfigEnforcementAdvisory
	}

	if len(cfg.Transitions) == 0 {
		return enfMode, ""
	}

	// System actors are exempt from all transition enforcement by default.
	if actorType == domain.ActorTypeSystem && !cfg.EnforceSystemActors {
		return enfMode, ""
	}

	tr, ok := cfg.Transitions[fromStatus]
	if !ok {
		// No rule defined for this from-status → no restriction.
		return enfMode, ""
	}

	// (a) Allowed target statuses.
	if len(tr.Allowed) > 0 {
		found := false
		for _, a := range tr.Allowed {
			if a == toStatus {
				found = true
				break
			}
		}
		if !found {
			return enfMode, fmt.Sprintf(
				"transition %q→%q not permitted; allowed targets: [%s]",
				fromStatus, toStatus, strings.Join(tr.Allowed, ", "),
			)
		}
	}

	// (b) Actor restriction via AllowedActors patterns.
	if len(tr.AllowedActors) > 0 {
		if !isTransitionActorAllowed(actorType, tr.AllowedActors) {
			return enfMode, fmt.Sprintf(
				"actor type %q not permitted for transition %q→%q",
				actorType, fromStatus, toStatus,
			)
		}
	}

	return enfMode, ""
}

// isTransitionActorAllowed checks if actorType matches any allowed actor pattern.
// Supported patterns: "*" (any), "user", "agent", "system".
func isTransitionActorAllowed(actorType domain.ActorType, allowed []string) bool {
	s := string(actorType)
	for _, p := range allowed {
		if p == "*" || p == s {
			return true
		}
	}
	return false
}
