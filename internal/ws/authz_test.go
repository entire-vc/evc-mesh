package ws

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMockAuthorizer(t *testing.T) (Authorizer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	return NewDBAuthorizer(sqlx.NewDb(db, "postgres")), mock, func() { _ = db.Close() }
}

func TestDBAuthorizer_WorkspaceIDBySlug(t *testing.T) {
	authz, mock, closeDB := newMockAuthorizer(t)
	defer closeDB()

	want := uuid.New()
	mock.ExpectQuery("SELECT id FROM workspaces").
		WithArgs("ws-11111111").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(want))

	got, err := authz.WorkspaceIDBySlug(context.Background(), "ws-11111111")
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestDBAuthorizer_WorkspaceIDBySlug_Unknown: an unknown slug and a database
// error must be indistinguishable to the caller, which answers 403 either way —
// the handshake is not allowed to become a slug oracle.
func TestDBAuthorizer_WorkspaceIDBySlug_Unknown(t *testing.T) {
	authz, mock, closeDB := newMockAuthorizer(t)
	defer closeDB()

	mock.ExpectQuery("SELECT id FROM workspaces").
		WithArgs("ws-deadbeef").
		WillReturnError(errors.New("no rows"))

	_, err := authz.WorkspaceIDBySlug(context.Background(), "ws-deadbeef")
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDBAuthorizer_ProjectWorkspaceID(t *testing.T) {
	authz, mock, closeDB := newMockAuthorizer(t)
	defer closeDB()

	projectID := uuid.New()
	wsID := uuid.New()
	mock.ExpectQuery("SELECT workspace_id FROM projects").
		WithArgs(projectID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))

	got, err := authz.ProjectWorkspaceID(context.Background(), projectID)
	require.NoError(t, err)
	assert.Equal(t, wsID, got)

	missing := uuid.New()
	mock.ExpectQuery("SELECT workspace_id FROM projects").
		WithArgs(missing).
		WillReturnError(errors.New("no rows"))
	_, err = authz.ProjectWorkspaceID(context.Background(), missing)
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDBAuthorizer_UserIsWorkspaceMember(t *testing.T) {
	authz, mock, closeDB := newMockAuthorizer(t)
	defer closeDB()

	wsID := uuid.New()
	member := uuid.New()
	mock.ExpectQuery("SELECT role FROM workspace_members").
		WithArgs(wsID, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))
	assert.True(t, authz.UserIsWorkspaceMember(context.Background(), wsID, member))

	// A stranger: no membership row, and not the owner either.
	stranger := uuid.New()
	mock.ExpectQuery("SELECT role FROM workspace_members").
		WithArgs(wsID, stranger).
		WillReturnError(errors.New("no rows"))
	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(wsID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(uuid.New()))
	assert.False(t, authz.UserIsWorkspaceMember(context.Background(), wsID, stranger))

	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestDBAuthorizer_NilDBFailsClosed: a miswired authorizer must refuse everyone
// rather than admit anyone.
func TestDBAuthorizer_NilDBFailsClosed(t *testing.T) {
	authz := NewDBAuthorizer(nil)

	_, err := authz.WorkspaceIDBySlug(context.Background(), "ws-11111111")
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
	_, err = authz.ProjectWorkspaceID(context.Background(), uuid.New())
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
	assert.False(t, authz.UserIsWorkspaceMember(context.Background(), uuid.New(), uuid.New()))
}

// TestClient_sendDenial checks the refusal a client gets back: it names the
// channel the client itself asked for and nothing else.
func TestClient_sendDenial(t *testing.T) {
	c := NewClient(nil, nil, Principal{UserID: uuid.New()}, nil)
	c.sendDenial("ws:ws-somebody-else")

	select {
	case msg := <-c.Send:
		assert.Contains(t, string(msg), "subscribe.denied")
		assert.Contains(t, string(msg), "forbidden")
		assert.Contains(t, string(msg), "ws:ws-somebody-else")
	default:
		t.Fatal("no denial was sent")
	}
}

// TestClient_sendDenial_FullBufferDoesNotBlock: a client that has stopped reading
// must not wedge the read pump.
func TestClient_sendDenial_FullBufferDoesNotBlock(t *testing.T) {
	c := NewClient(nil, nil, Principal{UserID: uuid.New()}, nil)
	for i := 0; i < sendBufferSize; i++ {
		c.Send <- []byte("filler")
	}
	c.sendDenial("project:" + uuid.New().String()) // must return, not block
}
