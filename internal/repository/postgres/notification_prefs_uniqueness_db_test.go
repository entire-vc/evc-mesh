package postgres

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The sqlmock tests next door assert which statements UpsertPreference issues.
// These assert what the table ends up containing, which is the part a mock
// cannot answer: "one row" is a property of the database, and the bug this
// covers was invisible in the response the caller got back either way.
//
// Untagged for the same reason as userRepoTestDB — CI's untagged `go test ./...`
// is what runs against a migrated DATABASE_URL. Skips when no DB is reachable.
func notifPrefsTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// notifPrefsFixture creates the workspace and user the foreign keys demand.
func notifPrefsFixture(t *testing.T, db *sqlx.DB) (wsID, userID uuid.UUID) {
	t.Helper()

	userID = uuid.New()
	handle := "prefs-" + userID.String()[:8]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username, is_active)
		 VALUES ($1, $2, 'not-a-real-hash', 'Prefs Test', $3, true)`,
		userID, handle+"@example.test", handle,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM users WHERE id = $1`, userID) })

	wsID = uuid.New()
	_, err = db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id) VALUES ($1, 'Prefs Test', $2, $3)`,
		wsID, "prefs-ws-"+wsID.String()[:8], userID,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID) })

	return wsID, userID
}

func countPrefs(t *testing.T, db *sqlx.DB, wsID, userID uuid.UUID, channel string) int {
	t.Helper()
	var n int
	require.NoError(t, db.Get(&n,
		`SELECT count(*) FROM notification_preferences
		 WHERE workspace_id = $1 AND user_id = $2 AND channel = $3`,
		wsID, userID, channel))
	return n
}

// TestUpsertPreferenceDB_SecondSaveUpdatesTheRowInsteadOfAddingOne is the
// duplicate-row bug stated as the user sees it: saving a setting twice is an
// ordinary thing to do, and it used to leave the table one row larger each time
// while the setting itself never changed.
//
// The assertion reads the table rather than the returned struct. The old code
// returned a perfectly plausible preference object on every call; the damage was
// only ever visible in what had accumulated behind it.
func TestUpsertPreferenceDB_SecondSaveUpdatesTheRowInsteadOfAddingOne(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	wsID, userID := notifPrefsFixture(t, db)

	first := &domain.NotificationPreference{
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "web_push",
		Events:      pq.StringArray{"task.assigned"},
		IsEnabled:   true,
	}
	require.NoError(t, repo.UpsertPreference(ctx, first))

	second := &domain.NotificationPreference{
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "web_push",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   false,
	}
	require.NoError(t, repo.UpsertPreference(ctx, second))

	assert.Equal(t, 1, countPrefs(t, db, wsID, userID, "web_push"),
		"saving the same preference twice left more than one row")
	assert.Equal(t, first.ID, second.ID, "the second save created a new row instead of updating the first")

	var events pq.StringArray
	var enabled bool
	require.NoError(t, db.QueryRow(
		`SELECT events, is_enabled FROM notification_preferences
		 WHERE workspace_id = $1 AND user_id = $2 AND channel = $3`,
		wsID, userID, "web_push").Scan(&events, &enabled))

	assert.Equal(t, pq.StringArray{"comment.created"}, events, "the second save did not take effect")
	assert.False(t, enabled, "is_enabled kept the first save's value")
}

// TestNotificationPrefs_TableRejectsASecondRowForTheSameActorAndChannel checks
// the constraint itself rather than the code path that respects it.
//
// UpsertPreference looking for the existing row is a rule the application
// follows; this is the one the table enforces. It is what stops a future caller
// — a backfill, an import, a second write path added later — from reintroducing
// the duplicates by going straight to SQL, which is exactly how the rows this
// migration had to clean up were created.
func TestNotificationPrefs_TableRejectsASecondRowForTheSameActorAndChannel(t *testing.T) {
	db := notifPrefsTestDB(t)
	ctx := context.Background()

	wsID, userID := notifPrefsFixture(t, db)

	insert := func() error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO notification_preferences (id, workspace_id, user_id, channel, events, is_enabled)
			 VALUES ($1, $2, $3, 'web_push', $4, true)`,
			uuid.New(), wsID, userID, pq.StringArray{"task.assigned"})
		return err
	}

	require.NoError(t, insert(), "the first preference row was rejected")

	err := insert()
	require.Error(t, err, "the table accepted a duplicate (workspace, user, channel) row")

	var pqErr *pq.Error
	require.ErrorAs(t, err, &pqErr)
	assert.EqualValues(t, "23505", pqErr.Code, "expected a unique-violation, got %s", pqErr.Code)
}

// TestUpsertPreferenceDB_AgentSubscriptionInsertsAndThenUpdates exercises the
// agent half of the insert, which is a different statement from the user half:
// the ON CONFLICT clause has to name the agent index and repeat its WHERE
// predicate, or PostgreSQL cannot infer which index is meant and rejects the
// statement outright.
//
// A wrong predicate there is not a subtle bug — it is every agent subscription
// failing — but it is invisible to a mock, which returns whatever rows the test
// supplied without ever parsing the clause. The existing agent test covers the
// UPDATE path only, so nothing had executed this string against the real index.
func TestUpsertPreferenceDB_AgentSubscriptionInsertsAndThenUpdates(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	wsID, _ := notifPrefsFixture(t, db)

	agentID := uuid.New()
	slug := "agent-" + agentID.String()[:8]
	_, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		 VALUES ($1, $2, 'Prefs Test Agent', $3, 'not-a-real-hash', 'test')`,
		agentID, wsID, slug,
	)
	require.NoError(t, err)

	first := &domain.NotificationPreference{
		WorkspaceID: wsID,
		AgentID:     &agentID,
		Channel:     "web_push",
		Events:      pq.StringArray{"task.assigned"},
		IsEnabled:   true,
	}
	require.NoError(t, repo.UpsertPreference(ctx, first), "the agent INSERT could not execute")

	second := &domain.NotificationPreference{
		WorkspaceID: wsID,
		AgentID:     &agentID,
		Channel:     "web_push",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   true,
	}
	require.NoError(t, repo.UpsertPreference(ctx, second))

	var rows int
	require.NoError(t, db.Get(&rows,
		`SELECT count(*) FROM notification_preferences
		 WHERE workspace_id = $1 AND agent_id = $2 AND channel = 'web_push'`,
		wsID, agentID))

	assert.Equal(t, 1, rows, "the agent's second save left a duplicate row")
	assert.Equal(t, first.ID, second.ID, "the agent's second save created a new row")
}

// TestNotificationPrefs_DifferentChannelsAreSeparateSubscriptions guards the
// obvious over-correction: the key includes the channel, so turning on in-app
// notifications must not overwrite the browser-push row.
func TestNotificationPrefs_DifferentChannelsAreSeparateSubscriptions(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)
	ctx := context.Background()

	wsID, userID := notifPrefsFixture(t, db)

	for _, channel := range []string{"web_push", "browser_push"} {
		require.NoError(t, repo.UpsertPreference(ctx, &domain.NotificationPreference{
			WorkspaceID: wsID,
			UserID:      &userID,
			Channel:     channel,
			Events:      pq.StringArray{"comment.created"},
			IsEnabled:   true,
		}))
	}

	assert.Equal(t, 1, countPrefs(t, db, wsID, userID, "web_push"))
	assert.Equal(t, 1, countPrefs(t, db, wsID, userID, "browser_push"),
		"the two channels were collapsed into one subscription")
}

// TestUpsertPreferenceDB_ConcurrentFirstSavesConvergeOnOneRow covers the gap the
// index closes that the lookup cannot: UpsertPreference is an UPDATE followed by
// an INSERT, so two callers saving the first preference for the same actor at the
// same moment both find nothing to update and both go on to insert.
//
// Without the unique index that is a second row and no error. With it, one
// insert wins and the rest resolve through ON CONFLICT into the update they
// meant — so the outcome is one row and no caller sees a failure. Both halves
// are asserted: dropping ON CONFLICT would satisfy the row count and fail the
// error check, which is the regression the index itself introduces.
//
// Read this as a smoke test, not as the proof. The race is real but hard to
// provoke — measured against the pre-index schema, 48 concurrent writers
// reproduced the duplicate in 1 run out of 12, and 6 and 24 writers never
// reproduced it at all, because the window between the UPDATE finding nothing
// and the INSERT landing is narrow enough that the scheduler usually serialises
// them. So a green run here is weak evidence and a red one is strong evidence.
// What holds the invariant deterministically is
// TestNotificationPrefs_TableRejectsASecondRowForTheSameActorAndChannel; this
// test's job is to notice if concurrent load starts returning errors to callers.
func TestUpsertPreferenceDB_ConcurrentFirstSavesConvergeOnOneRow(t *testing.T) {
	db := notifPrefsTestDB(t)
	repo := NewNotificationRepo(db)

	wsID, userID := notifPrefsFixture(t, db)

	const writers = 48
	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})

	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = repo.UpsertPreference(context.Background(), &domain.NotificationPreference{
				WorkspaceID: wsID,
				UserID:      &userID,
				Channel:     "web_push",
				Events:      pq.StringArray{"comment.created"},
				IsEnabled:   true,
			})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "concurrent writer %d failed instead of converging", i)
	}
	assert.Equal(t, 1, countPrefs(t, db, wsID, userID, "web_push"),
		"concurrent first saves raced past the lookup and left duplicate rows")
}
