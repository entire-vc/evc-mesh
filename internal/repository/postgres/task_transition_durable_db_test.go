package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Real database refusal controls run in the default CI test job, without tags.
func TestTaskTransitionDurableDB_AtomicRefusalAndConflict(t *testing.T) {
	for _, fault := range []string{"", "activity_log", "task_event_outbox", "event_bus_messages"} {
		t.Run("refuse="+fault, func(t *testing.T) {
			repo, id, owner := checkoutGenerationFixture(t)
			ctx := context.Background()
			lease, err := repo.AcquireCheckout(ctx, id, owner, uuid.New(), time.Now().Add(time.Hour), generationScope())
			require.NoError(t, err)
			before, err := repo.GetByID(ctx, id)
			require.NoError(t, err)
			if fault != "" {
				repo.db.SetMaxOpenConns(1)
				repo.db.SetMaxIdleConns(1)
				schema := pq.QuoteIdentifier("durable_repo_" + uuid.New().String())
				// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
				_, err = repo.db.Exec(`CREATE SCHEMA ` + schema + `; CREATE TABLE ` + schema + `.` + fault + ` (LIKE public.` + fault + ` INCLUDING ALL); ALTER TABLE ` + schema + `.` + fault + ` ADD CHECK (false); SET search_path=` + schema + `,public`)
				require.NoError(t, err)
				t.Cleanup(func() {
					// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
					_, e := repo.db.Exec(`SET search_path=public; DROP SCHEMA ` + schema + ` CASCADE`)
					require.NoError(t, e)
				})
			}
			next := *before
			next.Position = 42
			next.AssigneeID = &owner
			next.AssigneeType = domain.AssigneeTypeAgent
			next.UpdatedAt = time.Now()
			source := domain.AssignmentSourceSystem
			audit := &domain.TransitionAudit{ActorID: owner, ActorType: domain.ActorTypeAgent, Source: "mcp", Reason: "durable proof", SessionID: lease.SessionID, PreviousHolder: &owner, LeaseGeneration: lease.Generation, Entries: []domain.TaskAuditEntry{{Action: "task.moved", Changes: map[string]any{"position": 42}}, {Action: "task.assigned", Changes: map[string]any{"reason": "explicit"}}}}
			input := domain.TaskTransition{ExpectedVersion: before.Version, Audit: audit, AssignedBy: &source, ReleaseCheckout: true}
			err = repo.UpdateTransition(ctx, &next, input)
			fresh, readErr := repo.GetByID(ctx, id)
			require.NoError(t, readErr)
			if fault != "" {
				require.Error(t, err, "failure after UPDATE must abort the complete transaction")
				require.Equal(t, before.Version, fresh.Version)
				require.Equal(t, before.Position, fresh.Position)
				require.Equal(t, before.AssigneeID, fresh.AssigneeID)
				require.Equal(t, lease.Token, fresh.CheckoutToken)
				for _, table := range []string{"activity_log", "task_event_outbox", "event_bus_messages"} {
					var n int
					column := "task_id"
					if table == "activity_log" {
						column = "entity_id"
					}
					require.NoError(t, repo.db.Get(&n, `SELECT count(*) FROM `+table+` WHERE `+column+`=$1`, id))
					require.Zero(t, n)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, before.Version+1, fresh.Version)
			require.Equal(t, 42.0, fresh.Position)
			require.Equal(t, &owner, fresh.AssigneeID)
			require.Nil(t, fresh.CheckoutToken)
			require.Equal(t, lease.SessionID, fresh.CheckoutSessionID, "terminal tombstone remains")
			var n int
			require.NoError(t, repo.db.Get(&n, `SELECT count(*) FROM activity_log a JOIN task_event_outbox o ON o.id=a.event_id JOIN event_bus_messages e ON e.id=o.id WHERE a.entity_id=$1 AND a.new_version=$2 AND a.source='mcp' AND a.actor_id=$3`, id, fresh.Version, owner))
			require.Equal(t, 2, n)
			err = repo.UpdateTransition(ctx, &next, input)
			var conflict *domain.TaskConflict
			require.ErrorAs(t, err, &conflict)
			require.Equal(t, fresh.Version, conflict.CurrentVersion)
			require.NoError(t, repo.db.Get(&n, `SELECT count(*) FROM task_event_outbox WHERE task_id=$1`, id))
			require.Equal(t, 2, n, "stale CAS cannot duplicate the event")
		})
	}
}

func TestTaskOutboxDurableDB_RetryCrashAndConcurrentClaim(t *testing.T) {
	repo, id, owner := checkoutGenerationFixture(t)
	ctx := context.Background()
	before, err := repo.GetByID(ctx, id)
	require.NoError(t, err)
	before.Position = 3
	before.UpdatedAt = time.Now()
	audit := &domain.TransitionAudit{ActorID: owner, ActorType: domain.ActorTypeAgent, Source: "api", Entries: []domain.TaskAuditEntry{{Action: "task.moved", Changes: map[string]any{"position": 3}}}}
	require.NoError(t, repo.UpdateTransition(ctx, before, domain.TaskTransition{ExpectedVersion: before.Version, Audit: audit}))
	// Isolate candidates from concurrently running service-package fixtures.
	repo.db.SetMaxOpenConns(2)
	schema := pq.QuoteIdentifier("outbox_repo_" + uuid.New().String())
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
	_, err = repo.db.Exec(`CREATE SCHEMA ` + schema + `; CREATE TABLE ` + schema + `.task_event_outbox (LIKE public.task_event_outbox INCLUDING ALL)`)
	require.NoError(t, err)
	// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- test-only SQL identifiers are pq.QuoteIdentifier of generated UUIDs; table selectors are fixed literals, values are bound or canonical UUIDs; no external input
	t.Cleanup(func() { _, e := repo.db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); require.NoError(t, e) })
	_, err = repo.db.Exec(`INSERT INTO `+schema+`.task_event_outbox SELECT * FROM public.task_event_outbox WHERE task_id=$1`, id)
	require.NoError(t, err)
	parsed, err := url.Parse(os.Getenv("DATABASE_URL"))
	require.NoError(t, err)
	params := parsed.Query()
	params.Set("search_path", schema+",public")
	parsed.RawQuery = params.Encode()
	isolated, err := sqlx.Connect("postgres", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, isolated.Close()) })
	outbox := NewTaskOutboxRepo(isolated)
	var expected uuid.UUID
	require.NoError(t, isolated.Get(&expected, `SELECT id FROM task_event_outbox WHERE task_id=$1`, id))
	publishErr := errors.New("broker unavailable")
	found, err := outbox.DeliverNext(ctx, func(_ context.Context, msg *domain.EventBusMessage) error {
		require.Equal(t, expected, msg.ID)
		return publishErr
	})
	require.True(t, found)
	require.ErrorIs(t, err, publishErr)
	var attempts int
	require.NoError(t, isolated.Get(&attempts, `SELECT attempts FROM task_event_outbox WHERE id=$1 AND delivered_at IS NULL AND available_at>clock_timestamp()`, expected))
	require.Equal(t, 1, attempts)
	found, err = outbox.DeliverNext(ctx, func(context.Context, *domain.EventBusMessage) error { t.Fatal("backoff bypassed"); return nil })
	require.False(t, found)
	require.NoError(t, err)
	_, err = isolated.Exec(`UPDATE task_event_outbox SET available_at=now()`)
	require.NoError(t, err)
	crashCtx, cancel := context.WithCancel(ctx)
	found, err = outbox.DeliverNext(crashCtx, func(_ context.Context, msg *domain.EventBusMessage) error {
		require.Equal(t, expected, msg.ID)
		cancel()
		return nil
	})
	require.True(t, found)
	require.Error(t, err)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		_, e := outbox.DeliverNext(ctx, func(_ context.Context, msg *domain.EventBusMessage) error {
			if msg.ID != expected {
				return errors.New("unstable ID")
			}
			close(started)
			<-release
			return nil
		})
		done <- e
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not claim row")
	}
	found, err = outbox.DeliverNext(ctx, func(context.Context, *domain.EventBusMessage) error {
		t.Fatal("concurrent duplicate publish")
		return nil
	})
	unblock()
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, <-done)
	require.NoError(t, isolated.Get(&attempts, `SELECT attempts FROM task_event_outbox WHERE id=$1 AND delivered_at IS NOT NULL`, expected))
	require.Equal(t, 2, attempts)
	var delivered domain.EventBusMessage
	var raw []byte
	require.NoError(t, isolated.Get(&raw, `SELECT message FROM task_event_outbox WHERE id=$1`, expected))
	require.NoError(t, json.Unmarshal(raw, &delivered))
	require.Equal(t, expected, delivered.ID)
}
