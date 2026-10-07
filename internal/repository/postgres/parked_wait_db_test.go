package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type parkedFixture struct {
	repo                           *TaskRepo
	id, owner, backlog, todo, done uuid.UUID
	ctx                            context.Context
	plan                           domain.ParkedWaitPlan
}

func newParkedFixture(t *testing.T) *parkedFixture {
	t.Helper()
	r, id, owner := checkoutGenerationFixture(t)
	task, err := r.GetByID(context.Background(), id)
	require.NoError(t, err)
	f := &parkedFixture{repo: r, id: id, owner: owner, todo: task.StatusID, backlog: uuid.New(), done: uuid.New(), ctx: actorctx.WithActor(context.Background(), owner, domain.ActorTypeAgent)}
	_, err = r.db.Exec(`INSERT INTO task_statuses(id,project_id,name,slug,category) VALUES($1,$2,'Backlog','backlog','backlog'),($3,$2,'Done','done','done')`, f.backlog, task.ProjectID, f.done)
	require.NoError(t, err)
	_, err = r.db.Exec(`UPDATE tasks SET status_id=$2,assignee_id=$3,assignee_type='agent',labels=$4 WHERE id=$1`, id, f.backlog, owner, pq.Array([]string{"park:date", "keep:Юникод"}))
	require.NoError(t, err)
	created := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Microsecond)
	f.plan = domain.ParkedWaitPlan{ID: uuid.New(), ProjectID: task.ProjectID, OwnerID: owner, OwnerType: domain.AssigneeTypeAgent, WaitCommentID: uuid.New(), FeedReceiptID: uuid.New(), FeedSource: "confirmed_feed", FeedReceivedAt: created.Add(-time.Minute), FeedClosedAt: created.Add(time.Minute), Reason: "pipeline", Condition: domain.ParkedWaitCondition{ProjectPath: "entire-vc/evc-mesh", PipelineID: 93}, RemoveLabels: []string{"park:date"}, Lease: domain.ParkedWaitLease{Mode: "absent"}}
	_, err = r.db.Exec(`INSERT INTO comments(id,task_id,author_id,author_type,body,created_at,updated_at) VALUES($1,$2,$3,'agent','⏳ WAIT pipeline:entire-vc/evc-mesh#93: штатная поставка',$4,$4)`, f.plan.WaitCommentID, id, owner, created)
	require.NoError(t, err)
	f.refresh(t)
	return f
}

func (f *parkedFixture) refresh(t *testing.T) {
	t.Helper()
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	f.plan.ExpectedVersion = task.Version
	f.plan.ExpectedStartAfter = task.StartAfter
	f.plan.Lease.Generation = task.CheckoutGeneration
}

func (f *parkedFixture) register(t *testing.T) {
	t.Helper()
	f.refresh(t)
	r, err := f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
	require.NoError(t, err)
	require.False(t, r.Released)
}

func (f *parkedFixture) request() domain.ReleaseParkedWait {
	return domain.ReleaseParkedWait{RegistrationID: f.plan.ID, ExpectedVersion: f.plan.ExpectedVersion, ReleaseID: uuid.New(), Trigger: domain.ParkedWaitTrigger{Kind: f.plan.Reason, EventID: "confirmed-terminal-93"}}
}

func (f *parkedFixture) assertHeld(t *testing.T) {
	t.Helper()
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, f.backlog, task.StatusID)
	require.Contains(t, task.Labels, "park:date")
	var count int
	require.NoError(t, f.repo.db.Get(&count, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, f.id))
	require.Zero(t, count)
}

func parked409(t *testing.T, err error) {
	t.Helper()
	var e *apierror.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, 409, e.Code)
}

func TestParkedWaitEmptyRemovalPreservesLabels(t *testing.T) {
	for _, nilLabels := range []bool{true, false} {
		t.Run(fmt.Sprint(nilLabels), func(t *testing.T) {
			f := newParkedFixture(t)
			labels := []string{"keep:Юникод", "ordinary-label"}
			_, err := f.repo.db.Exec(`UPDATE tasks SET labels=$2 WHERE id=$1`, f.id, pq.Array(labels))
			require.NoError(t, err)
			f.plan.RemoveLabels = []string{}
			if nilLabels {
				f.plan.RemoveLabels = nil // JSON null or omitted must also subtract nothing.
			}
			f.register(t)
			_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
			require.NoError(t, err)
			task, err := f.repo.GetByID(f.ctx, f.id)
			require.NoError(t, err)
			require.Equal(t, pq.StringArray(labels), task.Labels)
			require.Equal(t, f.todo, task.StatusID)
		})
	}
}

func TestParkedWaitReleaseExactlyOnce(t *testing.T) {
	t.Run("legacy", func(t *testing.T) { parkedWaitExactlyOnce(t, nil) })
	t.Run("required jobs", func(t *testing.T) { parkedWaitExactlyOnce(t, []string{"build", "verify"}) })
}

func parkedWaitExactlyOnce(t *testing.T, jobs []string) {
	t.Helper()
	f := newParkedFixture(t)
	f.plan.Condition.RequiredJobs = jobs
	f.register(t)
	input := f.request()
	start := make(chan struct{})
	results := make(chan *domain.ParkedWaitResult, 2)
	errs := make(chan error, 2)
	var providerReads atomic.Int32
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			r, err := f.repo.ReleaseParkedWaitVerified(f.ctx, f.id, input, func(context.Context, *domain.Task, domain.ParkedWaitPlan) (string, error) {
				if providerReads.Add(1) != 1 {
					return "", apierror.ServiceUnavailable("provider offline after first release")
				}
				return "success", nil
			})
			results <- r
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs)
	}
	require.Equal(t, int32(1), providerReads.Load())
	a, b := <-results, <-results
	require.Equal(t, a.ActivityID, b.ActivityID)
	require.NotEqual(t, a.Replayed, b.Replayed)
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, f.todo, task.StatusID)
	require.Equal(t, f.owner, *task.AssigneeID)
	require.Equal(t, f.plan.ProjectID, task.ProjectID)
	require.Equal(t, pq.StringArray{"keep:Юникод"}, task.Labels)
	require.Equal(t, f.plan.ExpectedVersion+1, task.Version)
	var row struct {
		Count   int       `db:"count"`
		Changes []byte    `db:"changes"`
		Actor   uuid.UUID `db:"actor_id"`
	}
	require.NoError(t, f.repo.db.Get(&row, `SELECT count(*) OVER() AS count,changes,actor_id FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, f.id))
	require.Equal(t, 1, row.Count)
	require.Equal(t, f.owner, row.Actor)
	var changes map[string]any
	require.NoError(t, json.Unmarshal(row.Changes, &changes))
	require.Equal(t, "parked-wait-api", changes["source"])
	require.Equal(t, f.plan.WaitCommentID.String(), changes["wait_comment_id"])
	require.Equal(t, "success", changes["pipeline_status"])
	if jobs != nil {
		require.Equal(t, []any{"build", "verify"}, changes["required_jobs"])
		changed := f.plan
		changed.Condition.RequiredJobs = []string{"build"}
		_, err = f.repo.RegisterParkedWait(f.ctx, f.id, changed)
		parked409(t, err)
	}
	_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	parked409(t, err)
	// A lost response can be retried after later edits and without GitLab access.
	_, err = f.repo.db.Exec(`UPDATE tasks SET labels=array_append(labels,'later') WHERE id=$1`, f.id)
	require.NoError(t, err)
	r, err := f.repo.ReleaseParkedWait(f.ctx, f.id, input, "")
	require.NoError(t, err)
	require.True(t, r.Replayed)
	reg, err := f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
	require.NoError(t, err)
	require.True(t, reg.Replayed)
}

func TestParkedWaitVerificationFailureRollsBack(t *testing.T) {
	f := newParkedFixture(t)
	f.plan.Condition.RequiredJobs = []string{"build"}
	f.register(t)
	input := f.request()
	_, err := f.repo.ReleaseParkedWaitVerified(f.ctx, f.id, input, func(context.Context, *domain.Task, domain.ParkedWaitPlan) (string, error) {
		return "", apierror.ServiceUnavailable("provider unavailable")
	})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 503, apiErr.Code)
	f.assertHeld(t)
	result, err := f.repo.ReleaseParkedWaitVerified(f.ctx, f.id, input, func(context.Context, *domain.Task, domain.ParkedWaitPlan) (string, error) {
		return "success", nil
	})
	require.NoError(t, err)
	require.True(t, result.Released)
}

func TestParkedRequiredJobsCannotReleaseOnOtherTerminalStatus(t *testing.T) {
	for _, status := range []string{"manual", "running", "failed", "skipped", "canceled", ""} {
		t.Run(status, func(t *testing.T) {
			f := newParkedFixture(t)
			f.plan.Condition.RequiredJobs = []string{"build", "verify"}
			f.register(t)
			_, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), status)
			parked409(t, err)
			f.assertHeld(t)
		})
	}
}

// Observe an actual PostgreSQL lock wait, rather than assuming goroutine timing.
func waitParkedBarrier(t *testing.T, db *sqlx.DB, pid int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("release did not reach the PostgreSQL lock barrier")
		case <-tick.C:
			var waiting bool
			require.NoError(t, db.Get(&waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid))
			if waiting {
				return
			}
		}
	}
}

func TestParkedWaitConcurrentSnapshotWriters(t *testing.T) {
	mutations := map[string]string{
		"ordinary-label": `UPDATE tasks SET labels=array_append(labels,'new-label') WHERE id=$1`,
		"new-gate":       `UPDATE tasks SET human_gate=true WHERE id=$1`,
		"freeze":         `UPDATE tasks SET labels=array_append(labels,'freeze') WHERE id=$1`,
		"repark":         `UPDATE tasks SET labels=array_append(labels,'park:wait-external') WHERE id=$1`,
		"version":        `UPDATE tasks SET title='new title' WHERE id=$1`,
		"lease":          `UPDATE tasks SET checked_out_by=assignee_id,checkout_token=gen_random_uuid(),checkout_expires=now()+interval '1 hour',checkout_generation=checkout_generation+1 WHERE id=$1`,
		"new-WAIT":       `INSERT INTO comments(id,task_id,author_id,author_type,body) SELECT gen_random_uuid(),id,assignee_id,'agent','⏳ WAIT 20m: новый gate' FROM tasks WHERE id=$1`,
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newParkedFixture(t)
			f.register(t)
			tx, err := f.repo.db.Beginx()
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.Exec(mutation, f.id)
			require.NoError(t, err)
			var pid int
			require.NoError(t, tx.Get(&pid, `SELECT pg_backend_pid()`))
			done := make(chan error, 1)
			input := f.request()
			go func() { _, err := f.repo.ReleaseParkedWait(f.ctx, f.id, input, "success"); done <- err }()
			waitParkedBarrier(t, f.repo.db, pid)
			require.NoError(t, tx.Commit())
			parked409(t, <-done)
			f.assertHeld(t)
			if name == "ordinary-label" {
				task, err := f.repo.GetByID(f.ctx, f.id)
				require.NoError(t, err)
				require.Contains(t, task.Labels, "new-label")
			}
		})
	}
}

func TestParkedWaitCurrentIndependentGates(t *testing.T) {
	for _, label := range []string{"freeze", "no-promote", "no-intake-promote", "manual-park", "wait-external", "park:wait-external", "park:unknown", "human:verify", "hold"} {
		t.Run(label, func(t *testing.T) {
			f := newParkedFixture(t)
			_, err := f.repo.db.Exec(`UPDATE tasks SET labels=array_append(labels,$2) WHERE id=$1`, f.id, label)
			require.NoError(t, err)
			f.refresh(t)
			_, err = f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
			parked409(t, err)
			f.assertHeld(t)
		})
	}
	for _, sql := range []string{`UPDATE tasks SET human_gate=true WHERE id=$1`, `UPDATE tasks SET delegation_level='supervised' WHERE id=$1`} {
		f := newParkedFixture(t)
		_, err := f.repo.db.Exec(sql, f.id)
		require.NoError(t, err)
		f.refresh(t)
		_, err = f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
		parked409(t, err)
	}
}

func (f *parkedFixture) blocker(t *testing.T, status uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.repo.db.Exec(`INSERT INTO tasks(id,project_id,status_id,title,task_number,created_by,created_by_type) VALUES($1,$2,$3,'blocker',(SELECT coalesce(max(task_number),0)+1 FROM tasks WHERE project_id=$2),$4,'system')`, id, f.plan.ProjectID, status, uuid.Nil)
	require.NoError(t, err)
	_, err = f.repo.db.Exec(`INSERT INTO task_dependencies(id,task_id,depends_on_task_id,dependency_type) VALUES($1,$2,$3,'blocks')`, uuid.New(), f.id, id)
	require.NoError(t, err)
	return id
}

func (f *parkedFixture) dependency(t *testing.T, blocker uuid.UUID) {
	t.Helper()
	f.plan.Reason = "dependency"
	f.plan.Condition = domain.ParkedWaitCondition{TaskID: &blocker}
	f.plan.RemoveLabels = []string{"park:wait-external"}
	_, err := f.repo.db.Exec(`UPDATE comments SET body=$2 WHERE id=$1`, f.plan.WaitCommentID, "⏳ WAIT card:#"+blocker.String()[:8]+": dependency only")
	require.NoError(t, err)
	_, err = f.repo.db.Exec(`UPDATE tasks SET labels=$2 WHERE id=$1`, f.id, pq.Array([]string{"park:wait-external", "keep:Юникод"}))
	require.NoError(t, err)
}

func TestParkedWaitAllBlockersAndExplicitReason(t *testing.T) {
	f := newParkedFixture(t)
	first := f.blocker(t, f.done)
	second := f.blocker(t, f.todo)
	f.dependency(t, first)
	f.register(t)
	_, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "")
	parked409(t, err)
	_, err = f.repo.db.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, second, f.done)
	require.NoError(t, err)
	r, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "")
	require.NoError(t, err)
	require.True(t, r.Released)
	// The edge alone cannot reclassify a real external/pipeline WAIT.
	g := newParkedFixture(t)
	blocker := g.blocker(t, g.done)
	g.dependency(t, blocker)
	_, err = g.repo.db.Exec(`UPDATE comments SET body='⏳ WAIT pipeline:entire-vc/evc-mesh#93: independent' WHERE id=$1`, g.plan.WaitCommentID)
	require.NoError(t, err)
	g.refresh(t)
	_, err = g.repo.RegisterParkedWait(g.ctx, g.id, g.plan)
	parked409(t, err)
}

func TestParkedWaitBlockerReopensAtBarrier(t *testing.T) {
	f := newParkedFixture(t)
	blocker := f.blocker(t, f.done)
	f.register(t)
	tx, err := f.repo.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`UPDATE tasks SET status_id=$2 WHERE id=$1`, blocker, f.todo)
	require.NoError(t, err)
	var pid int
	require.NoError(t, tx.Get(&pid, `SELECT pg_backend_pid()`))
	done := make(chan error, 1)
	go func() { _, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success"); done <- err }()
	waitParkedBarrier(t, f.repo.db, pid)
	require.NoError(t, tx.Commit())
	parked409(t, <-done)
	f.assertHeld(t)
}

func TestParkedWaitNewEdgeInvalidatesRegistration(t *testing.T) {
	f := newParkedFixture(t)
	f.register(t)
	f.blocker(t, f.todo)
	_, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	parked409(t, err)
	f.assertHeld(t)
}

func TestParkedWaitPipelineTerminalControl(t *testing.T) {
	f := newParkedFixture(t)
	f.register(t)
	for _, status := range []string{"", "running", "pending", "manual", "scheduled"} {
		_, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), status)
		parked409(t, err)
		f.assertHeld(t)
	}
	r, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "failed")
	require.NoError(t, err)
	require.True(t, r.Released, "failed is a terminal event, not success evidence")
}

func TestParkedWaitRollbackAndRetry(t *testing.T) {
	f := newParkedFixture(t)
	f.register(t)
	input := f.request()
	lock, err := f.repo.db.BeginTxx(context.Background(), nil)
	require.NoError(t, err)
	_, err = lock.Exec(`SELECT pg_advisory_xact_lock(90304261007003)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		if _, cleanupErr := f.repo.db.Exec(`DROP TRIGGER IF EXISTS parked_wait_test_fail_activity ON activity_log; DROP FUNCTION IF EXISTS parked_wait_test_fail_activity(); DROP TABLE IF EXISTS parked_wait_test_failure_targets`); cleanupErr != nil {
			t.Errorf("drop parked-wait failure trigger fixture: %v", cleanupErr)
		}
		if cleanupErr := lock.Rollback(); cleanupErr != nil {
			t.Errorf("release parked-wait failure fixture lock: %v", cleanupErr)
		}
	})
	// Fixed object names avoid interpolating SQL identifiers. The advisory
	// transaction lock serializes concurrent copies of this test; a target row
	// scopes the trigger to this fixture so unrelated activity inserts continue.
	_, err = f.repo.db.Exec(`DROP TRIGGER IF EXISTS parked_wait_test_fail_activity ON activity_log; DROP FUNCTION IF EXISTS parked_wait_test_fail_activity(); DROP TABLE IF EXISTS parked_wait_test_failure_targets; CREATE TABLE parked_wait_test_failure_targets (entity_id uuid PRIMARY KEY); CREATE FUNCTION parked_wait_test_fail_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS (SELECT 1 FROM parked_wait_test_failure_targets WHERE entity_id=NEW.entity_id) THEN RAISE EXCEPTION 'injected activity failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER parked_wait_test_fail_activity BEFORE INSERT ON activity_log FOR EACH ROW EXECUTE FUNCTION parked_wait_test_fail_activity()`)
	require.NoError(t, err)
	_, err = f.repo.db.Exec(`INSERT INTO parked_wait_test_failure_targets (entity_id) VALUES ($1)`, f.id)
	require.NoError(t, err)
	_, err = f.repo.ReleaseParkedWait(f.ctx, f.id, input, "success")
	require.ErrorContains(t, err, "injected activity failure")
	f.assertHeld(t)
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, f.plan.ExpectedVersion, task.Version)
	_, err = f.repo.db.Exec(`DELETE FROM parked_wait_test_failure_targets WHERE entity_id=$1`, f.id)
	require.NoError(t, err)
	_, err = f.repo.db.Exec(`DROP TRIGGER parked_wait_test_fail_activity ON activity_log; DROP FUNCTION parked_wait_test_fail_activity(); DROP TABLE parked_wait_test_failure_targets`)
	require.NoError(t, err)
	r, err := f.repo.ReleaseParkedWait(f.ctx, f.id, input, "success")
	require.NoError(t, err)
	require.True(t, r.Released)
	r, err = f.repo.ReleaseParkedWait(f.ctx, f.id, input, "")
	require.NoError(t, err)
	require.True(t, r.Replayed)
	var count int
	require.NoError(t, f.repo.db.Get(&count, `SELECT count(*) FROM activity_log WHERE entity_id=$1`, f.id))
	require.Equal(t, 1, count)
}

func TestParkedWaitStartAfterOwnership(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			f := newParkedFixture(t)
			future := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
			_, err := f.repo.db.Exec(`UPDATE tasks SET start_after=$2 WHERE id=$1`, f.id, future)
			require.NoError(t, err)
			if owned {
				meta, _ := json.Marshal(map[string]any{"parked_wait": map[string]any{"start_after": future}})
				_, err = f.repo.db.Exec(`UPDATE comments SET metadata=$2 WHERE id=$1`, f.plan.WaitCommentID, meta)
				require.NoError(t, err)
				f.plan.ClearStartAfter = true
			}
			f.register(t)
			r, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
			if !owned {
				parked409(t, err)
				f.assertHeld(t)
				return
			}
			require.NoError(t, err)
			require.True(t, r.Released)
			task, err := f.repo.GetByID(f.ctx, f.id)
			require.NoError(t, err)
			require.Nil(t, task.StartAfter)
		})
	}
	f := newParkedFixture(t)
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	_, err := f.repo.db.Exec(`UPDATE tasks SET start_after=$2 WHERE id=$1`, f.id, future)
	require.NoError(t, err)
	f.refresh(t)
	f.plan.ClearStartAfter = true
	_, err = f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
	parked409(t, err)
}

func TestParkedWaitDateRequiresExplicitDueSemantics(t *testing.T) {
	for _, minutes := range []int{1, 20} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			f := newParkedFixture(t)
			var created time.Time
			require.NoError(t, f.repo.db.Get(&created, `SELECT created_at FROM comments WHERE id=$1`, f.plan.WaitCommentID))
			notBefore := created.Add(time.Duration(minutes) * time.Minute)
			f.plan.Reason = "date"
			f.plan.Condition = domain.ParkedWaitCondition{NotBefore: &notBefore, TimeSemantics: "not_before"}
			_, err := f.repo.db.Exec(`UPDATE comments SET body=$2 WHERE id=$1`, f.plan.WaitCommentID, fmt.Sprintf("⏳ WAIT %dm: explicit deadline", minutes))
			require.NoError(t, err)
			f.register(t)
			r, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "")
			if minutes == 20 {
				parked409(t, err)
				f.assertHeld(t)
			} else {
				require.NoError(t, err)
				require.True(t, r.Released)
			}
		})
	}
}

func TestParkedWaitOwnedLeaseRequiresExactHolderSession(t *testing.T) {
	f := newParkedFixture(t)
	scope := generationScope()
	lease, err := f.repo.AcquireCheckout(f.ctx, f.id, f.owner, uuid.New(), time.Now().Add(time.Hour), scope)
	require.NoError(t, err)
	// Acquisition may move backlog to in_progress through service, but raw repo
	// acquisition leaves status intact and exposes the same generation contract.
	f.plan.Lease = domain.ParkedWaitLease{Mode: "owned", Generation: lease.Generation, Holder: &f.owner, SessionID: scope.SessionID}
	f.register(t)
	other := actorctx.WithActor(context.Background(), uuid.New(), domain.ActorTypeAgent)
	_, err = f.repo.ReleaseParkedWait(other, f.id, f.request(), "success")
	parked409(t, err)
	r, err := f.repo.ReleaseParkedWait(f.ctx, f.id, f.request(), "success")
	require.NoError(t, err)
	require.True(t, r.Released)
	task, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.Nil(t, task.CheckoutToken)
	require.Nil(t, task.CheckedOutBy)
}

func TestParkedWaitRejectsLegacyFeedSource(t *testing.T) {
	for _, source := range []string{"", "_REFEED", "legacy", "current_task"} {
		t.Run(source, func(t *testing.T) {
			f := newParkedFixture(t)
			f.plan.FeedSource = source
			_, err := f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
			var e *apierror.Error
			require.ErrorAs(t, err, &e)
			require.Equal(t, 400, e.Code)
			f.assertHeld(t)
		})
	}
}

func TestParkedWaitFeedIdentityCannotFollowProjectMove(t *testing.T) {
	f := newParkedFixture(t)
	other := uuid.New()
	_, err := f.repo.db.Exec(`INSERT INTO projects(id,workspace_id,name,slug) SELECT $1,workspace_id,'other',$2 FROM projects WHERE id=$3`, other, other.String(), f.plan.ProjectID)
	require.NoError(t, err)
	_, err = f.repo.db.Exec(`UPDATE tasks SET project_id=$2 WHERE id=$1`, f.id, other)
	require.NoError(t, err)
	// Refresh only the mutable CAS snapshot, never the receipt's historical identity.
	f.refresh(t)
	_, err = f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
	parked409(t, err)
	f.assertHeld(t)
}

func TestParkedWaitRegistrationProvenanceAndRepark(t *testing.T) {
	for _, field := range []string{"project", "owner", "window", "pipeline-prefix", "latest-WAIT"} {
		t.Run(field, func(t *testing.T) {
			f := newParkedFixture(t)
			switch field {
			case "project":
				f.plan.ProjectID = uuid.New()
			case "owner":
				f.plan.OwnerID = uuid.New()
			case "window":
				f.plan.FeedReceivedAt = f.plan.FeedClosedAt.Add(-time.Second)
			case "pipeline-prefix":
				_, err := f.repo.db.Exec(`UPDATE comments SET body='⏳ WAIT pipeline:entire-vc/evc-mesh#934: different pipeline' WHERE id=$1`, f.plan.WaitCommentID)
				require.NoError(t, err)
			case "latest-WAIT":
				_, err := f.repo.db.Exec(`INSERT INTO comments(id,task_id,author_id,author_type,body) VALUES($1,$2,$3,'agent','⏳ WAIT 20m: later wait')`, uuid.New(), f.id, f.owner)
				require.NoError(t, err)
			}
			f.refresh(t)
			_, err := f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
			parked409(t, err)
		})
	}
	f := newParkedFixture(t)
	f.register(t)
	_, err := f.repo.db.Exec(`UPDATE tasks SET title='changed' WHERE id=$1`, f.id)
	require.NoError(t, err)
	f.refresh(t)
	f.plan.ID = uuid.New()
	_, err = f.repo.RegisterParkedWait(f.ctx, f.id, f.plan)
	parked409(t, err)
}

func TestGetParkedWaitNotFound(t *testing.T) {
	f := newParkedFixture(t)
	_, err := f.repo.GetParkedWait(f.ctx, f.id, uuid.New())
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, 404, apiErr.Code)
}
