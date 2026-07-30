package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// noopMemberRepo is a minimal repository.WorkspaceMemberRepository stub for
// tests that exercise importTeamConfig's agent path and never touch humans.
type noopMemberRepo struct{}

func (noopMemberRepo) Create(context.Context, *domain.WorkspaceMember) error { return nil }
func (noopMemberRepo) GetByWorkspaceAndUser(context.Context, uuid.UUID, uuid.UUID) (*domain.WorkspaceMember, error) {
	return nil, nil
}
func (noopMemberRepo) GetRole(context.Context, uuid.UUID, uuid.UUID) (string, error) { return "", nil }
func (noopMemberRepo) List(context.Context, uuid.UUID) ([]domain.WorkspaceMemberWithUser, error) {
	return nil, nil
}
func (noopMemberRepo) ListWithProjects(context.Context, uuid.UUID) ([]repository.HumanWithProjects, error) {
	return nil, nil
}
func (noopMemberRepo) UpdateRole(context.Context, uuid.UUID, uuid.UUID, string) error { return nil }
func (noopMemberRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error             { return nil }
func (noopMemberRepo) CountOwners(context.Context, uuid.UUID) (int, error)            { return 0, nil }

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

// --------------------------------------------------------------------------
// UpdateAgentProfile — partial-update (PATCH-semantics) regression
// --------------------------------------------------------------------------

// strPtr and intPtr are tiny helpers for building AgentProfileUpdate literals
// in tests, mirroring how a real caller sends only the fields it means to change.
func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// TestUpdateAgentProfile_OmittedFieldsAreNotZeroed pins the documented
// behavior of domain.AgentProfileUpdate: a request that only sets one field
// must leave every other existing field untouched, not blank it out. This is
// the regression test for the "full-replace footgun" described in task #7acd2497 —
// previously every field was assigned unconditionally, so a caller who forgot
// to resend e.g. working_hours would silently wipe it.
func TestUpdateAgentProfile_OmittedFieldsAreNotZeroed(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	svc := NewRulesService(nil, nil, nil, agentRepo, nil, nil, nil)

	agentID := uuid.New()
	original := &domain.Agent{
		ID:                 agentID,
		Name:               "test-agent",
		Slug:               "test-agent",
		Role:               "developer",
		ResponsibilityZone: "backend",
		MaxConcurrentTasks: 3,
		WorkingHours:       "09:00-17:00",
		ProfileDescription: "Original description",
	}
	require.NoError(t, agentRepo.Create(context.Background(), original))

	// Only ProfileDescription is being changed; every other field is omitted.
	update := domain.AgentProfileUpdate{
		ProfileDescription: strPtr("Updated description"),
	}
	err := svc.UpdateAgentProfile(context.Background(), agentID, update)
	require.NoError(t, err)

	got, err := agentRepo.GetByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, "Updated description", got.ProfileDescription, "the field actually sent should change")
	assert.Equal(t, "developer", got.Role, "Role must survive an update that didn't mention it")
	assert.Equal(t, "backend", got.ResponsibilityZone, "ResponsibilityZone must survive an update that didn't mention it")
	assert.Equal(t, 3, got.MaxConcurrentTasks, "MaxConcurrentTasks must survive an update that didn't mention it")
	assert.Equal(t, "09:00-17:00", got.WorkingHours, "WorkingHours must survive an update that didn't mention it")
}

// TestUpdateAgentProfile_ExplicitEmptyStringClears confirms a caller can still
// intentionally clear a string field by sending an explicit empty string —
// the partial-update semantics distinguish "omitted" (nil pointer) from
// "explicitly set to empty" (non-nil pointer to "").
func TestUpdateAgentProfile_ExplicitEmptyStringClears(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	svc := NewRulesService(nil, nil, nil, agentRepo, nil, nil, nil)

	agentID := uuid.New()
	require.NoError(t, agentRepo.Create(context.Background(), &domain.Agent{
		ID:           agentID,
		Name:         "test-agent",
		Slug:         "test-agent",
		WorkingHours: "09:00-17:00",
	}))

	err := svc.UpdateAgentProfile(context.Background(), agentID, domain.AgentProfileUpdate{
		WorkingHours: strPtr(""),
	})
	require.NoError(t, err)

	got, err := agentRepo.GetByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, "", got.WorkingHours, "an explicit empty string must still clear the field")
}

// TestUpdateAgentProfile_AllFieldsSetAppliesEverything verifies the ordinary
// "send the full profile" path still works — the legitimate way to use this
// endpoint as a full replace.
func TestUpdateAgentProfile_AllFieldsSetAppliesEverything(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	svc := NewRulesService(nil, nil, nil, agentRepo, nil, nil, nil)

	agentID := uuid.New()
	require.NoError(t, agentRepo.Create(context.Background(), &domain.Agent{
		ID:   agentID,
		Name: "test-agent",
		Slug: "test-agent",
	}))

	err := svc.UpdateAgentProfile(context.Background(), agentID, domain.AgentProfileUpdate{
		Role:               strPtr("lead"),
		ResponsibilityZone: strPtr("infra"),
		MaxConcurrentTasks: intPtr(5),
		WorkingHours:       strPtr("10:00-18:00"),
		ProfileDescription: strPtr("Full replace"),
	})
	require.NoError(t, err)

	got, err := agentRepo.GetByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, "lead", got.Role)
	assert.Equal(t, "infra", got.ResponsibilityZone)
	assert.Equal(t, 5, got.MaxConcurrentTasks)
	assert.Equal(t, "10:00-18:00", got.WorkingHours)
	assert.Equal(t, "Full replace", got.ProfileDescription)
}

// TestImportTeamConfig_AgentProfile_AppliesAllFields exercises the
// importTeamConfig → UpdateAgentProfile call site directly (via the exported
// ImportTeam), which builds an AgentProfileUpdate by taking the address of
// each TeamAgentConfig field. This is the regression test for that
// construction: every pointer field must actually reach the agent.
func TestImportTeamConfig_AgentProfile_AppliesAllFields(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	svc := NewRulesService(nil, nil, nil, agentRepo, noopMemberRepo{}, nil, nil)

	wsID := uuid.New()
	agentID := uuid.New()
	require.NoError(t, agentRepo.Create(context.Background(), &domain.Agent{
		ID:          agentID,
		WorkspaceID: wsID,
		Name:        "TestAgent",
		Slug:        "test-agent",
		Role:        "old-role",
	}))

	yamlData := []byte(`
agents:
  - name: TestAgent
    role: developer
    responsibility_zone: backend
    capabilities: [go, testing]
    accepts_from: ["*"]
    max_concurrent_tasks: 4
    working_hours: "09:00-17:00"
    description: "Updated via team import"
`)

	result, err := svc.ImportTeam(context.Background(), wsID, yamlData)
	require.NoError(t, err)
	assert.Empty(t, result.Errors)
	assert.Equal(t, 1, result.AgentsUpdated)

	got, err := agentRepo.GetByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, "developer", got.Role)
	assert.Equal(t, "backend", got.ResponsibilityZone)
	assert.Equal(t, 4, got.MaxConcurrentTasks)
	assert.Equal(t, "09:00-17:00", got.WorkingHours)
	assert.Equal(t, "Updated via team import", got.ProfileDescription)
	assert.JSONEq(t, `["go","testing"]`, string(got.Capabilities))
}

// TestImportTeamConfig_AgentNotFound_RecordsError verifies the "not found"
// branch of the same loop still reports an error and does not touch AgentsUpdated.
func TestImportTeamConfig_AgentNotFound_RecordsError(t *testing.T) {
	agentRepo := NewMockAgentRepository()
	svc := NewRulesService(nil, nil, nil, agentRepo, noopMemberRepo{}, nil, nil)

	wsID := uuid.New()
	yamlData := []byte(`
agents:
  - name: GhostAgent
    role: developer
`)

	result, err := svc.ImportTeam(context.Background(), wsID, yamlData)
	require.NoError(t, err)
	assert.Equal(t, 0, result.AgentsUpdated)
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "GhostAgent")
	assert.Contains(t, result.Errors[0], "not found")
}
