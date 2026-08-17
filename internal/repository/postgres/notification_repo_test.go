package postgres

import (
	"context"
	"errors"
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

// nowStub is a fixed timestamp for the created_at/updated_at columns these
// queries return; nothing under test reads its value.
func nowStub() time.Time { return time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC) }

func newNotificationRepoMock(t *testing.T) (*NotificationRepo, sqlmock.Sqlmock) {
	t.Helper()
	rawDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawDB.Close() })
	return NewNotificationRepo(sqlx.NewDb(rawDB, "postgres")), mock
}

// --- FilterWorkspaceMembers -------------------------------------------------

// TestFilterWorkspaceMembers_KeepsMembersAndDropsStrangers is the query the
// notification dispatcher leans on to decide who may be told the contents of a
// comment. A user id that comes back is a recipient; one that does not is not.
func TestFilterWorkspaceMembers_KeepsMembersAndDropsStrangers(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	wsID := uuid.New()
	member := uuid.New()
	stranger := uuid.New()

	mock.ExpectQuery("FROM workspace_members").
		WithArgs(wsID, pq.StringArray{member.String(), stranger.String()}).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(member))

	got, err := repo.FilterWorkspaceMembers(context.Background(), wsID, []uuid.UUID{member, stranger})
	require.NoError(t, err)

	assert.True(t, got[member], "a member of the workspace was dropped")
	assert.False(t, got[stranger], "a user with no membership was reported as one")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestFilterWorkspaceMembers_AsksAboutOwnershipToo pins the half of the
// membership rule that is easy to lose: the founding owner of a workspace has no
// workspace_members row, so a query that only reads that table would silently
// stop notifying the one person who owns the place.
//
// middleware.UserIsWorkspaceMember has the same fallback for the same reason.
func TestFilterWorkspaceMembers_AsksAboutOwnershipToo(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	wsID := uuid.New()
	owner := uuid.New()

	// The regexp is the assertion: the query has to consult workspaces.owner_id,
	// not just workspace_members.
	mock.ExpectQuery("owner_id").
		WithArgs(wsID, pq.StringArray{owner.String()}).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(owner))

	got, err := repo.FilterWorkspaceMembers(context.Background(), wsID, []uuid.UUID{owner})
	require.NoError(t, err)

	assert.True(t, got[owner], "the workspace owner is not treated as a member")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestFilterWorkspaceMembers_EmptyInputAsksNothing: a workspace with no
// subscribers must not produce a query with an empty array in it.
func TestFilterWorkspaceMembers_EmptyInputAsksNothing(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	got, err := repo.FilterWorkspaceMembers(context.Background(), uuid.New(), nil)
	require.NoError(t, err)

	assert.Empty(t, got)
	assert.NoError(t, mock.ExpectationsWereMet(), "an empty candidate set still hit the database")
}

// TestFilterWorkspaceMembers_QueryErrorIsReturned matters more than it looks:
// the caller is required to fail closed on this error, and it can only do that if
// the error reaches it instead of an empty-but-successful set.
func TestFilterWorkspaceMembers_QueryErrorIsReturned(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	userID := uuid.New()
	mock.ExpectQuery("FROM workspace_members").
		WillReturnError(errors.New("connection reset"))

	got, err := repo.FilterWorkspaceMembers(context.Background(), uuid.New(), []uuid.UUID{userID})

	require.Error(t, err)
	assert.Nil(t, got, "a failed membership lookup must not read as \"nobody is a member\"")
}

// TestFilterWorkspaceMembers_RowsErrorIsReturned covers the failure that arrives
// after the first row: a connection that dies mid-result would otherwise leave a
// partial member set looking like a complete answer, and the caller would fail
// closed on the wrong half of the workspace.
func TestFilterWorkspaceMembers_RowsErrorIsReturned(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	member := uuid.New()
	mock.ExpectQuery("FROM workspace_members").
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).
			AddRow(member).
			RowError(0, errors.New("connection reset")))

	got, err := repo.FilterWorkspaceMembers(context.Background(), uuid.New(), []uuid.UUID{member})

	require.Error(t, err)
	assert.Nil(t, got, "a truncated result was returned as the membership of the workspace")
}

func TestFilterWorkspaceMembers_ScanErrorIsReturned(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	userID := uuid.New()
	mock.ExpectQuery("FROM workspace_members").
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow("not-a-uuid"))

	_, err := repo.FilterWorkspaceMembers(context.Background(), uuid.New(), []uuid.UUID{userID})
	require.Error(t, err)
}

// --- DeletePreferenceForUser ------------------------------------------------

// TestDeletePreferenceForUser_ScopesTheDeleteToTheCaller: the user_id in the
// WHERE clause is the authorization for this route. Without it,
// DELETE /notifications/preferences/:pref_id would be a way to cancel other
// people's subscriptions by guessing ids.
func TestDeletePreferenceForUser_ScopesTheDeleteToTheCaller(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	prefID := uuid.New()
	userID := uuid.New()

	mock.ExpectExec("DELETE FROM notification_preferences").
		WithArgs(prefID, userID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	removed, err := repo.DeletePreferenceForUser(context.Background(), prefID, userID)
	require.NoError(t, err)

	assert.EqualValues(t, 1, removed)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestDeletePreferenceForUser_SomebodyElsesRowRemovesNothing is what the handler
// turns into a 404.
func TestDeletePreferenceForUser_SomebodyElsesRowRemovesNothing(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	mock.ExpectExec("DELETE FROM notification_preferences").
		WillReturnResult(sqlmock.NewResult(0, 0))

	removed, err := repo.DeletePreferenceForUser(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)

	assert.EqualValues(t, 0, removed)
}

func TestDeletePreferenceForUser_ErrorIsReturned(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	mock.ExpectExec("DELETE FROM notification_preferences").
		WillReturnError(errors.New("connection reset"))

	_, err := repo.DeletePreferenceForUser(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
}

// --- UpsertPreference -------------------------------------------------------

// TestUpsertPreference_UpdatesTheExistingRow is the behaviour the name always
// claimed. The previous implementation wrote ON CONFLICT (id) against an id it
// had just generated, so nothing ever conflicted and every call inserted: five
// toggles left five rows, the dispatcher walked all five, and no single row
// existed for an unsubscribe to delete.
func TestUpsertPreference_UpdatesTheExistingRow(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	wsID := uuid.New()
	userID := uuid.New()
	existing := uuid.New()

	mock.ExpectQuery("UPDATE notification_preferences").
		WithArgs(wsID, userID, "web_push", pq.StringArray{"comment.created"}, true, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(existing, nowStub(), nowStub()))

	pref := &domain.NotificationPreference{
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "web_push",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   true,
	}
	require.NoError(t, repo.UpsertPreference(context.Background(), pref))

	assert.Equal(t, existing, pref.ID, "a second call created a new row instead of updating the first")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestUpsertPreference_InsertsWhenThereIsNoRowYet is the other branch: the first
// subscription has nothing to update.
func TestUpsertPreference_InsertsWhenThereIsNoRowYet(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	wsID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("UPDATE notification_preferences").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}))
	mock.ExpectQuery("INSERT INTO notification_preferences").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(uuid.New(), nowStub(), nowStub()))

	pref := &domain.NotificationPreference{
		WorkspaceID: wsID,
		UserID:      &userID,
		Channel:     "web_push",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   true,
	}
	require.NoError(t, repo.UpsertPreference(context.Background(), pref))

	assert.NotEqual(t, uuid.Nil, pref.ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestUpsertPreference_AgentRowMatchesOnTheAgent: chk_single_actor means a row is
// either a user's or an agent's, and matching an agent preference on user_id
// would insert a duplicate every time.
func TestUpsertPreference_AgentRowMatchesOnTheAgent(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	agentID := uuid.New()
	existing := uuid.New()

	mock.ExpectQuery("agent_id = \\$2").
		WithArgs(sqlmock.AnyArg(), agentID, "web_push", sqlmock.AnyArg(), true, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow(existing, nowStub(), nowStub()))

	pref := &domain.NotificationPreference{
		WorkspaceID: uuid.New(),
		AgentID:     &agentID,
		Channel:     "web_push",
		Events:      pq.StringArray{"task.assigned"},
		IsEnabled:   true,
	}
	require.NoError(t, repo.UpsertPreference(context.Background(), pref))

	assert.Equal(t, existing, pref.ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestUpsertPreference_UpdateErrorIsNotRetriedAsAnInsert: a broken connection
// must not be mistaken for "no such row" and turned into a duplicate.
func TestUpsertPreference_UpdateErrorIsNotRetriedAsAnInsert(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	userID := uuid.New()
	mock.ExpectQuery("UPDATE notification_preferences").
		WillReturnError(errors.New("connection reset"))

	pref := &domain.NotificationPreference{
		WorkspaceID: uuid.New(),
		UserID:      &userID,
		Channel:     "web_push",
		Events:      pq.StringArray{"comment.created"},
		IsEnabled:   true,
	}
	require.Error(t, repo.UpsertPreference(context.Background(), pref))
	assert.NoError(t, mock.ExpectationsWereMet(), "an INSERT was attempted after a failed UPDATE")
}

// --- GetPreferencesByWorkspace ---------------------------------------------

// TestGetPreferencesByWorkspace_ReturnsTheTableAsItIs documents the contract the
// dispatcher depends on: this reader is deliberately unfiltered, which is why the
// caller has to reduce it with FilterWorkspaceMembers before delivering anything.
func TestGetPreferencesByWorkspace_ReturnsTheTableAsItIs(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)

	wsID := uuid.New()
	stranger := uuid.New()

	mock.ExpectQuery("FROM notification_preferences").
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "workspace_id", "user_id", "agent_id", "channel",
			"events", "is_enabled", "config", "created_at", "updated_at",
		}).AddRow(uuid.New(), wsID, stranger, nil, "web_push",
			pq.StringArray{"comment.created"}, true, []byte(`{}`), nowStub(), nowStub()))

	prefs, err := repo.GetPreferencesByWorkspace(context.Background(), wsID)
	require.NoError(t, err)

	require.Len(t, prefs, 1)
	assert.Equal(t, stranger, *prefs[0].UserID,
		"this reader must not filter — the membership decision belongs to the dispatcher, "+
			"where it can fail closed")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// --- ListUnread / CountUnread: deleted-workspace exclusion -------------------
//
// notification rows are written once, at event time, and never touched again
// by anything that later deletes the workspace they're about — unlike
// tasks/comments there is no cascade to lean on, so these two joins are the
// only thing standing between a deleted workspace and its notifications still
// showing up in every user's inbox forever.

func TestListUnread_ExcludesNotificationsFromADeletedWorkspace(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)
	userID := uuid.New()

	mock.ExpectQuery("FROM notifications n").
		WithArgs(userID, 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "workspace_id", "user_id", "event_type", "title", "body", "metadata", "is_read", "created_at",
		}))

	items, err := repo.ListUnread(context.Background(), userID, 50)
	require.NoError(t, err)
	assert.Empty(t, items)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestListUnread_SQL(t *testing.T) {
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewNotificationRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{
		"id", "workspace_id", "user_id", "event_type", "title", "body", "metadata", "is_read", "created_at",
	}))

	_, err = repo.ListUnread(context.Background(), uuid.New(), 50)
	require.NoError(t, err)

	assert.Contains(t, captured, "JOIN workspaces w ON w.id = n.workspace_id")
	assert.Contains(t, captured, "w.deleted_at IS NULL")
}

func TestCountUnread_ExcludesNotificationsFromADeletedWorkspace(t *testing.T) {
	repo, mock := newNotificationRepoMock(t)
	userID := uuid.New()

	mock.ExpectQuery("FROM notifications n").
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	count, err := repo.CountUnread(context.Background(), userID)
	require.NoError(t, err)
	assert.Zero(t, count)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestCountUnread_SQL(t *testing.T) {
	var captured string
	rawDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
		sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
			captured = actualSQL
			return nil
		})))
	require.NoError(t, err)
	defer func() { _ = rawDB.Close() }()

	repo := NewNotificationRepo(sqlx.NewDb(rawDB, "postgres"))
	mock.ExpectQuery(".*").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	_, err = repo.CountUnread(context.Background(), uuid.New())
	require.NoError(t, err)

	assert.Contains(t, captured, "JOIN workspaces w ON w.id = n.workspace_id")
	assert.Contains(t, captured, "w.deleted_at IS NULL")
}
