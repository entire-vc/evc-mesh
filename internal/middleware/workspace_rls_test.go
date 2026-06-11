package middleware

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Minimal SQL driver for WorkspaceRLS tests (no external dependency).
//
// We only need to exercise the set_config call path. Resolution step 3
// (GetWorkspaceID from Echo context) is used to avoid DB round-trips for
// workspace-ID resolution, so the only query that hits the mock DB is the
// set_config call itself.
// ---------------------------------------------------------------------------

var registerRLSDriversOnce sync.Once

func registerRLSDrivers() {
	registerRLSDriversOnce.Do(func() {
		sql.Register("rls-ok", &rlsDriver{failQuery: false})
		sql.Register("rls-err", &rlsDriver{failQuery: true})
	})
}

type rlsDriver struct{ failQuery bool }
type rlsConn struct{ failQuery bool }
type rlsStmt struct{ failQuery bool }
type rlsSuccessRows struct{ done bool }

func (d *rlsDriver) Open(_ string) (driver.Conn, error) {
	return &rlsConn{failQuery: d.failQuery}, nil
}

func (c *rlsConn) Prepare(_ string) (driver.Stmt, error) {
	return &rlsStmt{failQuery: c.failQuery}, nil
}
func (c *rlsConn) Close() error              { return nil }
func (c *rlsConn) Begin() (driver.Tx, error) { return nil, errors.New("tx not supported") }
func (s *rlsStmt) Close() error              { return nil }
func (s *rlsStmt) NumInput() int             { return -1 }
func (s *rlsStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return nil, errors.New("exec not supported")
}
func (s *rlsStmt) Query(_ []driver.Value) (driver.Rows, error) {
	if s.failQuery {
		return nil, errors.New("simulated set_config DB failure")
	}
	return &rlsSuccessRows{}, nil
}
func (r *rlsSuccessRows) Columns() []string { return []string{"set_config"} }
func (r *rlsSuccessRows) Close() error      { return nil }
func (r *rlsSuccessRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	dest[0] = "workspace-id-value"
	r.done = true
	return nil
}

// newRLSTestDB returns a *sqlx.DB backed by the named rls-ok or rls-err driver.
func newRLSTestDB(failQuery bool) *sqlx.DB {
	registerRLSDrivers()
	name := "rls-ok"
	if failQuery {
		name = "rls-err"
	}
	db, err := sql.Open(name, "")
	if err != nil {
		panic("newRLSTestDB: " + err.Error())
	}
	return sqlx.NewDb(db, name)
}

// setupRLSContext creates an Echo context pre-populated with workspace_id and
// agent auth type so WorkspaceRLS uses the auth-context resolution path (step 3)
// without touching the DB for resolution, and skips the workspace-role lookup.
func setupRLSContext(e *echo.Echo, wsID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(ContextKeyWorkspaceID, wsID)
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())
	return c, rec
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestWorkspaceRLS_SetConfigError_Returns500 verifies that a set_config DB
// failure causes the middleware to return 500 (fail-closed), not pass through.
func TestWorkspaceRLS_SetConfigError_Returns500(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()
	c, rec := setupRLSContext(e, wsID)

	db := newRLSTestDB(true /* failQuery */)

	mw := WorkspaceRLS(db, nil)
	reachedHandler := false
	err := mw(func(c echo.Context) error {
		reachedHandler = true
		return c.String(http.StatusOK, "ok")
	})(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "set_config failure must produce 500")
	assert.False(t, reachedHandler, "handler must not be reached when set_config fails")
}

// TestWorkspaceRLS_SetConfigSuccess_PassesThrough verifies that a successful
// set_config call lets the request continue to the handler.
func TestWorkspaceRLS_SetConfigSuccess_PassesThrough(t *testing.T) {
	e := echo.New()
	wsID := uuid.New()
	c, rec := setupRLSContext(e, wsID)

	db := newRLSTestDB(false /* failQuery */)

	mw := WorkspaceRLS(db, nil)
	err := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code, "successful set_config must pass through to handler")
}

// TestWorkspaceRLS_NoWorkspaceResolved_PassesThrough verifies that when no
// workspace ID can be resolved (no route params, no auth context), the
// middleware passes through without touching the DB.
func TestWorkspaceRLS_NoWorkspaceResolved_PassesThrough(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	// No workspace_id, no route params — resolved stays false.

	mw := WorkspaceRLS(nil /* db never called */, nil)
	err := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code, "unresolved workspace must pass through")
}
