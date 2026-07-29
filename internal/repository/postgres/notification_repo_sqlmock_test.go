package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func newNotifRepoMock(t *testing.T) (*NotificationRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewNotificationRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// TestUpsertPreference_ConflictsOnTheSubjectKey is the duplicate-row defect.
//
// The statement used to say ON CONFLICT (id), and the caller never has an
// existing row's id to supply — it is looking the row up BY workspace, subject
// and channel. So every call arrived with a freshly generated uuid, collided with
// nothing, and inserted. The table grew by one row per PUT and the update the
// user asked for never happened.
//
// What is asserted is the conflict target, because that is the entire fix: a
// statement that conflicts on the primary key is indistinguishable from a plain
// INSERT here.
func TestUpsertPreference_ConflictsOnTheSubjectKey(t *testing.T) {
	userID := uuid.New()
	agentID := uuid.New()

	t.Run("a user row keys on (workspace, user, channel)", func(t *testing.T) {
		repo, mock := newNotifRepoMock(t)

		mock.ExpectQuery(regexp.QuoteMeta(`ON CONFLICT (workspace_id, user_id, channel) WHERE user_id IS NOT NULL DO UPDATE`)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(uuid.New(), time.Now(), time.Now()))

		err := repo.UpsertPreference(context.Background(), &domain.NotificationPreference{
			WorkspaceID: uuid.New(),
			UserID:      &userID,
			Channel:     "web_push",
			Events:      pq.StringArray{"comment.created"},
			IsEnabled:   true,
		})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("an agent row keys on (workspace, agent, channel)", func(t *testing.T) {
		repo, mock := newNotifRepoMock(t)

		mock.ExpectQuery(regexp.QuoteMeta(`ON CONFLICT (workspace_id, agent_id, channel) WHERE agent_id IS NOT NULL DO UPDATE`)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(uuid.New(), time.Now(), time.Now()))

		err := repo.UpsertPreference(context.Background(), &domain.NotificationPreference{
			WorkspaceID: uuid.New(),
			AgentID:     &agentID,
			Channel:     "web_push",
			Events:      pq.StringArray{"comment.created"},
			IsEnabled:   true,
		})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("a row naming neither is refused before it reaches the database", func(t *testing.T) {
		repo, mock := newNotifRepoMock(t)

		err := repo.UpsertPreference(context.Background(), &domain.NotificationPreference{
			WorkspaceID: uuid.New(),
			Channel:     "web_push",
		})

		assert.ErrorIs(t, err, ErrNoPreferenceSubject)
		require.NoError(t, mock.ExpectationsWereMet(), "a subjectless preference was sent to the database")
	})
}

// TestDeletePreferenceBySubject_ScopesToTheWorkspace: the statement carries the
// workspace, so the caller's authorisation — which is over a workspace — and the
// rows the statement can reach cannot disagree. Deleting by id alone is how the
// composite-route bugs worked.
func TestDeletePreferenceBySubject_ScopesToTheWorkspace(t *testing.T) {
	wsID := uuid.New()
	userID := uuid.New()
	agentID := uuid.New()

	t.Run("user", func(t *testing.T) {
		repo, mock := newNotifRepoMock(t)
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM notification_preferences`)).
			WithArgs(wsID, "web_push", userID).
			WillReturnResult(sqlmock.NewResult(0, 1))

		n, err := repo.DeletePreferenceBySubject(context.Background(), wsID, &userID, nil, "web_push")
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("agent", func(t *testing.T) {
		repo, mock := newNotifRepoMock(t)
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM notification_preferences`)).
			WithArgs(wsID, "browser_push", agentID).
			WillReturnResult(sqlmock.NewResult(0, 1))

		n, err := repo.DeletePreferenceBySubject(context.Background(), wsID, nil, &agentID, "browser_push")
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no subject deletes nothing", func(t *testing.T) {
		repo, mock := newNotifRepoMock(t)

		n, err := repo.DeletePreferenceBySubject(context.Background(), wsID, nil, nil, "web_push")

		assert.ErrorIs(t, err, ErrNoPreferenceSubject)
		assert.Zero(t, n)
		require.NoError(t, mock.ExpectationsWereMet(),
			"a delete with no subject reached the database, where it would have matched every row in the workspace")
	})
}

// TestGetDeliverablePreferences_AsksWhoTheRowBelongsTo pins the membership
// predicates into the query itself.
//
// Its predecessor selected every enabled row with a matching workspace_id, and
// dispatch() — which filters only on the channel and the event type — delivered
// whatever came back. A preference row planted by an outsider was therefore a
// standing subscription to a stranger's workspace. The real-database proof is in
// tests/integration; this is the cheap guard that the predicates do not quietly
// go missing from the statement.
func TestGetDeliverablePreferences_AsksWhoTheRowBelongsTo(t *testing.T) {
	wsID := uuid.New()

	// A matcher that records the statement instead of comparing it: sqlmock does
	// not otherwise hand back the SQL it saw, and the assertion here is about the
	// statement's content rather than about which expectation it matched.
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	mock.ExpectQuery(".*").WithArgs(wsID).WillReturnRows(sqlmock.NewRows([]string{
		"id", "workspace_id", "user_id", "agent_id", "channel",
		"events", "is_enabled", "config", "created_at", "updated_at",
	}))

	repo := NewNotificationRepo(sqlx.NewDb(rawDB, "postgres"))
	prefs, err := repo.GetDeliverablePreferences(context.Background(), wsID)
	require.NoError(t, err)
	assert.Empty(t, prefs)
	require.NoError(t, mock.ExpectationsWereMet())

	for _, clause := range []string{
		"workspace_members", // the member rule
		"owner_id",          // and its owner fallback
		"agents",            // the agent rule
		"deleted_at IS NULL",
	} {
		assert.Contains(t, captured, clause,
			"GetDeliverablePreferences no longer establishes that the row's subscriber is in the workspace")
	}
}
