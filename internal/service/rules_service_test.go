package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// --------------------------------------------------------------------------
// isAgentAllowedByActors
// --------------------------------------------------------------------------

func TestIsAgentAllowedByActors_EmptyAllowsAll(t *testing.T) {
	assert.True(t, isAgentAllowedByActors(nil), "nil slice should allow-all")
	assert.True(t, isAgentAllowedByActors([]string{}), "empty slice should allow-all")
}

func TestIsAgentAllowedByActors_Wildcard(t *testing.T) {
	assert.True(t, isAgentAllowedByActors([]string{"*"}))
	assert.True(t, isAgentAllowedByActors([]string{"user", "*"}))
}

func TestIsAgentAllowedByActors_AgentExplicit(t *testing.T) {
	assert.True(t, isAgentAllowedByActors([]string{"agent"}))
	assert.True(t, isAgentAllowedByActors([]string{"user", "agent", "system"}))
}

func TestIsAgentAllowedByActors_UserOrSystemOnly(t *testing.T) {
	assert.False(t, isAgentAllowedByActors([]string{"user"}))
	assert.False(t, isAgentAllowedByActors([]string{"system"}))
	assert.False(t, isAgentAllowedByActors([]string{"user", "system"}))
}

// --------------------------------------------------------------------------
// computeAgentPermissions
// --------------------------------------------------------------------------

func makeTestAgent(role string) *domain.Agent {
	return &domain.Agent{
		ID:   uuid.New(),
		Name: "test-agent",
		Slug: "test-agent",
		Role: role,
	}
}

// TestComputeAgentPermissions_EmptyAllowedActors is the primary regression test:
// empty AllowedActors must yield can_transition=true (allow-all), not false.
func TestComputeAgentPermissions_EmptyAllowedActors(t *testing.T) {
	agent := makeTestAgent("lead")
	cfg := domain.WorkflowRulesConfig{
		Transitions: map[string]domain.TransitionRule{
			"backlog":     {Allowed: []string{"todo", "in_progress"}, AllowedActors: []string{}},
			"in_progress": {Allowed: []string{"review", "backlog"}, AllowedActors: nil},
			"done":        {Allowed: []string{"backlog"}, AllowedActors: []string{}},
		},
	}

	perms := computeAgentPermissions(agent, cfg)

	assert.True(t, perms.CanTransition["backlog"], "empty AllowedActors should allow-all")
	assert.True(t, perms.CanTransition["in_progress"], "nil AllowedActors should allow-all")
	assert.True(t, perms.CanTransition["done"], "empty AllowedActors should allow-all")
}

func TestComputeAgentPermissions_AgentExplicitlyAllowed(t *testing.T) {
	agent := makeTestAgent("developer")
	cfg := domain.WorkflowRulesConfig{
		Transitions: map[string]domain.TransitionRule{
			"todo": {Allowed: []string{"in_progress"}, AllowedActors: []string{"agent"}},
		},
	}

	perms := computeAgentPermissions(agent, cfg)
	assert.True(t, perms.CanTransition["todo"])
}

func TestComputeAgentPermissions_WildcardAllowsAgent(t *testing.T) {
	agent := makeTestAgent("reviewer")
	cfg := domain.WorkflowRulesConfig{
		Transitions: map[string]domain.TransitionRule{
			"review": {Allowed: []string{"done", "todo"}, AllowedActors: []string{"*"}},
		},
	}

	perms := computeAgentPermissions(agent, cfg)
	assert.True(t, perms.CanTransition["review"])
}

func TestComputeAgentPermissions_UserOnlyBlocksAgent(t *testing.T) {
	agent := makeTestAgent("lead")
	cfg := domain.WorkflowRulesConfig{
		Transitions: map[string]domain.TransitionRule{
			"backlog": {Allowed: []string{"todo"}, AllowedActors: []string{"user"}},
		},
	}

	perms := computeAgentPermissions(agent, cfg)
	assert.False(t, perms.CanTransition["backlog"], "user-only transition should block agent")
}

func TestComputeAgentPermissions_NoTransitionsEmptyMap(t *testing.T) {
	agent := makeTestAgent("lead")
	cfg := domain.WorkflowRulesConfig{}

	perms := computeAgentPermissions(agent, cfg)
	assert.Empty(t, perms.CanTransition, "no transitions configured → empty map")
	assert.Equal(t, "lead", perms.MyRole)
}

func TestComputeAgentPermissions_PolicyAllowed(t *testing.T) {
	agent := makeTestAgent("lead Mesh")
	cfg := domain.WorkflowRulesConfig{
		Policies: map[string]domain.PolicyRule{
			"create_tasks": {Allowed: []string{"*"}},
			"reassign":     {Allowed: []string{"role:lead Mesh"}},
			"delete_tasks": {Allowed: []string{"user"}},
		},
	}

	perms := computeAgentPermissions(agent, cfg)
	assert.True(t, perms.CanCreateTasks)
	assert.True(t, perms.CanReassign)
	assert.False(t, perms.CanDeleteTasks)
}
