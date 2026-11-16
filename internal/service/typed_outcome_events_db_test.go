package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// A turn command lands in the event feed once. AfterTurn used to publish the
// action itself after logActivity had already bridged it to the event bus, so
// every wait/next/hand_off produced two identical event_bus_messages rows
// 35 ms apart and the Events widget showed each twice (fde4f82f).
func TestTurnCommands_PublishOneEventEach_DB(t *testing.T) {
	f, repo, _, task, owner := newCheckoutM1Fixture(t)
	// mock: external boundary — capture push delivery without sending callbacks.
	push := NewMockAgentNotifyService()
	svc := NewTaskService(repo, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db),
		WithProjectRepo(postgres.NewProjectRepo(f.db)), WithTaskAgentRepo(postgres.NewAgentRepo(f.db)),
		WithProjectMemberRepoTask(postgres.NewProjectMemberRepo(f.db)), WithAgentNotifyService(push),
		WithEventBusService(NewEventBusService(postgres.NewEventBusMessageRepo(f.db), nil)))
	cmds := NewTurnCommandService(postgres.NewTurnCommandRepo(f.db), postgres.NewVCSLinkRepo(f.db), TurnHooksFor(svc), nil)
	t.Cleanup(func() { _, _ = f.db.Exec(`DELETE FROM event_bus_messages WHERE workspace_id=$1`, f.workspaceID) })

	blocker := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: f.statusID, Title: "blocker",
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, repo.Create(agentContext(owner), blocker, nil))
	next := uuid.New()
	nextSlug := "handoff-" + next.String()[:8]
	_, err := f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,$3,$3,'fixture-hash','test')`,
		next, f.workspaceID, nextSlug)
	require.NoError(t, err)

	checkout := func() {
		t.Helper()
		_, cerr := f.db.Exec(`UPDATE tasks SET checked_out_by=$2, checkout_token=gen_random_uuid(),
			checkout_expires=now()+interval '1 hour', checkout_acquired_at=now(),
			checkout_generation=checkout_generation+1 WHERE id=$1`, task, owner)
		require.NoError(t, cerr)
	}
	events := func(subject, outcome string) int {
		t.Helper()
		var n int
		require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM event_bus_messages
			WHERE task_id=$1 AND subject=$2 AND payload->>'source'='typed-outcome' AND payload->>'outcome'=$3`,
			task, subject, outcome))
		return n
	}
	ctx := agentContext(owner)

	checkout()
	_, err = cmds.WaitFor(ctx, task, WaitForInput{On: string(domain.TaskWaitCard), TargetTaskID: &blocker.ID, Note: "blocker first"})
	require.NoError(t, err)
	require.Equal(t, 1, events("task.updated", string(domain.TurnOutcomeWait)), "one wait_for, one task.updated")

	checkout()
	_, err = cmds.ContinueNext(ctx, task, TurnCommon{}, "edited, next is the test")
	require.NoError(t, err)
	require.Equal(t, 1, events("task.updated", string(domain.TurnOutcomeNext)), "one continue_next, one task.updated")

	// Positive control: hand_off still announces the assignment and wakes the
	// new assignee, once.
	checkout()
	_, err = cmds.HandOff(ctx, task, TurnCommon{}, nextSlug, "they own the deploy")
	require.NoError(t, err)
	require.Equal(t, 1, events("task.assigned", string(domain.TurnOutcomeHandoff)), "one hand_off, one task.assigned")
	var assigned int
	for _, c := range push.Calls() {
		if c.EventType == "task.assigned" && c.AgentID == next {
			assigned++
		}
	}
	require.Equal(t, 1, assigned, "the new assignee is notified")
}
