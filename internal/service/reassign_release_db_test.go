package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Reassignment ends the lock (the second step of the stale-worker fence): with
// the instance setting on, a change of assignee clears the previous holder's
// checkout in the same write, so that holder's extend and fenced result writes
// are refused and the new assignee can take the card. Off, nothing changes
// (TestResultFence_ReassignmentLeavesTheLock_DB pins that).

func (x *typedCommandFixture) assigner(repo repository.TaskRepository, releases bool) TaskService {
	return NewTaskService(repo, postgres.NewTaskStatusRepo(x.f.db), nil, postgres.NewActivityLogRepo(x.f.db),
		WithProjectRepo(postgres.NewProjectRepo(x.f.db)), WithTaskAgentRepo(postgres.NewAgentRepo(x.f.db)),
		WithProjectMemberRepoTask(postgres.NewProjectMemberRepo(x.f.db)), WithReassignReleasesCheckout(releases))
}

func (x *typedCommandFixture) holder(t *testing.T) *uuid.UUID {
	t.Helper()
	var h *uuid.UUID
	require.NoError(t, x.f.db.Get(&h, `SELECT checked_out_by FROM tasks WHERE id=$1`, x.task))
	return h
}

func (x *typedCommandFixture) autoReleases(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, x.f.db.Get(&n, `SELECT count(*) FROM activity_log WHERE entity_id=$1
		AND action='task.checkout_released_auto' AND changes->>'reason'='reassignment'`, x.task))
	return n
}

// An extend after the lock is gone answers the documented "you lost the task"
// 403 (agents-me-contract.md, "Fencing a stale worker"); clients branch on it.
func requireLostLockExtend(t *testing.T, err error, why string) {
	t.Helper()
	require.Error(t, err, why)
	var apiErr *apierror.Error
	require.True(t, errors.As(err, &apiErr), why+": an API error")
	assert.Equal(t, http.StatusForbidden, apiErr.Code, why+": 403")
}

var systemActor = actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)

func TestReassignReleasesCheckout_ReassignEndsTheOldLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	owner, asked := x.owner, x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)
	require.NotNil(t, x.holder(t))

	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))

	assert.Nil(t, x.holder(t), "the reassignment ended the previous holder's lock")
	assert.Equal(t, 1, x.autoReleases(t), "the release is in the audit with its reason")
	_, err := x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 30)
	requireLostLockExtend(t, err, "the old holder's extend is refused")
	x.requireAllRefused(t, ctx, tokenFence(x.task, x.owner, l), "old holder, token form")
	x.requireAllRefused(t, ctx, sessionFence(x.task, x.owner, l), "old holder, session form")
	_, err = x.svc.CheckoutTask(agentContext(x.asked), x.task, 30, nil, checkoutScope())
	require.NoError(t, err, "the new assignee takes the card at once")
}

func TestReassignReleasesCheckout_UnassignEndsTheLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	owner := x.owner
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)

	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeType: domain.AssigneeTypeUnassigned}))

	assert.Nil(t, x.holder(t), "unassigning ends the lock")
	x.requireAllRefused(t, ctx, tokenFence(x.task, x.owner, l), "unassigned holder")
	_, err := x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 30)
	requireLostLockExtend(t, err, "unassigned holder: extend")
}

// A move that changes the assignee ends the old lock like an assignment does.
func TestReassignReleasesCheckout_MoveWithNewAssigneeEndsTheLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	owner, asked := x.owner, x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)

	pos := 7.0
	require.NoError(t, a.MoveTask(systemActor, x.task, MoveTaskInput{Position: &pos, AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent, Source: "api"}))

	assert.Nil(t, x.holder(t), "the move to another assignee ended the lock")
	assert.Equal(t, 1, x.autoReleases(t))
	x.requireAllRefused(t, ctx, tokenFence(x.task, x.owner, l), "old holder after a reassigning move")
	_, err := x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 30)
	requireLostLockExtend(t, err, "old holder after a reassigning move: extend")
}

// Off (the default), a reassignment by assign or by move leaves the lock.
func TestReassignReleasesCheckout_OffKeepsTheLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, false)
	owner, asked := x.owner, x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)

	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))
	pos := 7.0
	require.NoError(t, a.MoveTask(systemActor, x.task, MoveTaskInput{Position: &pos, AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent, Source: "api"}))
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeType: domain.AssigneeTypeUnassigned}))

	h := x.holder(t)
	require.NotNil(t, h)
	assert.Equal(t, x.owner, *h)
	assert.Zero(t, x.autoReleases(t))
	_, err := x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 30)
	require.NoError(t, err, "the holder keeps its extend")
}

// Handing the card to the agent that already holds its lock keeps the lock,
// and a write that changes no assignee releases nothing.
func TestReassignReleasesCheckout_HolderKeepsItsOwnLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	asked := x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx) // the owner holds the lock of a card assigned to asked

	owner := x.owner
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))

	h := x.holder(t)
	require.NotNil(t, h)
	assert.Equal(t, x.owner, *h, "assigned to its own holder, the lock stays")
	assert.Zero(t, x.autoReleases(t))
	_, err := x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 30)
	require.NoError(t, err)
}

// The holder that gives its own card away (a reviewer returning it to the
// author: assign, then move or hand_off) keeps its lock until its own turn
// command or finish ends it; its later fenced writes of that turn pass. an internal ticket
func TestReassignReleasesCheckout_HolderGivingItsCardAwayKeepsItsLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	owner, asked := x.owner, x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)

	require.NoError(t, a.AssignTask(ctx, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))
	pos := 3.0
	require.NoError(t, a.MoveTask(ctx, x.task, MoveTaskInput{Position: &pos, AssigneeType: domain.AssigneeTypeUnassigned, Source: "api"}),
		"a move by the holder that unassigns the card")
	require.NoError(t, a.AssignTask(ctx, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))

	h := x.holder(t)
	require.NotNil(t, h, "the holder's own reassignment keeps its lock")
	assert.Equal(t, x.owner, *h)
	assert.Zero(t, x.autoReleases(t), "no task.checkout_released_auto for the holder's own reassignment")
	f := tokenFence(x.task, x.owner, l)
	require.NoError(t, postgres.NewCommentRepo(x.f.db).Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task,
		AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "returned with remarks", Fence: f}),
		"the holder's fenced comment after the reassignment passes")
	pos = 9.0
	require.NoError(t, a.MoveTask(ctx, x.task, MoveTaskInput{Position: &pos, Source: "api", Fence: f}),
		"the holder's fenced move after the reassignment passes")
	_, err := x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 30)
	require.NoError(t, err, "the holder keeps its extend")
}

// The path that failed live: the reviewer holding a card in review returns it
// to its author with assign_task (no fence), then move_task(todo) with its
// fence. The move passes, the card leaves review and the lock ends with it
// only when the turn command or finish releases it.
func TestReassignReleasesCheckout_ReviewerReturnsCardWithFencedMove_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	owner, asked := x.owner, x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)
	var review, todo uuid.UUID
	require.NoError(t, x.f.db.Get(&review, `SELECT id FROM task_statuses WHERE project_id=$1 AND slug='review'`, x.f.projectID))
	require.NoError(t, x.f.db.Get(&todo, `SELECT status_id FROM tasks WHERE id=$1`, x.task))
	_, err := x.f.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, x.task, review)
	require.NoError(t, err)

	require.NoError(t, a.AssignTask(ctx, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}),
		"the reviewer gives the card back to its author")
	require.NoError(t, a.MoveTask(ctx, x.task, MoveTaskInput{StatusID: &todo, Source: "api", Fence: tokenFence(x.task, x.owner, l)}),
		"the reviewer's fenced move to todo in the same turn passes")

	var st uuid.UUID
	require.NoError(t, x.f.db.Get(&st, `SELECT status_id FROM tasks WHERE id=$1`, x.task))
	assert.Equal(t, todo, st, "the card left review")
	h := x.holder(t)
	require.NotNil(t, h)
	assert.Equal(t, x.owner, *h)
	assert.Zero(t, x.autoReleases(t), "no reassignment release on the holder's own path")
}

// v1 compares the actor's agent id with the holder: the same agent from another
// session (a parallel lane) reassigning the card its sibling session holds is
// treated as the holder and the lock stays. Expected for now; passing the fence
// into assign_task is the next step if this shows up live.
func TestReassignReleasesCheckout_SameAgentOtherSessionKeepsTheLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	owner, asked := x.owner, x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	l := x.lease(t, agentContext(x.owner))

	otherSession := agentContext(x.owner) // same agent id, no fence of the holding session
	require.NoError(t, a.AssignTask(otherSession, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))

	h := x.holder(t)
	require.NotNil(t, h, "same agent id: the lock stays (v1)")
	assert.Equal(t, x.owner, *h)
	assert.Zero(t, x.autoReleases(t))
	var gen int64
	require.NoError(t, x.f.db.Get(&gen, `SELECT checkout_generation FROM tasks WHERE id=$1`, x.task))
	assert.Equal(t, l.Generation, gen, "the holding session's lease is the same one")
}

// Another agent (not the holder) reassigning the card is still a takeover.
func TestReassignReleasesCheckout_AnotherAgentTakesOver_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	a := x.assigner(x.repo, true)
	owner, asked := x.owner, x.asked
	require.NoError(t, a.AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)

	require.NoError(t, a.AssignTask(agentContext(x.asked), x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))

	assert.Nil(t, x.holder(t), "a reassignment by another agent ends the holder's lock")
	assert.Equal(t, 1, x.autoReleases(t))
	x.requireAllRefused(t, ctx, tokenFence(x.task, x.owner, l), "old holder after another agent's reassignment")
}

// A checkout taken between the assignment's read and its write refuses the
// whole write: the release never clears a lock it did not see.
func TestReassignReleasesCheckout_NeverClearsALockItDidNotSee_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	owner, asked := x.owner, x.asked
	require.NoError(t, x.assigner(x.repo, true).AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	old := x.lease(t, ctx)
	var fresh *CheckoutResult
	racing := &releaseAfterReadRepo{TaskRepo: x.repo, afterFirstRead: func() {
		require.NoError(t, x.svc.ReleaseCheckout(ctx, x.task, old.CheckoutToken))
		fresh = x.lease(t, ctx)
	}}

	err := x.assigner(racing, true).AssignTask(systemActor, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent})
	require.Error(t, err, "the version moved under the assignment")

	h := x.holder(t)
	require.NotNil(t, h, "the lock taken after the read is still there")
	assert.Equal(t, x.owner, *h)
	assert.Zero(t, x.autoReleases(t))
	require.NoError(t, postgres.NewCommentRepo(x.f.db).Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task,
		AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "fenced", Fence: tokenFence(x.task, x.owner, fresh)}),
		"the new lock's fenced write passes")
}

func TestReassignmentEndsLock(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	const ag, us, un = domain.AssigneeTypeAgent, domain.AssigneeTypeUser, domain.AssigneeTypeUnassigned
	for name, c := range map[string]struct {
		holder, old *uuid.UUID
		oldType     domain.AssigneeType
		new         *uuid.UUID
		newType     domain.AssigneeType
		want        bool
	}{
		"no lock":                      {nil, &a, ag, &b, ag, false},
		"assignee unchanged":           {&a, &a, ag, &a, ag, false},
		"to another agent":             {&a, &a, ag, &b, ag, true},
		"unassigned":                   {&a, &a, ag, nil, un, true},
		"to the holder":                {&a, &b, ag, &a, ag, false},
		"holder not the assignee":      {&a, &b, ag, nil, un, true},
		"same uuid, now a user":        {&a, &a, ag, &a, us, true},
		"to a user with holder's uuid": {&a, &b, ag, &a, us, true},
		"user to the holder agent":     {&a, &a, us, &a, ag, false},
		"no assignee, type relabelled": {&a, nil, "", nil, un, false},
	} {
		assert.Equal(t, c.want, reassignmentEndsLock(systemActor, c.holder, c.old, c.oldType, c.new, c.newType), name)
	}
}

// The actor decides between a takeover and the holder giving its card away.
func TestReassignmentEndsLock_Actor(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	const ag = domain.AssigneeTypeAgent
	assert.False(t, reassignmentEndsLock(agentContext(a), &a, &a, ag, &b, ag), "the holder itself")
	assert.True(t, reassignmentEndsLock(agentContext(b), &a, &a, ag, &b, ag), "another agent")
	assert.True(t, reassignmentEndsLock(actorctx.WithActor(context.Background(), a, domain.ActorTypeUser), &a, &a, ag, &b, ag),
		"a user sharing the holder's uuid is another principal")
	assert.True(t, reassignmentEndsLock(systemActor, &a, &a, ag, &b, ag), "the system")
	assert.True(t, reassignmentEndsLock(context.Background(), &a, &a, ag, &b, ag), "no actor")
}
