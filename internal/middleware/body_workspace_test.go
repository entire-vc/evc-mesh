package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bodyWSContext builds a request the way RequireBodyWorkspace sees one: a JSON
// body, an authenticated actor, and no route parameters at all — which is the
// whole problem, since RequireWorkspaceMemberScoped has nothing to resolve from a
// path like /rules/evaluate and waves it through as if it were /auth/me.
func bodyWSContext(body, authType string, actorID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/api/v1/rules/evaluate")
	c.Set(ContextKeyAuthType, authType)
	if authType == AuthTypeAgent {
		c.Set(ContextKeyAgentID, actorID)
	} else {
		c.Set(ContextKeyUserID, actorID)
	}
	return c, rec
}

// TestRequireBodyWorkspace_NonMemberIsRefused is the reported repro for the
// tenant-in-the-body class. POST /rules/evaluate returned another tenant's rule
// names to any authenticated caller who pasted their workspace id into the body.
func TestRequireBodyWorkspace_NonMemberIsRefused(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	victimWS := uuid.New()
	stranger := uuid.New()

	// No membership row, and the owner fallback finds somebody else.
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(victimWS, stranger).
		WillReturnRows(sqlmock.NewRows([]string{"role"}))
	mock.ExpectQuery("SELECT owner_id FROM workspaces").
		WithArgs(victimWS).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(uuid.New()))

	c, rec := bodyWSContext(`{"action":"task.move","workspace_id":"`+victimWS.String()+`"}`, AuthTypeUser, stranger)

	reached := false
	guard := RequireBodyWorkspace(sqlx.NewDb(db, "postgres"))
	require.NoError(t, guard(func(echo.Context) error { reached = true; return nil })(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, reached, "the handler ran against another tenant's workspace")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireBodyWorkspace_AgentKeyIsRefused runs the same on an agent key.
//
// It is not a duplicate: rbac() short-circuits to a static capability map on an
// agent key and never looks at the target workspace, so an agent key was the one
// credential that walked through every "protected by rbac" route in this class.
func TestRequireBodyWorkspace_AgentKeyIsRefused(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	victimWS := uuid.New()
	intruderAgent := uuid.New()

	mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
		WithArgs(intruderAgent).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(uuid.New()))

	c, rec := bodyWSContext(`{"workspace_id":"`+victimWS.String()+`"}`, AuthTypeAgent, intruderAgent)

	reached := false
	guard := RequireBodyWorkspace(sqlx.NewDb(db, "postgres"))
	require.NoError(t, guard(func(echo.Context) error { reached = true; return nil })(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, reached, "an agent key reached another tenant's workspace")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireBodyWorkspace_MemberPassesAndBodyIsIntact is the half that decides
// whether this can ship. The guard reads the body; the handler binds it
// afterwards, so the body has to be exactly what the client sent.
func TestRequireBodyWorkspace_MemberPassesAndBodyIsIntact(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	ownWS := uuid.New()
	member := uuid.New()
	const body = `{"action":"task.move","workspace_id":"` + `%s` + `"}`
	raw := strings.Replace(body, "%s", ownWS.String(), 1)

	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(ownWS, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	c, rec := bodyWSContext(raw, AuthTypeUser, member)

	var seen string
	guard := RequireBodyWorkspace(sqlx.NewDb(db, "postgres"))
	require.NoError(t, guard(func(ctx echo.Context) error {
		got, readErr := io.ReadAll(ctx.Request().Body)
		require.NoError(t, readErr)
		seen = string(got)
		return nil
	})(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, raw, seen, "the guard consumed the body the handler needs to bind")
	assert.Equal(t, ownWS, c.Get(ContextKeyWorkspaceID))
	assert.Equal(t, WorkspaceSourceParam, c.Get(ContextKeyWorkspaceSource))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireBodyWorkspace_ProjectIDResolvesItsWorkspace: a body naming only a
// project still names a tenant, and the caller has to be in it.
func TestRequireBodyWorkspace_ProjectIDResolvesItsWorkspace(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	projID := uuid.New()
	projWS := uuid.New()
	member := uuid.New()

	mock.ExpectQuery("SELECT workspace_id FROM projects").
		WithArgs(projID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(projWS))
	mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
		WithArgs(projWS, member).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("member"))

	c, rec := bodyWSContext(`{"project_id":"`+projID.String()+`"}`, AuthTypeUser, member)

	guard := RequireBodyWorkspace(sqlx.NewDb(db, "postgres"))
	require.NoError(t, guard(nopHandler)(c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, projWS, c.Get(ContextKeyWorkspaceID))
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireBodyWorkspace_DisagreeingIDsAreRefused: a body naming a workspace
// and a project in a different workspace is a request naming two tenants. Same
// rule as the composite paths, same reason — checking it against whichever one is
// more convenient is the bug.
func TestRequireBodyWorkspace_DisagreeingIDsAreRefused(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	projID := uuid.New()
	mock.ExpectQuery("SELECT workspace_id FROM projects").
		WithArgs(projID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(uuid.New()))

	c, rec := bodyWSContext(
		`{"workspace_id":"`+uuid.New().String()+`","project_id":"`+projID.String()+`"}`,
		AuthTypeUser, uuid.New())

	reached := false
	guard := RequireBodyWorkspace(sqlx.NewDb(db, "postgres"))
	require.NoError(t, guard(func(echo.Context) error { reached = true; return nil })(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, reached)
}

// TestRequireBodyWorkspace_UnknownProjectIsRefused: an id that names no row is a
// refusal, not a pass-through — otherwise the endpoint reports which project ids
// exist.
func TestRequireBodyWorkspace_UnknownProjectIsRefused(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("SELECT workspace_id FROM projects").
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}))

	c, rec := bodyWSContext(`{"project_id":"`+uuid.New().String()+`"}`, AuthTypeUser, uuid.New())

	guard := RequireBodyWorkspace(sqlx.NewDb(db, "postgres"))
	require.NoError(t, guard(nopHandler)(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRequireBodyWorkspace_NoTenantInBodyPassesThrough: nothing in the body names
// a tenant, so there is nothing for this guard to check. The handler falls back
// to the caller's own workspace or refuses on its own terms.
func TestRequireBodyWorkspace_NoTenantInBodyPassesThrough(t *testing.T) {
	for _, body := range []string{`{"action":"task.move"}`, ``, `not json at all`, `{"workspace_id":null}`} {
		t.Run(body, func(t *testing.T) {
			c, rec := bodyWSContext(body, AuthTypeUser, uuid.New())

			reached := false
			guard := RequireBodyWorkspace(nil)
			require.NoError(t, guard(func(echo.Context) error { reached = true; return nil })(c))

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.True(t, reached, "a body naming no tenant was refused")
		})
	}
}

// TestActorMayAccessWorkspace covers the shared membership rule directly,
// including the shapes that must answer "no" rather than panicking: no database,
// the zero workspace, and an actor with no id on the context.
func TestActorMayAccessWorkspace(t *testing.T) {
	t.Run("nil db", func(t *testing.T) {
		c, _ := bodyWSContext(`{}`, AuthTypeUser, uuid.New())
		assert.False(t, ActorMayAccessWorkspace(c, nil, uuid.New()))
	})

	t.Run("zero workspace", func(t *testing.T) {
		db, _, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		c, _ := bodyWSContext(`{}`, AuthTypeUser, uuid.New())
		assert.False(t, ActorMayAccessWorkspace(c, sqlx.NewDb(db, "postgres"), uuid.Nil))
	})

	t.Run("agent with no id on the context", func(t *testing.T) {
		db, _, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		e := echo.New()
		c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), httptest.NewRecorder())
		c.Set(ContextKeyAuthType, AuthTypeAgent)
		assert.False(t, ActorMayAccessWorkspace(c, sqlx.NewDb(db, "postgres"), uuid.New()))
	})

	t.Run("user with no id on the context", func(t *testing.T) {
		db, _, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		e := echo.New()
		c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", http.NoBody), httptest.NewRecorder())
		c.Set(ContextKeyAuthType, AuthTypeUser)
		assert.False(t, ActorMayAccessWorkspace(c, sqlx.NewDb(db, "postgres"), uuid.New()))
	})

	t.Run("agent in the workspace", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		wsID, agentID := uuid.New(), uuid.New()
		mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
			WithArgs(agentID).
			WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))

		c, _ := bodyWSContext(`{}`, AuthTypeAgent, agentID)
		assert.True(t, ActorMayAccessWorkspace(c, sqlx.NewDb(db, "postgres"), wsID))
	})

	t.Run("workspace owner without a membership row", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		wsID, userID := uuid.New(), uuid.New()
		mock.ExpectQuery(`SELECT wm\.role FROM workspace_members wm\s+JOIN workspaces w ON w\.id = wm\.workspace_id\s+WHERE wm\.workspace_id = \$1 AND wm\.user_id = \$2 AND w\.deleted_at IS NULL`).
			WithArgs(wsID, userID).
			WillReturnRows(sqlmock.NewRows([]string{"role"}))
		mock.ExpectQuery("SELECT owner_id FROM workspaces").
			WithArgs(wsID).
			WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(userID))

		c, _ := bodyWSContext(`{}`, AuthTypeUser, userID)
		assert.True(t, ActorMayAccessWorkspace(c, sqlx.NewDb(db, "postgres"), wsID),
			"the owner-without-a-members-row fallback is missing, which locks real owners out")
	})
}

// TestRequireScopedPairParams covers the containment half of the guard: an id
// that names something no single workspace owns — :user_id — has to be shown to
// belong to the tenant the rest of the path resolved to.
func TestRequireScopedPairParams(t *testing.T) {
	pairCtx := func(userID string, wsID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodPatch, "/", http.NoBody), rec)
		c.SetPath("/api/v1/workspaces/:ws_id/members/:user_id")
		c.SetParamNames("ws_id", "user_id")
		c.SetParamValues(wsID.String(), userID)
		if wsID != uuid.Nil {
			c.Set(ContextKeyWorkspaceID, wsID)
		}
		return c, rec
	}

	t.Run("a member of the resolved workspace passes", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		wsID, userID := uuid.New(), uuid.New()
		mock.ExpectQuery("FROM workspace_members").
			WithArgs(wsID, userID).
			WillReturnRows(sqlmock.NewRows([]string{"?column?"}).AddRow(1))

		c, rec := pairCtx(userID.String(), wsID)
		reached := false
		require.NoError(t, requireScopedPairParams(sqlx.NewDb(db, "postgres"))(func(echo.Context) error {
			reached = true
			return nil
		})(c))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, reached, "the guard refused a real member of the workspace in the path")
	})

	t.Run("a user from another tenant is refused", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		wsID, foreignUser := uuid.New(), uuid.New()
		mock.ExpectQuery("FROM workspace_members").
			WithArgs(wsID, foreignUser).
			WillReturnRows(sqlmock.NewRows([]string{"?column?"}))

		c, rec := pairCtx(foreignUser.String(), wsID)
		reached := false
		require.NoError(t, requireScopedPairParams(sqlx.NewDb(db, "postgres"))(func(echo.Context) error {
			reached = true
			return nil
		})(c))

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.False(t, reached)
	})

	t.Run("a non-uuid parameter is left to the handler", func(t *testing.T) {
		// /workspaces/:ws_id/members/me is a separate registered route, so this
		// shape only reaches here as a malformed id — a 400 the handler gives,
		// not a cross-tenant reach for this guard to refuse.
		c, rec := pairCtx("me", uuid.New())
		reached := false
		require.NoError(t, requireScopedPairParams(nil)(func(echo.Context) error {
			reached = true
			return nil
		})(c))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.True(t, reached)
	})

	t.Run("no workspace in context is refused", func(t *testing.T) {
		c, rec := pairCtx(uuid.New().String(), uuid.Nil)
		require.NoError(t, requireScopedPairParams(nil)(nopHandler)(c))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("a route carrying no pair parameter passes", func(t *testing.T) {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
		c.SetPath("/api/v1/tasks/:task_id")
		c.SetParamNames("task_id")
		c.SetParamValues(uuid.New().String())

		reached := false
		require.NoError(t, requireScopedPairParams(nil)(func(echo.Context) error {
			reached = true
			return nil
		})(c))
		assert.True(t, reached)
	})
}

// TestWorkspaceParamResolvers_DegradeToRefusal: every resolver must answer "no
// workspace" rather than panicking or guessing when it cannot look one up. The
// guard reads a false here as a refusal, so these are the paths that decide
// whether a missing dependency fails open.
func TestWorkspaceParamResolvers_DegradeToRefusal(t *testing.T) {
	ctx := context.Background()

	t.Run("nil db", func(t *testing.T) {
		_, ok := queryWorkspace(ctx, nil, "SELECT 1", uuid.New())
		assert.False(t, ok)
	})

	t.Run("nil project repo", func(t *testing.T) {
		_, ok := resolveProjectWorkspace(ctx, nil, nil, uuid.New().String())
		assert.False(t, ok)
	})

	t.Run("malformed project id", func(t *testing.T) {
		_, ok := resolveProjectWorkspace(ctx, nil, stubProjectRepo{wsID: uuid.New()}, "not-a-uuid")
		assert.False(t, ok)
	})

	t.Run("project repo answers a workspace", func(t *testing.T) {
		wsID := uuid.New()
		got, ok := resolveProjectWorkspace(ctx, nil, stubProjectRepo{wsID: wsID}, uuid.New().String())
		require.True(t, ok)
		assert.Equal(t, wsID, got)
	})

	t.Run("ws_id is the workspace itself", func(t *testing.T) {
		var resolver workspaceParamResolver
		for _, r := range workspaceParamResolvers {
			if r.param == "ws_id" {
				resolver = r
				break
			}
		}
		require.NotNil(t, resolver.resolve, "the :ws_id resolver is gone")

		wsID := uuid.New()
		got, ok := resolver.resolve(ctx, nil, nil, wsID.String())
		require.True(t, ok)
		assert.Equal(t, wsID, got)

		_, ok = resolver.resolve(ctx, nil, nil, "not-a-uuid")
		assert.False(t, ok)
	})
}
