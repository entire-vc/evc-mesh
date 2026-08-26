package service

// Tests for assertAssigneeAcceptsAssignments / AssigneeRefusesAssignmentsError —
// the narrow gate that refuses an assignment onto an agent whose accepts_from
// is the explicit empty JSON array [], a lane's own declaration "I accept no
// assignments at all" (Sage, mesh-dispatcher-svc, agent-ops-probe today).
//
// Deliberately NOT tested here (out of scope per the task's own decision,
// see ADR `adrs-accepts-from-routing-gate`): comparing the acting caller
// against accepts_from's contents, max_concurrent_tasks, assigned_by.
//
// Every negative test is paired with a positive control on the same path —
// same discipline as task_assignee_workspace_test.go — because a guard that
// refuses everything satisfies the negative half by itself while breaking
// assignment for the whole fleet.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// seedAcceptsFromAgent adds an agent native to env's home workspace with the
// given accepts_from raw value, and returns its id.
func (e *tenancyEnv) seedAcceptsFromAgent(t *testing.T, slug string, acceptsFrom json.RawMessage) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.agents.items[id] = &domain.Agent{
		ID: id, WorkspaceID: e.homeWS, Slug: slug, AcceptsFrom: acceptsFrom,
	}
	return id
}

// assertRefusedAssignments checks the error is the accepts_from=[] refusal,
// carries the agent's slug, and — the mutation-not-presence half of AC#1 —
// that no project_members row was manufactured as a side effect. The second
// assertion is what actually proves the check ran BEFORE enrolment rather
// than after: if enrolment ran first, this assertion (not the error type)
// would be the one to fail.
func (e *tenancyEnv) assertRefusedAssignments(t *testing.T, err error, agentID uuid.UUID, what string) {
	t.Helper()
	require.Error(t, err, "%s: an agent with accepts_from=[] must be refused", what)
	var refused *AssigneeRefusesAssignmentsError
	require.ErrorAs(t, err, &refused, "%s: refusal must be the typed accepts_from error, not some other one", what)
	assert.Equal(t, agentID, refused.PrincipalID)
	exists, existsErr := e.members.ExistsMember(context.Background(), e.projectID, nil, &agentID)
	require.NoError(t, existsErr)
	assert.False(t, exists,
		"%s: a refused agent must not be enrolled into project_members — that row is exactly what "+
			"would make it look native to every later check", what)
}

// ---------------------------------------------------------------------------
// AC#1 — negative control by mutation, and AC#3 — fires on AssignTask.
// ---------------------------------------------------------------------------

func TestAcceptsFromEmpty_AssignTaskRefuses(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)
	refusingAgent := env.seedAcceptsFromAgent(t, "sage", json.RawMessage(`[]`))

	err := env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &refusingAgent, AssigneeType: domain.AssigneeTypeAgent,
	})

	env.assertRefusedAssignments(t, err, refusingAgent, "assign_task")
	assert.Nil(t, env.tasks.items[taskID].AssigneeID,
		"assign_task: assignee_id must be UNCHANGED (still nil) after a refused assignment")
}

// TestAcceptsFromEmpty_AssignTaskDoesNotOverwriteExistingAssignee is the same
// mutation control against a task that already had a different assignee —
// the more realistic shape of "assignee_id did not change", since a nil-stays-nil
// result could in principle be explained by something else entirely.
func TestAcceptsFromEmpty_AssignTaskDoesNotOverwriteExistingAssignee(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)
	refusingAgent := env.seedAcceptsFromAgent(t, "sage", json.RawMessage(`[]`))

	require.NoError(t, env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
	}), "setup: seed a real prior assignee")

	err := env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &refusingAgent, AssigneeType: domain.AssigneeTypeAgent,
	})

	env.assertRefusedAssignments(t, err, refusingAgent, "assign_task over an existing assignee")
	require.NotNil(t, env.tasks.items[taskID].AssigneeID)
	assert.Equal(t, env.nativeAgent, *env.tasks.items[taskID].AssigneeID,
		"assignee_id must remain the PRIOR assignee, not the refused one and not nil")
}

// ---------------------------------------------------------------------------
// AC#2 — positive controls: ["*"] and a specific non-empty list still work.
// Without these, a gate that rejects everything is indistinguishable from a
// working one.
// ---------------------------------------------------------------------------

func TestAcceptsFromEmpty_AssignTaskAllowsWildcard(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)
	wildcardAgent := env.seedAcceptsFromAgent(t, "wildcard-agent", json.RawMessage(`["*"]`))

	require.NoError(t, env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &wildcardAgent, AssigneeType: domain.AssigneeTypeAgent,
	}), "positive control: accepts_from=[\"*\"] must still be assignable")
	require.NotNil(t, env.tasks.items[taskID].AssigneeID)
	assert.Equal(t, wildcardAgent, *env.tasks.items[taskID].AssigneeID)
}

func TestAcceptsFromEmpty_AssignTaskAllowsSpecificList(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)
	listedAgent := env.seedAcceptsFromAgent(t, "listed-agent", json.RawMessage(`["bill","garfield"]`))

	require.NoError(t, env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &listedAgent, AssigneeType: domain.AssigneeTypeAgent,
	}), "positive control: a non-empty specific accepts_from list must still be assignable")
	require.NotNil(t, env.tasks.items[taskID].AssigneeID)
	assert.Equal(t, listedAgent, *env.tasks.items[taskID].AssigneeID)
}

// TestAcceptsFromEmpty_AssignTaskAllowsUnsetAcceptsFrom is a third positive
// control: an agent whose accepts_from was never explicitly set (nil
// RawMessage, as every agent seeded elsewhere in this package models) must
// not be swept up by the empty-array check.
func TestAcceptsFromEmpty_AssignTaskAllowsUnsetAcceptsFrom(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)

	require.NoError(t, env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.nativeAgent, AssigneeType: domain.AssigneeTypeAgent,
	}), "positive control: unset accepts_from (nil) must not be treated as accepts_from=[]")
}

// ---------------------------------------------------------------------------
// AC#3 — fires on Create.
// ---------------------------------------------------------------------------

func TestAcceptsFromEmpty_CreateRefuses(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	_, statusID := env.seedTask(t)
	refusingAgent := env.seedAcceptsFromAgent(t, "mesh-dispatcher-svc", json.RawMessage(`[]`))
	before := len(env.tasks.items)

	err := env.svc.Create(context.Background(), &domain.Task{
		ProjectID: env.projectID, StatusID: statusID, Title: "planted",
		AssigneeID: &refusingAgent, AssigneeType: domain.AssigneeTypeAgent,
	})

	env.assertRefusedAssignments(t, err, refusingAgent, "create_task")
	assert.Len(t, env.tasks.items, before,
		"a task created with a refusing assignee must not exist at all — same contract as the "+
			"tenancy refusal: a half-created task the caller has to notice is unassigned is worse "+
			"than a clean refusal")
}

func TestAcceptsFromEmpty_CreateAllowsWildcard(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	_, statusID := env.seedTask(t)
	wildcardAgent := env.seedAcceptsFromAgent(t, "wildcard-agent", json.RawMessage(`["*"]`))

	require.NoError(t, env.svc.Create(context.Background(), &domain.Task{
		ProjectID: env.projectID, StatusID: statusID, Title: "legitimate",
		AssigneeID: &wildcardAgent, AssigneeType: domain.AssigneeTypeAgent,
	}), "positive control: create_task with accepts_from=[\"*\"] must still succeed")
}

// ---------------------------------------------------------------------------
// AC#3 — fires on CreateSubtask.
// ---------------------------------------------------------------------------

func TestAcceptsFromEmpty_CreateSubtaskRefuses(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	parentID, statusID := env.seedTask(t)
	refusingAgent := env.seedAcceptsFromAgent(t, "agent-ops-probe", json.RawMessage(`[]`))

	_, err := env.svc.CreateSubtask(context.Background(), parentID, CreateSubtaskInput{
		Title: "child", StatusID: &statusID,
		AssigneeID: &refusingAgent, AssigneeType: domain.AssigneeTypeAgent,
	})

	env.assertRefusedAssignments(t, err, refusingAgent, "create_subtask")
}

func TestAcceptsFromEmpty_CreateSubtaskAllowsSpecificList(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	parentID, statusID := env.seedTask(t)
	listedAgent := env.seedAcceptsFromAgent(t, "listed-agent", json.RawMessage(`["bill"]`))

	child, err := env.svc.CreateSubtask(context.Background(), parentID, CreateSubtaskInput{
		Title: "child", StatusID: &statusID,
		AssigneeID: &listedAgent, AssigneeType: domain.AssigneeTypeAgent,
	})
	require.NoError(t, err, "positive control: create_subtask with a non-empty specific list must still succeed")
	require.NotNil(t, child.AssigneeID)
	assert.Equal(t, listedAgent, *child.AssigneeID)
}

// ---------------------------------------------------------------------------
// Human assignee is untouched: accepts_from has no meaning for a user.
// ---------------------------------------------------------------------------

func TestAcceptsFromEmpty_DoesNotApplyToHumanAssignee(t *testing.T) {
	env := setupTenancyEnv(t, nil)
	taskID, _ := env.seedTask(t)

	require.NoError(t, env.svc.AssignTask(context.Background(), taskID, AssignTaskInput{
		AssigneeID: &env.nativeUser, AssigneeType: domain.AssigneeTypeUser,
	}), "accepts_from is an agent-profile field; it must not affect human assignees")
}

// ---------------------------------------------------------------------------
// Unit table for the predicate itself, same style as
// TestAssertAssigneeIsTyped_UnitTable in task_assignee_workspace_test.go.
// ---------------------------------------------------------------------------

func TestAcceptsFromRefusesAll_UnitTable(t *testing.T) {
	cases := []struct {
		name    string
		raw     json.RawMessage
		refuses bool
	}{
		{"nil (unset)", nil, false},
		{"empty RawMessage", json.RawMessage(``), false},
		{"explicit empty array", json.RawMessage(`[]`), true},
		{"explicit empty array with whitespace", json.RawMessage(`[ ]`), true},
		{"wildcard", json.RawMessage(`["*"]`), false},
		{"specific single-item list", json.RawMessage(`["bill"]`), false},
		{"specific multi-item list", json.RawMessage(`["bill","garfield"]`), false},
		{"malformed JSON — not this check's job", json.RawMessage(`not json`), false},
		{"wrong shape (object, not array) — not this check's job", json.RawMessage(`{}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.refuses, acceptsFromRefusesAll(tc.raw))
		})
	}
}

// ---------------------------------------------------------------------------
// TestEveryAssigneeWritePathIsWorkspaceChecked stays green: this new function
// does not write an AssigneeID/ReviewerID field anywhere, and the call site it
// gains (ensureAssigneeProjectMember) was already declared "funnel:" before
// this change — see assignee_write_path_invariant_test.go. No entry needed
// here; this test exists so a future edit that DOES turn
// assertAssigneeAcceptsAssignments into a writer trips the real invariant test
// instead of silently drifting.
// ---------------------------------------------------------------------------
