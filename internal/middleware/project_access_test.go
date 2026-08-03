package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// setupProjectCtx builds an Echo context for a request against the given route
// pattern, mimicking what DualAuth + WorkspaceRLS produce before this middleware runs.
// routePath is the registered pattern (e.g. "/projects/:proj_id/statuses"), which is
// what c.Path() returns and what the middleware reasons about.
func setupProjectCtx(e *echo.Echo, routePath string, params map[string]string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath(routePath)

	names := make([]string, 0, len(params))
	values := make([]string, 0, len(params))
	for k, v := range params {
		names = append(names, k)
		values = append(values, v)
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	return c, rec
}

// reached records whether the guarded handler ran. A gate that passes the request
// through is only detectable by observing the handler, not the status code — a 200
// from a permissive gate and a 200 from a legitimate one are identical.
func reachedHandler(hit *bool) echo.HandlerFunc {
	return func(c echo.Context) error {
		*hit = true
		return c.String(http.StatusOK, "ok")
	}
}

// TestRequireProjectMember_NoProjIDParam_FailsClosed pins the central invariant:
// this guard must never pass a request through on a route it cannot evaluate.
//
// It previously returned next(c) when the route had no :proj_id, which made
//
//	api.GET("/tasks/:task_id/x", h, projAccess)
//
// read like "locked behind project membership" while running with no gate at all —
// there is no workspace check in this chain either. An absent gate wearing a gate's
// name is worse than an obviously ungated route, because it gets believed.
func TestRequireProjectMember_NoProjIDParam_FailsClosed(t *testing.T) {
	e := echo.New()

	hit := false
	// A task-scoped route: the pattern carries :task_id and no :proj_id at all.
	c, rec := setupProjectCtx(e, "/tasks/:task_id/statuses", map[string]string{
		"task_id": uuid.New().String(),
	})
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())

	// nil db is deliberate: reaching a query at all would itself be a failure here.
	err := RequireProjectMember(nil)(reachedHandler(&hit))(c)
	require.NoError(t, err)

	assert.False(t, hit, "the guarded handler must NOT run on a route this guard cannot evaluate")
	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"a route misconfiguration must fail closed")

	// 500 rather than 403 on purpose: the caller is not forbidden, the server is
	// misconfigured. A 403 here would send whoever debugs it looking for a membership
	// problem that does not exist.
	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Equal(t, http.StatusInternalServerError, apiErr.Code)
	assert.NotContains(t, apiErr.Message, "not a member",
		"the error must not describe this as a membership failure")
}

// TestRequireProjectMember_NoProjIDParam_FailsClosedForUsers covers the same route
// shape on the user auth path, so the fix cannot be half-applied to agents only.
func TestRequireProjectMember_NoProjIDParam_FailsClosedForUsers(t *testing.T) {
	e := echo.New()

	hit := false
	c, rec := setupProjectCtx(e, "/tasks/:task_id/statuses", map[string]string{
		"task_id": uuid.New().String(),
	})
	c.Set(ContextKeyAuthType, AuthTypeUser)
	c.Set(ContextKeyUserID, uuid.New())
	c.Set(ContextKeyWorkspaceRole, domain.RoleMember)

	err := RequireProjectMember(nil)(reachedHandler(&hit))(c)
	require.NoError(t, err)

	assert.False(t, hit, "handler must not run for users either")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestRequireProjectMember_OwnerBypassStillNeedsAProject guards against a tempting
// but wrong fix: letting owners/admins through before the :proj_id check. The bypass
// exists because owners have access to every project — not because a route without a
// project is safe. Ordering it first would restore the hole for exactly the callers
// with the most authority.
func TestRequireProjectMember_OwnerBypassStillNeedsAProject(t *testing.T) {
	e := echo.New()

	hit := false
	c, rec := setupProjectCtx(e, "/tasks/:task_id/statuses", map[string]string{
		"task_id": uuid.New().String(),
	})
	c.Set(ContextKeyAuthType, AuthTypeUser)
	c.Set(ContextKeyUserID, uuid.New())
	c.Set(ContextKeyWorkspaceRole, domain.RoleOwner)

	err := RequireProjectMember(nil)(reachedHandler(&hit))(c)
	require.NoError(t, err)

	assert.False(t, hit, "an owner must not be waved through a route with no project to own")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// --- Positive controls: the guard still does its real job on project routes. ---
//
// Without these, the test above is satisfied by a middleware that rejects everything,
// which would "pass" while breaking all 50 project-scoped routes.

// TestRequireProjectMember_AgentMember_PassesThrough is the positive control for agents.
func TestRequireProjectMember_AgentMember_PassesThrough(t *testing.T) {
	e := echo.New()
	projID, agentID := uuid.New(), uuid.New()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(projID, agentID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	hit := false
	c, rec := setupProjectCtx(e, "/projects/:proj_id/statuses", map[string]string{
		"proj_id": projID.String(),
	})
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)

	err = RequireProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c)
	require.NoError(t, err)
	assert.True(t, hit, "a project member must still reach the handler")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireProjectMember_AgentNotMember_Forbidden is the negative control on the
// real path: a non-member is still refused, and still told it is a membership problem.
func TestRequireProjectMember_AgentNotMember_Forbidden(t *testing.T) {
	e := echo.New()
	projID, agentID := uuid.New(), uuid.New()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(projID, agentID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	hit := false
	c, rec := setupProjectCtx(e, "/projects/:proj_id/statuses", map[string]string{
		"proj_id": projID.String(),
	})
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)

	err = RequireProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	var apiErr apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &apiErr))
	assert.Contains(t, apiErr.Message, "not a member of this project",
		"a genuine membership refusal must still say so — that message is correct here")
}

// TestRequireProjectMember_OwnerBypassesOnProjectRoute pins the bypass that does exist:
// on a route that HAS a project, an owner passes without a membership row.
func TestRequireProjectMember_OwnerBypassesOnProjectRoute(t *testing.T) {
	e := echo.New()

	hit := false
	c, rec := setupProjectCtx(e, "/projects/:proj_id/statuses", map[string]string{
		"proj_id": uuid.New().String(),
	})
	c.Set(ContextKeyAuthType, AuthTypeUser)
	c.Set(ContextKeyUserID, uuid.New())
	c.Set(ContextKeyWorkspaceRole, domain.RoleOwner)

	// nil db: an owner must be resolved without a query.
	err := RequireProjectMember(nil)(reachedHandler(&hit))(c)
	require.NoError(t, err)
	assert.True(t, hit, "workspace owners still bypass project membership")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRequireProjectMember_InvalidProjID keeps the existing 400 behaviour for a
// malformed project id, which is a caller error and distinct from the two cases above.
func TestRequireProjectMember_InvalidProjID(t *testing.T) {
	e := echo.New()

	hit := false
	c, rec := setupProjectCtx(e, "/projects/:proj_id/statuses", map[string]string{
		"proj_id": "not-a-uuid",
	})
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())

	err := RequireProjectMember(nil)(reachedHandler(&hit))(c)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
