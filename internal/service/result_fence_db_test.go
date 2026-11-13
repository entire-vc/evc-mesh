package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// A stale attempt cannot publish. Each result write (comment,
// artifact, finish, move) carrying a checkout precondition commits only while
// the caller holds exactly that live lock, checked under the task row lock;
// refused, it answers 409 checkout_precondition_failed and writes nothing.
// Skips without a database like the other *_db_test.go here; CI runs them.

type fenceCounts struct {
	StatusID      uuid.UUID `db:"status_id"`
	Comments      int       `db:"comments"`
	Artifacts     int       `db:"artifacts"`
	Outcomes      int       `db:"outcomes"`
	InputRevision int64     `db:"input_revision"`
}

func (x *typedCommandFixture) counts(t *testing.T) fenceCounts {
	t.Helper()
	var c fenceCounts
	require.NoError(t, x.f.db.Get(&c, `SELECT status_id, input_revision,
		(SELECT count(*) FROM comments WHERE task_id=t.id) AS comments,
		(SELECT count(*) FROM artifacts WHERE task_id=t.id) AS artifacts,
		(SELECT count(*) FROM turn_outcomes WHERE task_id=t.id) AS outcomes
		FROM tasks t WHERE id=$1`, x.task))
	return c
}

func (x *typedCommandFixture) lease(t *testing.T, ctx context.Context) *CheckoutResult {
	t.Helper()
	res, err := x.svc.CheckoutTask(ctx, x.task, 30, nil, checkoutScope())
	require.NoError(t, err)
	return res
}

func tokenFence(task, holder uuid.UUID, l *CheckoutResult) *domain.ResultFence {
	tok := l.CheckoutToken
	return &domain.ResultFence{TaskID: task, Holder: holder, Token: &tok}
}

func sessionFence(task, holder uuid.UUID, l *CheckoutResult) *domain.ResultFence {
	return &domain.ResultFence{TaskID: task, Holder: holder, SessionID: l.SessionID, Generation: l.Generation}
}

func requireFenceRefusal(t *testing.T, err error, code, what string) {
	t.Helper()
	require.Error(t, err, what)
	var ae *apierror.Error
	require.True(t, errors.As(err, &ae), "%s: want *apierror.Error, got %T %v", what, err, err)
	assert.Equal(t, http.StatusConflict, ae.Code, what)
	assert.Equal(t, code, ae.ErrorCode, what)
}

// publishAll tries the four fenced result writes with one fence and returns
// their errors in the order comment, artifact, move, finish.
func (x *typedCommandFixture) publishAll(t *testing.T, ctx context.Context, f *domain.ResultFence) map[string]error {
	t.Helper()
	comments := postgres.NewCommentRepo(x.f.db)
	artifacts := postgres.NewArtifactRepo(x.f.db)
	out := map[string]error{}
	out["comment"] = comments.Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task, AuthorID: x.owner,
		AuthorType: domain.ActorTypeAgent, Body: "result", Fence: f})
	out["artifact"] = artifacts.Create(ctx, &domain.Artifact{ID: uuid.New(), TaskID: x.task, Name: "r.txt",
		ArtifactType: domain.ArtifactTypeFile, MimeType: "text/plain", StorageKey: "k/" + uuid.NewString(), SizeBytes: 1,
		UploadedBy: x.owner, UploadedByType: domain.UploaderTypeAgent, Fence: f})
	pos := 7.0
	out["move"] = x.svc.MoveTask(ctx, x.task, MoveTaskInput{Position: &pos, Source: "api", Fence: f})
	_, out["finish"] = x.cmds().FinishTask(ctx, x.task, FinishTaskInput{ResultKind: "change", Evidence: "go test: ok", To: "done", Fence: f})
	return out
}

func (x *typedCommandFixture) requireAllRefused(t *testing.T, ctx context.Context, f *domain.ResultFence, why string) {
	t.Helper()
	before := x.counts(t)
	for what, err := range x.publishAll(t, ctx, f) {
		requireFenceRefusal(t, err, domain.CodeCheckoutPreconditionFailed, why+": "+what)
	}
	assert.Equal(t, before, x.counts(t), why+": nothing is written")
}

func TestResultFence_NewGenerationRefusesTheOldAttempt_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := agentContext(x.owner)
	old := x.lease(t, ctx)
	require.NoError(t, x.svc.ReleaseCheckout(ctx, x.task, old.CheckoutToken))
	cur := x.lease(t, ctx)
	require.Greater(t, cur.Generation, old.Generation)

	// Same agent, older attempt: today's lockCommandTask lets it through.
	x.requireAllRefused(t, ctx, tokenFence(x.task, x.owner, old), "old token")
	x.requireAllRefused(t, ctx, sessionFence(x.task, x.owner, old), "old session+generation")

	// Positive control: the current attempt publishes all four (in the order
	// comment, artifact, move, finish; the finish ends the lock last).
	before := x.counts(t)
	for what, err := range x.publishAll(t, ctx, tokenFence(x.task, x.owner, cur)) {
		require.NoError(t, err, what)
	}
	after := x.counts(t)
	assert.Equal(t, before.Comments+2, after.Comments, "comment + finish closing comment")
	assert.Equal(t, before.Artifacts+1, after.Artifacts)
	assert.Equal(t, x.done, after.StatusID, "the current holder's finish lands")
}

func TestResultFence_ExpiredLockNobodyTookIsRefused_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)
	// Positive control while live.
	require.NoError(t, postgres.NewCommentRepo(x.f.db).Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task,
		AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "live", Fence: tokenFence(x.task, x.owner, l)}))
	_, err := x.f.db.Exec(`UPDATE tasks SET checkout_expires = now() - interval '1 second' WHERE id=$1`, x.task)
	require.NoError(t, err)
	x.requireAllRefused(t, ctx, tokenFence(x.task, x.owner, l), "expired token")
	x.requireAllRefused(t, ctx, sessionFence(x.task, x.owner, l), "expired session+generation")
}

func TestResultFence_ForceReleasedLockIsRefused_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)
	admin := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeUser)
	holder := x.owner
	require.NoError(t, x.svc.ForceReleaseCheckout(admin, x.task, domain.CheckoutExpectation{
		Holder: &holder, Generation: l.Generation, SessionID: l.SessionID, Reason: "stuck worker"}))
	x.requireAllRefused(t, ctx, tokenFence(x.task, x.owner, l), "force-released token")
	x.requireAllRefused(t, ctx, sessionFence(x.task, x.owner, l), "force-released session+generation")
}

// Two attempts of one holder finish at once with the same lock: the row lock
// orders them, the first finish ends the lock, the second is refused.
func TestResultFence_ConcurrentPublishesOneWins_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)
	before := x.counts(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = x.cmds().FinishTask(ctx, x.task, FinishTaskInput{ResultKind: "change",
				Evidence: "attempt", To: "done", Fence: tokenFence(x.task, x.owner, l)})
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
			continue
		}
		requireFenceRefusal(t, err, domain.CodeCheckoutPreconditionFailed, "the losing finish")
	}
	assert.Equal(t, 1, won, "exactly one publish wins: %v", errs)
	after := x.counts(t)
	assert.Equal(t, before.Comments+1, after.Comments, "one closing comment")
	assert.Equal(t, before.Outcomes+1, after.Outcomes, "one outcome")
}

func TestResultFence_InputRevision_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := agentContext(x.owner)
	rev0 := x.counts(t).InputRevision
	require.Positive(t, rev0)

	// Unscoped lease: the agent heartbeat only extends those.
	l, err := x.svc.CheckoutTask(ctx, x.task, 30, nil)
	require.NoError(t, err)
	_, err = x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 60)
	require.NoError(t, err)
	_, err = x.f.db.Exec(`INSERT INTO project_rules(id,project_id,rule_type,config,enforcement_mode) VALUES($1,$2,'workflow','{"mid_pipeline":{"heartbeat_extends_checkout":true}}','advisory')`, uuid.New(), x.f.projectID)
	require.NoError(t, err)
	n, err := x.repo.ExtendCheckoutsOnHeartbeat(ctx, x.owner)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the heartbeat did extend the lease")
	_, err = x.f.db.Exec(`UPDATE tasks SET runtime_state='{"kind":"working"}', labels=array_append(labels,'x'), position=position+1 WHERE id=$1`, x.task)
	require.NoError(t, err)
	_, err = x.f.db.Exec(`UPDATE tasks SET input_revision = 999 WHERE id=$1`, x.task)
	require.NoError(t, err)
	assert.Equal(t, rev0, x.counts(t).InputRevision, "checkout, extend, heartbeat, runtime_state, labels, position and a direct write leave input_revision")

	var rev1 int64
	for i, edit := range []struct{ what, sql string }{
		{"description", `UPDATE tasks SET description = coalesce(description,'') || ' edited' WHERE id=$1`},
		{"title", `UPDATE tasks SET title = title || ' edited' WHERE id=$1`},
		{"custom_fields", `UPDATE tasks SET custom_fields = coalesce(custom_fields,'{}'::jsonb) || '{"fence_edit":1}'::jsonb WHERE id=$1`},
		{"delegation_level", `UPDATE tasks SET delegation_level = CASE WHEN delegation_level='review' THEN 'auto'::delegation_level ELSE 'review'::delegation_level END WHERE id=$1`},
	} {
		_, err = x.f.db.Exec(edit.sql, x.task)
		require.NoError(t, err, edit.what)
		rev1 = x.counts(t).InputRevision
		assert.Equal(t, rev0+int64(i)+1, rev1, "a %s change advances input_revision", edit.what)
	}
	task, err := x.repo.GetByID(ctx, x.task)
	require.NoError(t, err)
	assert.Equal(t, rev1, task.InputRevision, "the task JSON carries input_revision")

	stale := tokenFence(x.task, x.owner, l)
	stale.ExpectedInputRevision = &rev0
	before := x.counts(t)
	err = postgres.NewCommentRepo(x.f.db).Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task,
		AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "from stale input", Fence: stale})
	requireFenceRefusal(t, err, domain.CodeInputRevisionChanged, "stale expected_input_revision")
	assert.Equal(t, before, x.counts(t), "nothing is written")

	fresh := tokenFence(x.task, x.owner, l)
	fresh.ExpectedInputRevision = &rev1
	require.NoError(t, postgres.NewCommentRepo(x.f.db).Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task,
		AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "from current input", Fence: fresh}),
		"positive control: the current revision publishes")
}

// This MR does not change reassignment (the release comes with a second MR,
// after the Runner sends the fence): the holder keeps its lock, its extend and
// its fenced writes, and another agent's checkout waits, as before.
func TestResultFence_ReassignmentLeavesTheLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	system := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	owner, asked := x.owner, x.asked
	// The production assign path checks the assignee against the agent directory.
	assigner := NewTaskService(x.repo, postgres.NewTaskStatusRepo(x.f.db), nil, postgres.NewActivityLogRepo(x.f.db),
		WithProjectRepo(postgres.NewProjectRepo(x.f.db)), WithTaskAgentRepo(postgres.NewAgentRepo(x.f.db)),
		WithProjectMemberRepoTask(postgres.NewProjectMemberRepo(x.f.db)))
	require.NoError(t, assigner.AssignTask(system, x.task, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)

	require.NoError(t, assigner.AssignTask(system, x.task, AssignTaskInput{AssigneeID: &asked, AssigneeType: domain.AssigneeTypeAgent}))

	var holder *uuid.UUID
	require.NoError(t, x.f.db.Get(&holder, `SELECT checked_out_by FROM tasks WHERE id=$1`, x.task))
	require.NotNil(t, holder)
	assert.Equal(t, x.owner, *holder, "reassignment does not end the lock")
	_, err := x.svc.ExtendCheckout(ctx, x.task, l.CheckoutToken, 30)
	require.NoError(t, err, "the holder's extend still answers")
	_, err = x.svc.CheckoutTask(agentContext(x.asked), x.task, 30, nil, checkoutScope())
	require.Error(t, err, "the new assignee's checkout waits for the holder's turn")
	require.NoError(t, postgres.NewCommentRepo(x.f.db).Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task,
		AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "fenced", Fence: tokenFence(x.task, x.owner, l)}),
		"the holder's fenced write passes: the lock is live and its own")

	var released int
	require.NoError(t, x.f.db.Get(&released, `SELECT count(*) FROM activity_log WHERE entity_id=$1
		AND action='task.checkout_released_auto'`, x.task))
	assert.Zero(t, released, "no automatic release on reassignment")
}

// Without a fence the routes behave as today: an older attempt of the same
// agent can still comment (the documented reason to send the precondition).
func TestResultFence_AbsentKeepsTodaysBehaviour_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := agentContext(x.owner)
	old := x.lease(t, ctx)
	require.NoError(t, x.svc.ReleaseCheckout(ctx, x.task, old.CheckoutToken))
	_ = x.lease(t, ctx)
	require.NoError(t, postgres.NewCommentRepo(x.f.db).Create(ctx, &domain.Comment{ID: uuid.New(), TaskID: x.task,
		AuthorID: x.owner, AuthorType: domain.ActorTypeAgent, Body: "unfenced"}))
}

// releaseAfterReadRepo lets a concurrent release land right after the move's
// first read of the task, the window between the early fence check and the
// move's answer.
type releaseAfterReadRepo struct {
	*postgres.TaskRepo
	afterFirstRead func()
}

func (r *releaseAfterReadRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	t, err := r.TaskRepo.GetByID(ctx, id)
	if f := r.afterFirstRead; f != nil {
		r.afterFirstRead = nil
		f()
	}
	return t, err
}

// A fenced move that would change nothing writes nothing, so it never reaches
// the locked check of the write; it must still not answer success for a lock
// released after its first read (an Idempotency-Key would replay that answer).
func TestResultFence_NoOpMoveRechecksTheLock_DB(t *testing.T) {
	x := newTypedCommandFixture(t)
	ctx := agentContext(x.owner)
	l := x.lease(t, ctx)
	var pos float64
	require.NoError(t, x.f.db.Get(&pos, `SELECT position FROM tasks WHERE id=$1`, x.task))

	repo := &releaseAfterReadRepo{TaskRepo: x.repo}
	svc := NewTaskService(repo, postgres.NewTaskStatusRepo(x.f.db), nil, postgres.NewActivityLogRepo(x.f.db))
	require.NoError(t, svc.MoveTask(ctx, x.task, MoveTaskInput{Position: &pos, Source: "api", Fence: tokenFence(x.task, x.owner, l)}),
		"positive control: a no-op move under the live lock answers success")

	repo.afterFirstRead = func() { require.NoError(t, x.svc.ReleaseCheckout(ctx, x.task, l.CheckoutToken)) }
	err := svc.MoveTask(ctx, x.task, MoveTaskInput{Position: &pos, Source: "api", Fence: tokenFence(x.task, x.owner, l)})
	requireFenceRefusal(t, err, domain.CodeCheckoutPreconditionFailed, "no-op move after the lock was released")
}
