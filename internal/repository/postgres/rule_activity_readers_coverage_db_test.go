package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Readers on rules, agent_activity_log and event_bus_messages. Every query
// below was rewritten from `SELECT *` (bare or `e.*`-qualified) by this change
// and had no coverage at this package's level.
//
// rules is the sharpest case in the package: its readers scan via
// rows.StructScan inside scanRules, which is exactly as intolerant of an
// unknown column as GetContext/SelectContext are, and the table carries three
// scopes whose queries differ only in the column they key on.
//
// See select_star_coverage_fixtures_db_test.go for why these assert on field
// values rather than on row counts.

// ---------------------------------------------------------------------------
// rule_repo.go: GetByID, ListByWorkspace, ListByProject, ListByAgent, GetEffective
// ---------------------------------------------------------------------------

// seedRule writes one rules row at the requested scope. The table's CHECK
// constraints pin which of project_id/agent_id may be set per scope, so the
// caller passes the scope and this fills in the rest consistently.
func seedRule(t *testing.T, db *sqlx.DB, fx coverageFixture, scope domain.RuleScope, name string, enabled bool) *domain.Rule {
	t.Helper()
	rule := &domain.Rule{
		ID:                  uuid.New(),
		WorkspaceID:         fx.workspaceID,
		Scope:               scope,
		RuleType:            "task.capacity_limit",
		Name:                name,
		Description:         "description for " + name,
		Config:              json.RawMessage(`{"max_tasks": 5}`),
		AppliesToActorTypes: pq.StringArray{"agent"},
		AppliesToRoles:      pq.StringArray{"developer"},
		Enforcement:         domain.RuleEnforcementWarn,
		Priority:            42,
		IsEnabled:           enabled,
		CreatedBy:           fx.userID,
		CreatedByType:       domain.ActorTypeUser,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	switch scope {
	case domain.RuleScopeProject:
		// chk_rules_scope_project: project_id NOT NULL and agent_id NULL.
		rule.ProjectID = &fx.projectID
	case domain.RuleScopeAgent:
		// chk_rules_scope_agent: agent_id NOT NULL.
		rule.AgentID = &fx.agentID
	case domain.RuleScopeWorkspace:
		// chk_rules_scope_workspace: both must stay NULL.
	}
	require.NoError(t, NewRuleRepo(db).Create(context.Background(), rule))
	return rule
}

func TestRuleRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedRule(t, db, fx, domain.RuleScopeProject, "capacity guard", true)

	got, err := NewRuleRepo(db).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	require.NotNil(t, got.ProjectID)
	assert.Equal(t, fx.projectID, *got.ProjectID)
	assert.Nil(t, got.AgentID, "a project-scoped rule has no agent; nil is the correct read")
	assert.Equal(t, domain.RuleScopeProject, got.Scope)
	assert.Equal(t, "task.capacity_limit", got.RuleType)
	assert.Equal(t, "capacity guard", got.Name)
	assert.Equal(t, "description for capacity guard", got.Description)
	assert.JSONEq(t, `{"max_tasks": 5}`, string(got.Config))
	assert.Equal(t, pq.StringArray{"agent"}, got.AppliesToActorTypes)
	assert.Equal(t, pq.StringArray{"developer"}, got.AppliesToRoles,
		"the two applies_to_* arrays are adjacent and identically typed; transposing them is silent")
	assert.Equal(t, domain.RuleEnforcementWarn, got.Enforcement,
		"enforcement decides block-vs-warn; defaulting it would silently change what the engine does")
	assert.Equal(t, 42, got.Priority)
	assert.True(t, got.IsEnabled)
	assert.Equal(t, fx.userID, got.CreatedBy)
	assert.Equal(t, domain.ActorTypeUser, got.CreatedByType)
}

func TestRuleRepoListByWorkspace_ReturnsOnlyWorkspaceScopedRules(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	wsRule := seedRule(t, db, fx, domain.RuleScopeWorkspace, "workspace rule", true)
	seedRule(t, db, fx, domain.RuleScopeProject, "project rule", true)
	seedRule(t, db, fx, domain.RuleScopeAgent, "agent rule", true)

	rules, err := NewRuleRepo(db).ListByWorkspace(context.Background(), fx.workspaceID, false)
	require.NoError(t, err)
	require.Len(t, rules, 1, "the scope predicate must exclude project- and agent-scoped rules")

	assert.Equal(t, wsRule.ID, rules[0].ID)
	assert.Equal(t, "workspace rule", rules[0].Name)
	assert.Equal(t, domain.RuleScopeWorkspace, rules[0].Scope)
	assert.Nil(t, rules[0].ProjectID)
	assert.Nil(t, rules[0].AgentID)
	assert.Equal(t, domain.RuleEnforcementWarn, rules[0].Enforcement)
	assert.Equal(t, 42, rules[0].Priority)
	assert.JSONEq(t, `{"max_tasks": 5}`, string(rules[0].Config))
}

func TestRuleRepoListByWorkspace_IncludeDisabledSwitchesTheEnabledPredicate(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seedRule(t, db, fx, domain.RuleScopeWorkspace, "enabled rule", true)
	disabled := seedRule(t, db, fx, domain.RuleScopeWorkspace, "disabled rule", false)

	repo := NewRuleRepo(db)

	enabledOnly, err := repo.ListByWorkspace(context.Background(), fx.workspaceID, false)
	require.NoError(t, err)
	require.Len(t, enabledOnly, 1)
	assert.Equal(t, "enabled rule", enabledOnly[0].Name)

	all, err := repo.ListByWorkspace(context.Background(), fx.workspaceID, true)
	require.NoError(t, err)
	require.Len(t, all, 2)

	var sawDisabled bool
	for _, r := range all {
		if r.ID == disabled.ID {
			sawDisabled = true
			assert.False(t, r.IsEnabled, "is_enabled must be read per row, not assumed")
			assert.Equal(t, "disabled rule", r.Name)
		}
	}
	assert.True(t, sawDisabled, "includeDisabled must widen the result to the disabled rule")
}

func TestRuleRepoListByProject_ReturnsOnlyProjectScopedRules(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	projRule := seedRule(t, db, fx, domain.RuleScopeProject, "project rule", true)
	seedRule(t, db, fx, domain.RuleScopeWorkspace, "workspace rule", true)

	rules, err := NewRuleRepo(db).ListByProject(context.Background(), fx.projectID, false)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	assert.Equal(t, projRule.ID, rules[0].ID)
	assert.Equal(t, "project rule", rules[0].Name)
	assert.Equal(t, domain.RuleScopeProject, rules[0].Scope)
	require.NotNil(t, rules[0].ProjectID)
	assert.Equal(t, fx.projectID, *rules[0].ProjectID)
	assert.Equal(t, pq.StringArray{"developer"}, rules[0].AppliesToRoles)
}

func TestRuleRepoListByAgent_ReturnsOnlyAgentScopedRules(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	agentRule := seedRule(t, db, fx, domain.RuleScopeAgent, "agent rule", true)
	seedRule(t, db, fx, domain.RuleScopeWorkspace, "workspace rule", true)

	rules, err := NewRuleRepo(db).ListByAgent(context.Background(), fx.agentID, false)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	assert.Equal(t, agentRule.ID, rules[0].ID)
	assert.Equal(t, "agent rule", rules[0].Name)
	assert.Equal(t, domain.RuleScopeAgent, rules[0].Scope)
	require.NotNil(t, rules[0].AgentID)
	assert.Equal(t, fx.agentID, *rules[0].AgentID)
	assert.Equal(t, domain.RuleEnforcementWarn, rules[0].Enforcement)
}

func TestRuleRepoGetEffective_UnionsAllThreeScopes(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	wsRule := seedRule(t, db, fx, domain.RuleScopeWorkspace, "workspace rule", true)
	projRule := seedRule(t, db, fx, domain.RuleScopeProject, "project rule", true)
	agentRule := seedRule(t, db, fx, domain.RuleScopeAgent, "agent rule", true)

	rules, err := NewRuleRepo(db).GetEffective(context.Background(), fx.workspaceID, &fx.projectID, &fx.agentID)
	require.NoError(t, err)
	require.Len(t, rules, 3, "all three scopes are candidates when project and agent are both supplied")

	byID := map[uuid.UUID]domain.Rule{}
	for _, r := range rules {
		byID[r.ID] = r
	}
	require.Contains(t, byID, wsRule.ID)
	require.Contains(t, byID, projRule.ID)
	require.Contains(t, byID, agentRule.ID)

	// The union is assembled with fmt.Sprintf from a dynamic condition list —
	// so the assertion is that each branch scanned a fully-populated row.
	assert.Equal(t, "workspace rule", byID[wsRule.ID].Name)
	assert.Equal(t, domain.RuleEnforcementWarn, byID[wsRule.ID].Enforcement)
	assert.Equal(t, "project rule", byID[projRule.ID].Name)
	require.NotNil(t, byID[projRule.ID].ProjectID)
	assert.Equal(t, fx.projectID, *byID[projRule.ID].ProjectID)
	assert.Equal(t, "agent rule", byID[agentRule.ID].Name)
	require.NotNil(t, byID[agentRule.ID].AgentID)
	assert.Equal(t, fx.agentID, *byID[agentRule.ID].AgentID)
	assert.JSONEq(t, `{"max_tasks": 5}`, string(byID[agentRule.ID].Config))
}

func TestRuleRepoGetEffective_NilScopesNarrowToWorkspaceOnly(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	wsRule := seedRule(t, db, fx, domain.RuleScopeWorkspace, "workspace rule", true)
	seedRule(t, db, fx, domain.RuleScopeProject, "project rule", true)
	seedRule(t, db, fx, domain.RuleScopeAgent, "agent rule", true)

	// The dynamic WHERE drops the project and agent branches entirely: this
	// exercises the shortest form of the generated query.
	rules, err := NewRuleRepo(db).GetEffective(context.Background(), fx.workspaceID, nil, nil)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	assert.Equal(t, wsRule.ID, rules[0].ID)
	assert.Equal(t, domain.RuleScopeWorkspace, rules[0].Scope)
	assert.Equal(t, "workspace rule", rules[0].Name)
	assert.Equal(t, 42, rules[0].Priority)
}

// ---------------------------------------------------------------------------
// agent_activity_log_repo.go: List, ListByWorkspace
// ---------------------------------------------------------------------------

// seedActivityLog writes one agent_activity_log row.
func seedActivityLog(t *testing.T, db *sqlx.DB, fx coverageFixture, eventType, message string, taskID *uuid.UUID) *domain.AgentActivityLog {
	t.Helper()
	entry := &domain.AgentActivityLog{
		ID:          uuid.New(),
		AgentID:     fx.agentID,
		WorkspaceID: fx.workspaceID,
		EventType:   eventType,
		TaskID:      taskID,
		Message:     message,
		Metadata:    json.RawMessage(`{"source": "coverage"}`),
		CreatedAt:   time.Now(),
	}
	require.NoError(t, NewAgentActivityLogRepo(db).Create(context.Background(), entry))
	return entry
}

func TestAgentActivityLogRepoList_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedActivityLog(t, db, fx, "checkout", "checked out the task", &fx.taskID)

	page, err := NewAgentActivityLogRepo(db).List(context.Background(), fx.agentID,
		repository.AgentActivityLogFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 1, page.TotalCount)

	got := page.Items[0]
	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.agentID, got.AgentID)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	assert.Equal(t, "checkout", got.EventType)
	require.NotNil(t, got.TaskID, "task_id is nullable but was set; nil here means a dropped column")
	assert.Equal(t, fx.taskID, *got.TaskID)
	assert.Equal(t, "checked out the task", got.Message)
	assert.JSONEq(t, `{"source": "coverage"}`, string(got.Metadata))
	assert.False(t, got.CreatedAt.IsZero())
}

func TestAgentActivityLogRepoList_FiltersByEventTypeInSQL(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	wanted := seedActivityLog(t, db, fx, "checkout", "checked out", &fx.taskID)
	seedActivityLog(t, db, fx, "heartbeat", "still alive", nil)

	page, err := NewAgentActivityLogRepo(db).List(context.Background(), fx.agentID,
		repository.AgentActivityLogFilter{EventType: "checkout"},
		pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 1, page.TotalCount, "the count query must apply the same filter as the data query")

	assert.Equal(t, wanted.ID, page.Items[0].ID)
	assert.Equal(t, "checkout", page.Items[0].EventType)
}

func TestAgentActivityLogRepoListByWorkspace_SpansAgentsInTheWorkspace(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedActivityLog(t, db, fx, "checkout", "checked out the task", &fx.taskID)

	// A second workspace's activity must not leak in.
	other := newCoverageFixture(t, db)
	seedActivityLog(t, db, other, "checkout", "someone else's activity", nil)

	page, err := NewAgentActivityLogRepo(db).ListByWorkspace(context.Background(), fx.workspaceID,
		repository.AgentActivityLogFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 1, page.TotalCount)

	got := page.Items[0]
	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.agentID, got.AgentID)
	assert.Equal(t, "checkout", got.EventType)
	assert.Equal(t, "checked out the task", got.Message)
	assert.JSONEq(t, `{"source": "coverage"}`, string(got.Metadata))
	require.NotNil(t, got.TaskID)
	assert.Equal(t, fx.taskID, *got.TaskID)
}

// ---------------------------------------------------------------------------
// event_bus_repo.go: GetByID, List
// ---------------------------------------------------------------------------

// seedEventBusMessage writes one event_bus_messages row.
func seedEventBusMessage(t *testing.T, db *sqlx.DB, fx coverageFixture, eventType domain.EventType, subject string) *domain.EventBusMessage {
	t.Helper()
	agentID := fx.agentID
	taskID := fx.taskID
	msg := &domain.EventBusMessage{
		ID:          uuid.New(),
		WorkspaceID: fx.workspaceID,
		ProjectID:   fx.projectID,
		TaskID:      &taskID,
		AgentID:     &agentID,
		EventType:   eventType,
		Subject:     subject,
		Payload:     json.RawMessage(`{"body": "coverage"}`),
		Tags:        pq.StringArray{"coverage", "select-star"},
		TTL:         "7 days",
		CreatedAt:   time.Now(),
	}
	require.NoError(t, NewEventBusMessageRepo(db).Create(context.Background(), msg))
	return msg
}

func TestEventBusRepoGetByID_ReturnsEveryListedColumn(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedEventBusMessage(t, db, fx, domain.EventTypeSummary, "coverage summary")

	got, err := NewEventBusMessageRepo(db).GetByID(context.Background(), seeded.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.workspaceID, got.WorkspaceID)
	assert.Equal(t, fx.projectID, got.ProjectID)
	require.NotNil(t, got.TaskID)
	assert.Equal(t, fx.taskID, *got.TaskID)
	require.NotNil(t, got.AgentID)
	assert.Equal(t, fx.agentID, *got.AgentID)
	assert.Equal(t, domain.EventTypeSummary, got.EventType)
	assert.Equal(t, "coverage summary", got.Subject)
	assert.JSONEq(t, `{"body": "coverage"}`, string(got.Payload))
	assert.Equal(t, pq.StringArray{"coverage", "select-star"}, got.Tags)
	assert.NotEmpty(t, got.TTL, "ttl is an INTERVAL read back as a string; empty means it was not selected")
	require.NotNil(t, got.ExpiresAt, "expires_at is NOT NULL in the DB and must always come back set")
	assert.True(t, got.ExpiresAt.After(got.CreatedAt),
		"expires_at is derived from created_at + ttl, so it must sit in the future of created_at")
}

func TestEventBusRepoList_ReturnsFullyPopulatedRowsScopedToTheProject(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	seeded := seedEventBusMessage(t, db, fx, domain.EventTypeStatusChange, "coverage status change")

	other := newCoverageFixture(t, db)
	seedEventBusMessage(t, db, other, domain.EventTypeStatusChange, "someone else's event")

	page, err := NewEventBusMessageRepo(db).List(context.Background(), fx.projectID,
		repository.EventBusMessageFilter{}, pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 1, page.TotalCount)

	// This query is `e.`-qualified for the ORDER BY alias, so the qualified
	// column list has to line up with eventBusRow just as the bare one does.
	got := page.Items[0]
	assert.Equal(t, seeded.ID, got.ID)
	assert.Equal(t, fx.projectID, got.ProjectID)
	assert.Equal(t, domain.EventTypeStatusChange, got.EventType)
	assert.Equal(t, "coverage status change", got.Subject)
	assert.JSONEq(t, `{"body": "coverage"}`, string(got.Payload))
	assert.Equal(t, pq.StringArray{"coverage", "select-star"}, got.Tags)
	require.NotNil(t, got.AgentID)
	assert.Equal(t, fx.agentID, *got.AgentID)
	require.NotNil(t, got.ExpiresAt)
}

func TestEventBusRepoList_FiltersByEventTypeAndTagsInSQL(t *testing.T) {
	db := selectStarCoverageTestDB(t)
	fx := newCoverageFixture(t, db)
	wanted := seedEventBusMessage(t, db, fx, domain.EventTypeError, "the error")
	seedEventBusMessage(t, db, fx, domain.EventTypeSummary, "the summary")

	errorType := domain.EventTypeError
	page, err := NewEventBusMessageRepo(db).List(context.Background(), fx.projectID,
		repository.EventBusMessageFilter{EventType: &errorType, Tags: []string{"select-star"}},
		pagination.Params{Page: 1, PageSize: 50})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, 1, page.TotalCount, "the count query must apply the same filter as the data query")

	assert.Equal(t, wanted.ID, page.Items[0].ID)
	assert.Equal(t, domain.EventTypeError, page.Items[0].EventType)
	assert.Equal(t, "the error", page.Items[0].Subject)
}
