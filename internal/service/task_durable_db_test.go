package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// Exercise an actual PostgreSQL INSERT refusal after the guarded UPDATE. A
// per-test shadow table avoids DDL on tables used by other CI packages.
func TestMoveTask_DurableActivityRefusal(t *testing.T) {
	for _, fault := range []string{"none", "activity_log", "task_event_outbox"} {
		reject := fault != "none"
		t.Run(fmt.Sprintf("fault=%s", fault), func(t *testing.T) {
			f, repo, _, id, owner := newCheckoutM1Fixture(t)
			ctx := actorctx.WithActor(context.Background(), owner, domain.ActorTypeAgent)
			ip := uuid.New()
			_, err := f.db.Exec(`INSERT INTO task_statuses(id,project_id,name,slug,category) VALUES($1,$2,'Active','active','in_progress')`, ip, f.projectID)
			require.NoError(t, err)
			_, err = f.db.Exec(`UPDATE tasks SET status_id=$2,assignee_id=$3,assignee_type='agent' WHERE id=$1`, id, ip, owner)
			require.NoError(t, err)
			_, err = repo.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(-time.Minute), checkoutScope())
			require.NoError(t, err)
			before, err := repo.GetByID(ctx, id)
			require.NoError(t, err)
			if reject {
				f.db.SetMaxOpenConns(1)
				f.db.SetMaxIdleConns(1)
				schema := pq.QuoteIdentifier("durable_" + uuid.New().String())
				// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
				_, err = f.db.Exec(`CREATE SCHEMA ` + schema)
				require.NoError(t, err)
				t.Cleanup(func() {
					// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
					_, cleanupErr := f.db.Exec(`SET search_path=public; DROP SCHEMA ` + schema + ` CASCADE`)
					require.NoError(t, cleanupErr)
				})
				// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
				_, err = f.db.Exec(`CREATE TABLE ` + schema + `.` + fault + ` (LIKE public.` + fault + ` INCLUDING ALL)`)
				require.NoError(t, err)
				// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
				_, err = f.db.Exec(`ALTER TABLE ` + schema + `.` + fault + ` ADD CHECK (action <> 'task.moved')`)
				require.NoError(t, err)
				// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
				_, err = f.db.Exec(`SET search_path=` + schema + `,public`)
				require.NoError(t, err)
			}
			svc := NewTaskService(repo, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db), WithProjectRepo(postgres.NewProjectRepo(f.db)), WithTaskAgentRepo(postgres.NewAgentRepo(f.db)), WithProjectMemberRepoTask(postgres.NewProjectMemberRepo(f.db)), WithEventBusService(NewEventBusService(postgres.NewEventBusMessageRepo(f.db), nil)))
			nextOwner := uuid.New()
			_, err = f.db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'next owner',$3,'fixture-hash','test')`, nextOwner, f.workspaceID, nextOwner.String())
			require.NoError(t, err)
			input := reaperMoveInput(before, &f.statusID, activityCheckoutLeaseExpired)
			input.AssigneeID, input.AssigneeType = &nextOwner, domain.AssigneeTypeAgent
			err = svc.MoveTask(ctx, id, input)
			fresh, readErr := repo.GetByID(ctx, id)
			require.NoError(t, readErr)
			if reject {
				require.Error(t, err, "audit/outbox INSERT failure must abort mutation")
				require.Equal(t, before.StatusID, fresh.StatusID)
				require.Equal(t, before.AssigneeID, fresh.AssigneeID)
				require.Equal(t, before.CheckoutToken, fresh.CheckoutToken)
				require.Equal(t, before.Version, fresh.Version)
				var events int
				require.NoError(t, f.db.Get(&events, `SELECT count(*) FROM event_bus_messages WHERE task_id=$1 AND subject='task.moved'`, id))
				require.Zero(t, events)
				var auditCount, outboxCount int
				require.NoError(t, f.db.Get(&auditCount, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action IN ('task.moved','task.assigned','checkout_lease_expired')`, id))
				require.Zero(t, auditCount)
				require.NoError(t, f.db.Get(&outboxCount, `SELECT count(*) FROM task_event_outbox WHERE task_id=$1`, id))
				require.Zero(t, outboxCount)
			} else {
				require.NoError(t, err)
				require.Equal(t, f.statusID, fresh.StatusID)
				require.Nil(t, fresh.CheckoutToken)
				var activity int
				require.NoError(t, f.db.Get(&activity, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, id))
				require.Equal(t, 1, activity)
			}
		})
	}
}

func TestTaskDurableOutbox_RetryCrashConcurrent(t *testing.T) {
	f, repo, svc, id, _ := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
	position := float64(10)
	require.NoError(t, svc.MoveTask(ctx, id, MoveTaskInput{Position: &position}))
	var eventID uuid.UUID
	require.NoError(t, f.db.Get(&eventID, `SELECT id FROM task_event_outbox WHERE task_id=$1`, id))
	sink := pq.QuoteIdentifier("outbox_sink_" + uuid.New().String())
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
	_, err := f.db.Exec(`CREATE TABLE ` + sink + `(id uuid PRIMARY KEY)`)
	require.NoError(t, err)
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
	t.Cleanup(func() { _, cleanupErr := f.db.Exec(`DROP TABLE ` + sink); require.NoError(t, cleanupErr) })
	// Give the worker an isolated pending queue without modifying other tests.
	schemaName := "outbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	schema := pq.QuoteIdentifier(schemaName)
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
	_, err = f.db.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
	t.Cleanup(func() { _, e := f.db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); require.NoError(t, e) })
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
	_, err = f.db.Exec(`CREATE TABLE ` + schema + `.task_event_outbox (LIKE public.task_event_outbox INCLUDING ALL)`)
	require.NoError(t, err)
	_, err = f.db.Exec(`INSERT INTO `+schema+`.task_event_outbox SELECT * FROM public.task_event_outbox WHERE id=$1`, eventID)
	require.NoError(t, err)
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5437/mesh?sslmode=disable"
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	params := parsed.Query()
	params.Set("search_path", schemaName+",public")
	parsed.RawQuery = params.Encode()
	deliveryDB, err := sqlx.Connect("postgres", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, deliveryDB.Close()) })
	outbox := postgres.NewTaskOutboxRepo(deliveryDB)
	found, err := outbox.DeliverNext(context.Background(), func(_ context.Context, msg *domain.EventBusMessage) error {
		require.Equal(t, eventID, msg.ID)
		return fmt.Errorf("broker unavailable")
	})
	require.True(t, found)
	require.ErrorContains(t, err, "broker unavailable")
	var pending, attempts int
	require.NoError(t, deliveryDB.Get(&pending, `SELECT count(*) FROM task_event_outbox WHERE id=$1 AND delivered_at IS NULL AND available_at>now()`, eventID))
	require.Equal(t, 1, pending)
	require.NoError(t, deliveryDB.Get(&attempts, `SELECT attempts FROM task_event_outbox WHERE id=$1`, eventID))
	require.Equal(t, 1, attempts)
	_, err = deliveryDB.Exec(`UPDATE task_event_outbox SET available_at=now() WHERE id=$1`, eventID)
	require.NoError(t, err)
	persist := func(msg *domain.EventBusMessage) error {
		require.Equal(t, eventID, msg.ID)
		_, e := f.db.Exec(`INSERT INTO `+sink+`(id) VALUES($1) ON CONFLICT(id) DO NOTHING`, msg.ID)
		return e
	}
	// Sink accepted publish; process lost before ack. Cancel the real PG tx so
	// delivery state rolls back while the independent sink insert stays committed.
	crashCtx, cancel := context.WithCancel(context.Background())
	found, err = outbox.DeliverNext(crashCtx, func(_ context.Context, msg *domain.EventBusMessage) error { e := persist(msg); cancel(); return e })
	require.True(t, found)
	require.Error(t, err)
	require.NoError(t, deliveryDB.Get(&pending, `SELECT count(*) FROM task_event_outbox WHERE id=$1 AND delivered_at IS NULL`, eventID))
	require.Equal(t, 1, pending)
	// Two workers race on the retry while the first holds a deterministic barrier.
	published, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	go func() {
		_, e := outbox.DeliverNext(context.Background(), func(_ context.Context, msg *domain.EventBusMessage) error {
			e := persist(msg)
			close(published)
			<-release
			return e
		})
		done <- e
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach publish barrier")
	}
	found, err = outbox.DeliverNext(context.Background(), func(context.Context, *domain.EventBusMessage) error {
		t.Error("concurrent worker must skip locked event")
		return nil
	})
	require.NoError(t, err)
	require.False(t, found)
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, <-done)
	var delivered, sinkCount, feed int
	require.NoError(t, deliveryDB.Get(&delivered, `SELECT count(*) FROM task_event_outbox WHERE id=$1 AND delivered_at IS NOT NULL`, eventID))
	require.Equal(t, 1, delivered)
	require.NoError(t, f.db.Get(&sinkCount, `SELECT count(*) FROM `+sink))
	require.Equal(t, 1, sinkCount, "crash retry must use the original sink idempotency key")
	require.NoError(t, f.db.Get(&feed, `SELECT count(*) FROM event_bus_messages WHERE id=$1`, eventID))
	require.Equal(t, 1, feed)
	fresh, e := repo.GetByID(ctx, id)
	require.NoError(t, e)
	require.Equal(t, position, fresh.Position)
	// Run the production scheduler against this isolated real queue: a failed
	// transport remains pending and a later tick completes the same event.
	_, err = deliveryDB.Exec(`UPDATE task_event_outbox SET attempts=0,delivered_at=NULL,available_at=now() WHERE id=$1`, eventID)
	require.NoError(t, err)
	workerCtx, stop := context.WithCancel(context.Background())
	defer stop()
	finished := make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(finished)
		RunTaskOutbox(workerCtx, outbox, func(_ context.Context, msg *domain.EventBusMessage) error {
			if msg.ID != eventID {
				return fmt.Errorf("unexpected worker event")
			}
			if calls.Add(1) == 1 {
				return fmt.Errorf("transient broker failure")
			}
			return persist(msg)
		})
	}()
	// Why 30s and not the original 6s (#da03cd09). This phase has a built-in
	// floor of 3-4s that the old budget did not account for: RunTaskOutbox
	// ticks once a second (task_outbox_worker.go), the first tick's failed
	// publish sets available_at = now + 2^(attempts+1)s = 2s
	// (TaskOutboxRepo.DeliverNext, task_outbox.go), and the retry is picked up
	// only on the first tick after that — measured 3.02-4.02s locally under
	// -race -count=20, never anything else. So 6s left about 2s for every DB
	// round trip of two ticks plus the polling. With Postgres starved (0.02
	// CPU, two concurrent package runs) the phase stretched to 3.6-7.1s; CI
	// failed it twice in 14 days at 7.3s and 11.5s total test time (jobs
	// 171491, 176072, both green on retry of the same SHA). No race: the row
	// lock is held only inside DeliverNext and the previous phase's worker has
	// committed (<-done) before this one starts. The deadline is only a hang
	// detector; a green run still finishes in ~4s.
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for delivered = 0; delivered == 0; {
		select {
		case <-deadline.C:
			t.Fatal("worker did not complete retry")
		case <-poll.C:
			require.NoError(t, deliveryDB.Get(&delivered, `SELECT count(*) FROM task_event_outbox WHERE id=$1 AND delivered_at IS NOT NULL`, eventID))
		}
	}
	stop()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("worker failed to stop")
	}
	require.Equal(t, int32(2), calls.Load())
}

func TestTaskDurableAudit_ExpiredUnleasedAssignment(t *testing.T) {
	for _, mode := range []string{"expired", "unleased"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, _, id, owner := newCheckoutM1Fixture(t)
			svc := NewTaskService(repo, postgres.NewTaskStatusRepo(f.db), nil, postgres.NewActivityLogRepo(f.db), WithProjectRepo(postgres.NewProjectRepo(f.db)), WithTaskAgentRepo(postgres.NewAgentRepo(f.db)), WithProjectMemberRepoTask(postgres.NewProjectMemberRepo(f.db)))
			ctx := actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem)
			ip := uuid.New()
			_, err := f.db.Exec(`INSERT INTO task_statuses(id,project_id,name,slug,category) VALUES($1,$2,'Active','active','in_progress')`, ip, f.projectID)
			require.NoError(t, err)
			_, err = f.db.Exec(`UPDATE tasks SET status_id=$2,updated_at=now()-interval '3 hours' WHERE id=$1`, id, ip)
			require.NoError(t, err)
			if mode == "expired" {
				_, err = repo.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(-time.Minute), checkoutScope())
				require.NoError(t, err)
			}
			before, err := repo.GetByID(ctx, id)
			require.NoError(t, err)
			action := activityCheckoutUnleasedReturned
			if mode == "expired" {
				action = activityCheckoutLeaseExpired
			}
			require.NoError(t, svc.MoveTask(ctx, id, reaperMoveInput(before, &f.statusID, action)))
			var count int
			require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action IN ('task.moved',$2)
    AND source=$3 AND reason=$2 AND actor_type='system' AND actor_id=$4
    AND old_version=$5 AND new_version=$5+1 AND lease_generation=$6
    AND previous_holder IS NOT DISTINCT FROM $7::uuid AND event_id=id`, id, action, "lease_reaper."+mode, uuid.Nil, before.Version, before.CheckoutGeneration, before.CheckedOutBy))
			require.Equal(t, 2, count)
			// Standalone assignment has its own action, never increments move counters.
			require.NoError(t, svc.AssignTask(actorctx.WithActor(ctx, owner, domain.ActorTypeAgent), id, AssignTaskInput{AssigneeID: &owner, AssigneeType: domain.AssigneeTypeAgent}))
			require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.moved'`, id))
			require.Equal(t, 1, count)
			require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.assigned' AND actor_id=$2 AND actor_type='agent' AND source='api' AND new_version=old_version+1`, id, owner))
			require.Equal(t, 1, count)
			entries, err := postgres.NewActivityLogRepo(f.db).ListByTask(ctx, id, pagination.Params{Page: 1, PageSize: 20})
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(entries.Items), 3)
			rows, queryErr := f.db.QueryxContext(ctx, durableAuditSQL(t), id)
			require.NoError(t, queryErr)
			var leaseEntries, moveEntries, assignmentEntries int
			for rows.Next() {
				data := map[string]interface{}{}
				require.NoError(t, rows.MapScan(data))
				switch data["action"] {
				case action:
					leaseEntries++
					require.Equal(t, "lease_reaper."+mode, data["source"])
				case "task.moved":
					moveEntries++
				case "task.assigned":
					assignmentEntries++
				}
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			require.Equal(t, 1, leaseEntries)
			require.Equal(t, 1, moveEntries)
			require.Equal(t, 1, assignmentEntries)

		})
	}
}

func TestMoveTask_DurableTerminalLeaseRollback(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			f, repo, svc, id, owner := newCheckoutM1Fixture(t)
			ctx := actorctx.WithActor(context.Background(), owner, domain.ActorTypeAgent)
			terminal := uuid.New()
			_, err := f.db.Exec(`INSERT INTO task_statuses(id,project_id,name,slug,category) VALUES($1,$2,'Review','review','review')`, terminal, f.projectID)
			require.NoError(t, err)
			_, err = repo.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(time.Hour), checkoutScope())
			require.NoError(t, err)
			before, err := repo.GetByID(ctx, id)
			require.NoError(t, err)
			if reject {
				f.db.SetMaxOpenConns(1)
				f.db.SetMaxIdleConns(1)
				schema := pq.QuoteIdentifier("terminal_" + uuid.NewString())
				// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
				_, err = f.db.Exec(`CREATE SCHEMA ` + schema)
				require.NoError(t, err)
				t.Cleanup(func() {
					// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
					_, e := f.db.Exec(`SET search_path=public; DROP SCHEMA ` + schema + ` CASCADE`)
					require.NoError(t, e)
				})
				// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
				_, err = f.db.Exec(`CREATE TABLE ` + schema + `.activity_log(LIKE public.activity_log INCLUDING ALL); ALTER TABLE ` + schema + `.activity_log ADD CHECK(action<>'task.checkout_released_auto'); SET search_path=` + schema + `,public`)
				require.NoError(t, err)
			}
			err = svc.MoveTask(ctx, id, MoveTaskInput{StatusID: &terminal})
			after, e := repo.GetByID(ctx, id)
			require.NoError(t, e)
			if reject {
				require.Error(t, err)
				require.Equal(t, before.StatusID, after.StatusID)
				require.Equal(t, before.CheckoutToken, after.CheckoutToken)
				require.Equal(t, before.Version, after.Version)
			} else {
				require.NoError(t, err)
				require.Nil(t, after.CheckoutToken)
				require.Equal(t, before.Version+1, after.Version)
				var count int
				require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM activity_log WHERE entity_id=$1 AND action='task.checkout_released_auto' AND previous_holder=$2 AND new_version=$3`, id, owner, after.Version))
				require.Equal(t, 1, count)
			}
		})
	}
}

func TestTaskDurableAudit_ReadonlyWindow(t *testing.T) {
	f, repo, svc, id, owner := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), owner, domain.ActorTypeAgent)
	position := float64(25)
	require.NoError(t, svc.MoveTask(ctx, id, MoveTaskInput{Position: &position, Source: "mcp", CorrelationID: &owner}))
	query := durableAuditSQL(t)
	tx, err := f.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryxContext(ctx, query, id)
	require.NoError(t, err)
	var count int
	for rows.Next() {
		data := map[string]interface{}{}
		require.NoError(t, rows.MapScan(data))
		require.Equal(t, "mcp", data["source"])
		count++
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, 1, count)
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET position=0 WHERE id=$1`, id)
	require.ErrorContains(t, err, "read-only transaction")
	require.NoError(t, tx.Rollback())
	_, err = f.db.Exec(`UPDATE activity_log SET created_at=now()-interval '25 hours' WHERE entity_id=$1`, id)
	require.NoError(t, err)
	rows, err = f.db.QueryxContext(ctx, query, id)
	require.NoError(t, err)
	require.False(t, rows.Next())
	require.NoError(t, rows.Close())
	fresh, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, position, fresh.Position)
}

func durableAuditSQL(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("../../scripts/audit/task-transition.sql")
	require.NoError(t, err)
	query := string(content)
	start := strings.Index(query, "SELECT id AS event_id")
	require.GreaterOrEqual(t, start, 0)
	query = query[start:]
	end := strings.Index(query, ";")
	require.Greater(t, end, 0)
	query = query[:end]
	query = strings.ReplaceAll(query, ":'task_id'::uuid", "$1::uuid")

	return query
}

// mock: external boundary — broker transport errors without publishing callbacks.
type durableBrokerProbe struct {
	err error
	id  uuid.UUID
}

func (p *durableBrokerProbe) PublishEvent(context.Context, *domain.EventBusMessage, string, string) error {
	return fmt.Errorf("legacy publish must not run")
}
func (p *durableBrokerProbe) PublishCommittedEvent(_ context.Context, msg *domain.EventBusMessage, _, _ string) error {
	p.id = msg.ID
	return p.err
}
func TestTaskDurableCommittedTransport(t *testing.T) {
	f, _, _, id, _ := newCheckoutM1Fixture(t)
	s := NewEventBusService(postgres.NewEventBusMessageRepo(f.db), nil).(*eventBusService)
	msg := &domain.EventBusMessage{ID: uuid.New(), WorkspaceID: f.workspaceID, ProjectID: f.projectID, TaskID: &id}
	require.ErrorContains(t, s.PublishCommitted(context.Background(), msg), "unavailable")
	broker := &durableBrokerProbe{err: fmt.Errorf("NATS down")}
	s.SetEventBus(broker, postgres.NewWorkspaceRepo(f.db), postgres.NewProjectRepo(f.db))
	require.ErrorContains(t, s.PublishCommitted(context.Background(), msg), "NATS down")
	require.Equal(t, msg.ID, broker.id)
	broker.err = nil
	require.NoError(t, s.PublishCommitted(context.Background(), msg))
	require.Equal(t, msg.ID, broker.id)
}

// Audit rows are stamped with application time, which may run ahead of the DB
// clock; the audit window must still include them.
func TestTaskDurableAudit_FutureStampedRowsIncluded(t *testing.T) {
	f, _, svc, id, owner := newCheckoutM1Fixture(t)
	ctx := actorctx.WithActor(context.Background(), owner, domain.ActorTypeAgent)
	position := float64(25)
	require.NoError(t, svc.MoveTask(ctx, id, MoveTaskInput{Position: &position, Source: "mcp", CorrelationID: &owner}))
	_, err := f.db.Exec(`UPDATE activity_log SET created_at=now()+interval '5 minutes' WHERE entity_id=$1`, id)
	require.NoError(t, err)
	_, err = f.db.Exec(`UPDATE task_event_outbox SET created_at=now()+interval '5 minutes' WHERE task_id=$1`, id)
	require.NoError(t, err)

	rows, err := f.db.QueryxContext(ctx, durableAuditSQL(t), id)
	require.NoError(t, err)
	count := 0
	for rows.Next() {
		count++
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, 1, count)

	content, err := os.ReadFile("../../scripts/audit/task-transition.sql")
	require.NoError(t, err)
	outbox := string(content)
	start := strings.Index(outbox, "SELECT id AS event_id, task_version")
	require.GreaterOrEqual(t, start, 0)
	outbox = outbox[start:]
	end := strings.Index(outbox, ";")
	require.Greater(t, end, 0)
	outbox = strings.ReplaceAll(outbox[:end], ":'task_id'::uuid", "$1::uuid")
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM (`+outbox+`) q`, id))
	require.Equal(t, 1, count)
}
