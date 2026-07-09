package service

import (
	"testing"

	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func TestRuleAppliesTo_UserExemptFromBlockEnforcement(t *testing.T) {
	rule := domain.Rule{
		AppliesToActorTypes: pq.StringArray{}, // empty = all actors
		Enforcement:         domain.RuleEnforcementBlock,
	}
	input := EvaluateInput{ActorType: domain.ActorTypeUser}
	assert.False(t, ruleAppliesTo(rule, input), "user must be exempt from block rules")
}

func TestRuleAppliesTo_UserNotExemptFromWarnEnforcement(t *testing.T) {
	rule := domain.Rule{
		AppliesToActorTypes: pq.StringArray{},
		Enforcement:         domain.RuleEnforcementWarn,
	}
	input := EvaluateInput{ActorType: domain.ActorTypeUser}
	assert.True(t, ruleAppliesTo(rule, input), "user may receive warn-enforcement")
}

func TestRuleAppliesTo_AgentStillBlockedByEmptyActorTypes(t *testing.T) {
	rule := domain.Rule{
		AppliesToActorTypes: pq.StringArray{}, // empty = all actors
		Enforcement:         domain.RuleEnforcementBlock,
	}
	input := EvaluateInput{ActorType: domain.ActorTypeAgent}
	assert.True(t, ruleAppliesTo(rule, input), "agent must still be blocked")
}

func TestRuleAppliesTo_UserExplicitlyInActorTypes_StillExemptFromBlock(t *testing.T) {
	// Hard-exempt applies even if someone explicitly adds "user" to applies_to_actor_types.
	rule := domain.Rule{
		AppliesToActorTypes: pq.StringArray{"user", "agent"},
		Enforcement:         domain.RuleEnforcementBlock,
	}
	input := EvaluateInput{ActorType: domain.ActorTypeUser}
	assert.False(t, ruleAppliesTo(rule, input), "hard-exempt must win even with explicit user in actor types")
}
