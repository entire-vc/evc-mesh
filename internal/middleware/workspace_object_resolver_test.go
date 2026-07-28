package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkspaceRLS_ResolvesFlatObjectParams walks every entry in
// workspaceObjectResolvers through WorkspaceRLS and asserts the workspace it
// produced is tagged as coming from the path — which is what makes
// RequireWorkspaceMemberScoped check it rather than wave it through.
//
// Before these resolvers existed, each of these parameters resolved to nothing:
// the request reached the handler with either no workspace at all or, on an agent
// key, the caller's own — and the caller's own workspace says nothing about the
// object they asked for.
func TestWorkspaceRLS_ResolvesFlatObjectParams(t *testing.T) {
	for _, r := range workspaceObjectResolvers {
		t.Run(r.param, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			objectWS := uuid.New()
			objectID := uuid.New()

			mock.ExpectQuery("SELECT").
				WithArgs(objectID).
				WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(objectWS))
			mock.ExpectQuery("set_config").
				WithArgs(objectWS.String()).
				WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(objectWS.String()))

			e := echo.New()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
			c.SetPath("/api/v1/x/:" + r.param)
			c.SetParamNames(r.param)
			c.SetParamValues(objectID.String())
			c.Set(ContextKeyAuthType, AuthTypeAgent) // skips the workspace_role lookup

			var reached bool
			handler := WorkspaceRLS(sqlx.NewDb(db, "postgres"), nil)(func(echo.Context) error {
				reached = true
				return nil
			})
			require.NoError(t, handler(c))
			require.True(t, reached)

			assert.Equal(t, objectWS, c.Get(ContextKeyWorkspaceID),
				"WorkspaceRLS did not resolve the workspace from :%s", r.param)
			assert.Equal(t, WorkspaceSourceParam, c.Get(ContextKeyWorkspaceSource),
				"the workspace resolved from :%s is not tagged as coming from the path, so the guard skips it", r.param)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestWorkspaceRLS_FlatObjectParamUnknownIDResolvesNothing: an id that matches no
// row must leave the context without a workspace, so the guard refuses rather than
// falling back to the caller's own workspace.
func TestWorkspaceRLS_FlatObjectParamUnknownIDResolvesNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/events/:event_id")
	c.SetParamNames("event_id")
	c.SetParamValues(uuid.New().String())
	c.Set(ContextKeyAuthType, AuthTypeUser)

	require.NoError(t, WorkspaceRLS(sqlx.NewDb(db, "postgres"), nil)(nopHandler)(c))
	assert.Nil(t, c.Get(ContextKeyWorkspaceID))
	assert.Nil(t, c.Get(ContextKeyWorkspaceSource))
}

// TestResolveWorkspaceByTaskPrefix covers the one resolver that is not a lookup by
// primary key. A short task id is a hex prefix matched with LIKE, so it can name
// more than one task — including two tasks in two different tenants. There is no
// single workspace to check membership of then, and picking either one is the leak
// the resolver exists to close, so an ambiguous prefix must resolve to nothing.
func TestResolveWorkspaceByTaskPrefix(t *testing.T) {
	wsID := uuid.New()

	t.Run("one match resolves", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		mock.ExpectQuery("FROM tasks").
			WithArgs("abc123%").
			WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))

		got, ok := resolveWorkspaceByTaskPrefix(context.Background(), sqlx.NewDb(db, "postgres"), "abc123")
		assert.True(t, ok)
		assert.Equal(t, wsID, got)
	})

	t.Run("ambiguous prefix resolves nothing", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		mock.ExpectQuery("FROM tasks").
			WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID).AddRow(uuid.New()))

		_, ok := resolveWorkspaceByTaskPrefix(context.Background(), sqlx.NewDb(db, "postgres"), "abc123")
		assert.False(t, ok, "a prefix shared by two tenants resolved to one of them")
	})

	t.Run("uppercase is matched case-insensitively", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		mock.ExpectQuery("FROM tasks").
			WithArgs("abcdef%").
			WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))

		_, ok := resolveWorkspaceByTaskPrefix(context.Background(), sqlx.NewDb(db, "postgres"), "ABCDEF")
		assert.True(t, ok, "the handler lowercases the prefix before looking it up; the guard must agree")
	})

	// A non-hex value must not reach the LIKE pattern at all: '%' or '_' from the
	// caller would turn the lookup into a wildcard match over every tenant's tasks.
	for _, bad := range []string{"%", "abc", "abcdefabcdefa", "abc%12", "abc_12", "../etc", ""} {
		t.Run("rejected: "+bad, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			_, ok := resolveWorkspaceByTaskPrefix(context.Background(), sqlx.NewDb(db, "postgres"), bad)
			assert.False(t, ok)
			assert.NoError(t, mock.ExpectationsWereMet(), "a malformed short id reached the database")
		})
	}
}

// TestWorkspaceRLS_TaskShortIDResolves pins the other half of the short id: every
// /tasks/:task_id route accepts a hex prefix in place of the uuid, so the guard
// has to resolve one too. Otherwise widening the guard would 403 members on their
// own tasks whenever they used the short form — a security fix that reads as an
// outage.
func TestWorkspaceRLS_TaskShortIDResolves(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	wsID := uuid.New()
	mock.ExpectQuery("FROM tasks").
		WithArgs("a1b2c3%").
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))
	mock.ExpectQuery("set_config").
		WithArgs(wsID.String()).
		WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(wsID.String()))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues("a1b2c3")
	c.Set(ContextKeyAuthType, AuthTypeAgent)

	require.NoError(t, WorkspaceRLS(sqlx.NewDb(db, "postgres"), nil)(nopHandler)(c))
	assert.Equal(t, wsID, c.Get(ContextKeyWorkspaceID))
	assert.Equal(t, WorkspaceSourceParam, c.Get(ContextKeyWorkspaceSource))
}
