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

	perms := computeAgentPermissions(agent, cfg, nil)

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

	perms := computeAgentPermissions(agent, cfg, nil)
	assert.True(t, perms.CanTransition["todo"])
}

func TestComputeAgentPermissions_WildcardAllowsAgent(t *testing.T) {
	agent := makeTestAgent("reviewer")
	cfg := domain.WorkflowRulesConfig{
		Transitions: map[string]domain.TransitionRule{
			"review": {Allowed: []string{"done", "todo"}, AllowedActors: []string{"*"}},
		},
	}

	perms := computeAgentPermissions(agent, cfg, nil)
	assert.True(t, perms.CanTransition["review"])
}

func TestComputeAgentPermissions_UserOnlyBlocksAgent(t *testing.T) {
	agent := makeTestAgent("lead")
	cfg := domain.WorkflowRulesConfig{
		Transitions: map[string]domain.TransitionRule{
			"backlog": {Allowed: []string{"todo"}, AllowedActors: []string{"user"}},
		},
	}

	perms := computeAgentPermissions(agent, cfg, nil)
	assert.False(t, perms.CanTransition["backlog"], "user-only transition should block agent")
}

// TestComputeAgentPermissions_NoTransitionsNoStatusesKnown covers the case where
// the caller has no project status list to expand against (e.g. statusRepo not
// wired) — can_transition degrades to an empty map rather than erroring, but the
// advisory booleans still default to true (see EmptyConfigAllowsAll below for
// the fully-informed case).
func TestComputeAgentPermissions_NoTransitionsNoStatusesKnown(t *testing.T) {
	agent := makeTestAgent("lead")
	cfg := domain.WorkflowRulesConfig{}

	perms := computeAgentPermissions(agent, cfg, nil)
	assert.Empty(t, perms.CanTransition, "no statuses supplied → nothing to populate")
	assert.Equal(t, "lead", perms.MyRole)
	assert.True(t, perms.CanCreateTasks)
	assert.True(t, perms.CanDeleteTasks)
	assert.True(t, perms.CanReassign)
}

// TestComputeAgentPermissions_EmptyConfigAllowsAll is the primary regression test
// for #d9ec930b: a project with no workflow_rules row must report can_transition
// populated true for every real status, not an empty map that reads as "blocked
// from everywhere" — checkTransitionGate already treats this config as allow-all,
// my_permissions must not contradict it.
func TestComputeAgentPermissions_EmptyConfigAllowsAll(t *testing.T) {
	agent := makeTestAgent("lead")
	cfg := domain.WorkflowRulesConfig{}
	statuses := []domain.TaskStatus{
		{Slug: "backlog"},
		{Slug: "todo"},
		{Slug: "in_progress"},
		{Slug: "review"},
		{Slug: "done"},
	}

	perms := computeAgentPermissions(agent, cfg, statuses)

	assert.Len(t, perms.CanTransition, len(statuses))
	for _, st := range statuses {
		assert.True(t, perms.CanTransition[st.Slug], "status %q should allow transitions when no workflow config exists", st.Slug)
	}
	assert.True(t, perms.CanCreateTasks)
	assert.True(t, perms.CanDeleteTasks)
	assert.True(t, perms.CanReassign)
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

	perms := computeAgentPermissions(agent, cfg, nil)
	assert.True(t, perms.CanCreateTasks)
	assert.True(t, perms.CanReassign)
	assert.False(t, perms.CanDeleteTasks)
}

// TestComputeAgentPermissions_UnconfiguredPolicyDefaultsAllowed verifies that a
// non-empty Transitions config with no matching Policies entry still defaults
// the advisory create/delete/reassign booleans to true — these have no real
// enforcement anywhere in the codebase, so false misleadingly reads as a wall.
func TestComputeAgentPermissions_UnconfiguredPolicyDefaultsAllowed(t *testing.T) {
	agent := makeTestAgent("developer")
	cfg := domain.WorkflowRulesConfig{
		Transitions: map[string]domain.TransitionRule{
			"todo": {Allowed: []string{"in_progress"}, AllowedActors: []string{"agent"}},
		},
	}

	perms := computeAgentPermissions(agent, cfg, nil)
	assert.True(t, perms.CanCreateTasks)
	assert.True(t, perms.CanDeleteTasks)
	assert.True(t, perms.CanReassign)
}
