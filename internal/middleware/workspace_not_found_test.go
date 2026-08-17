package middleware

import (
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

// runScoped chains WorkspaceRLS and RequireWorkspaceMemberScoped the way the
// router registers them (WorkspaceRLS resolves, RequireWorkspaceMemberScoped
// enforces), so these tests exercise the real interaction between the two
// context flags (mismatch, not-found-resource) rather than hand-setting them.
func runScoped(db *sqlx.DB, c echo.Context) error {
	return WorkspaceRLS(db, nil)(RequireWorkspaceMemberScoped(db)(nopHandler))(c)
}

// TestRequireWorkspaceMemberScoped_TaskID_NotFound_Returns404 is the primary
// regression test for #a49500c5: a task_id that resolves to no row — because
// it is soft-deleted or never existed, the two are indistinguishable by
// design (deleted_at IS NULL in the resolver query, untouched by this fix) —
// must answer 404, not the old 403 "workspace access denied" that read as a
// permissions problem and sent three separate investigations chasing an ACL
// bug that did not exist.
//
// Mutation check: reverting the notFoundResource handling in WorkspaceRLS (or
// the resource check in RequireWorkspaceMemberScoped) makes this assertion
// fail with 403 instead of 404 — verified locally before filing this PR.
func TestRequireWorkspaceMemberScoped_TaskID_NotFound_Returns404(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// No rows: the same result whether the task was soft-deleted or the id
	// never existed — resolveTaskWorkspace's query already filters
	// deleted_at IS NULL, so this single mock covers both arms.
	mock.ExpectQuery("FROM tasks t JOIN projects p").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(uuid.New().String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())

	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.NoError(t, runScoped(sqlx.NewDb(db, "postgres"), c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Task not found")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_TaskID_NeverExistedUUID_Returns404 pins the
// second of the task's three arms explicitly: a syntactically valid UUID that
// was never assigned to any task resolves through the exact same "no rows"
// path as a soft-deleted one — same mock, same assertion, named separately so
// the acceptance criterion's three arms each have their own test.
func TestRequireWorkspaceMemberScoped_TaskID_NeverExistedUUID_Returns404(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	neverExisted := uuid.New()
	mock.ExpectQuery("FROM tasks t JOIN projects p").
		WithArgs(neverExisted).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(neverExisted.String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())

	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.NoError(t, runScoped(sqlx.NewDb(db, "postgres"), c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_TaskID_Live_Returns200 pins the third arm:
// a task that exists, in the caller's own workspace, still returns 200 — the
// fix only changes the not-found path, not the happy path.
func TestRequireWorkspaceMemberScoped_TaskID_Live_Returns200(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	wsID := uuid.New()
	taskID := uuid.New()
	agentID := uuid.New()

	mock.ExpectQuery("FROM tasks t JOIN projects p").
		WithArgs(taskID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))
	mock.ExpectQuery("set_config").
		WithArgs(wsID.String()).
		WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(wsID.String()))
	// RequireWorkspaceMember's agent branch: agent belongs to the same workspace.
	mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)

	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.NoError(t, runScoped(sqlx.NewDb(db, "postgres"), c))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_TaskID_ForeignWorkspace_Not200 is the
// mandatory negative control from #a49500c5's acceptance criteria: a task
// that DOES exist, in a workspace the caller does NOT belong to, must still
// be refused. Without this control, a 404-on-not-found fix is
// indistinguishable from "removed the membership check" — this proves the
// object-exists-but-is-not-yours path still returns non-200 (403, unchanged
// from before this fix).
func TestRequireWorkspaceMemberScoped_TaskID_ForeignWorkspace_Not200(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	foreignWS := uuid.New()
	ownWS := uuid.New()
	taskID := uuid.New()
	agentID := uuid.New()

	mock.ExpectQuery("FROM tasks t JOIN projects p").
		WithArgs(taskID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(foreignWS))
	mock.ExpectQuery("set_config").
		WithArgs(foreignWS.String()).
		WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(foreignWS.String()))
	// The agent belongs to a DIFFERENT workspace than the task.
	mock.ExpectQuery(`SELECT a\.workspace_id FROM agents a\s+JOIN workspaces w ON w\.id = a\.workspace_id\s+WHERE a\.id = \$1 AND a\.deleted_at IS NULL AND w\.deleted_at IS NULL`).
		WithArgs(agentID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(ownWS))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)

	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.NoError(t, runScoped(sqlx.NewDb(db, "postgres"), c))

	assert.NotEqual(t, http.StatusOK, rec.Code, "a task in a foreign workspace must never return 200")
	assert.Equal(t, http.StatusForbidden, rec.Code, "existing object, wrong tenant — unchanged 403 semantics")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_ArtifactID_NotFound_Returns404 pins the
// same fix on the parallel resolver named in the task's acceptance criterion
// #4 — GET/DELETE /artifacts/:artifact_id is a single-scoped-param route just
// like /tasks/:task_id, going through the same tasks/deleted_at join one hop
// further out (artifacts -> tasks -> projects).
func TestRequireWorkspaceMemberScoped_ArtifactID_NotFound_Returns404(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("FROM artifacts a").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/artifacts/:artifact_id")
	c.SetParamNames("artifact_id")
	c.SetParamValues(uuid.New().String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, uuid.New())

	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.NoError(t, runScoped(sqlx.NewDb(db, "postgres"), c))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Artifact not found")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestRequireWorkspaceMemberScoped_CompositeRoute_TaskIDNotFoundWithArtifactID_Stays403
// is the regression guard for the composite-route protection this fix must
// NOT weaken: GET /tasks/:task_id/artifacts/:artifact_id/download names two
// scoped parameters. If task_id fails to resolve while artifact_id resolves
// to a real, own-workspace artifact, the request must still be refused with
// 403 (the pre-existing mismatch behavior) — NOT 404, and NOT let through on
// the strength of the artifact_id alone. Falling through here would let a
// caller reach an artifact via a garbage/foreign task_id in the path as long
// as they separately know a real artifact_id, which is exactly the class of
// bug workspaceParamResolvers' "every, not any" doctrine exists to prevent.
func TestRequireWorkspaceMemberScoped_CompositeRoute_TaskIDNotFoundWithArtifactID_Stays403(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	ownWS := uuid.New()
	agentID := uuid.New()
	artifactID := uuid.New()

	// task_id resolves to nothing (deleted/nonexistent).
	mock.ExpectQuery("FROM tasks t JOIN projects p").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}))
	// artifact_id resolves fine, to the agent's own workspace — WorkspaceRLS
	// still sets the RLS session to it (mismatch is a downstream guard
	// decision, not something WorkspaceRLS itself skips set_config for).
	mock.ExpectQuery("FROM artifacts a").
		WithArgs(artifactID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(ownWS))
	mock.ExpectQuery("set_config").
		WithArgs(ownWS.String()).
		WillReturnRows(sqlmock.NewRows([]string{"set_config"}).AddRow(ownWS.String()))

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), httptest.NewRecorder())
	c.SetPath("/api/v1/tasks/:task_id/artifacts/:artifact_id/download")
	c.SetParamNames("task_id", "artifact_id")
	c.SetParamValues(uuid.New().String(), artifactID.String())
	c.Set(ContextKeyAuthType, AuthTypeAgent)
	c.Set(ContextKeyAgentID, agentID)

	rec := c.Response().Writer.(*httptest.ResponseRecorder)
	require.NoError(t, runScoped(sqlx.NewDb(db, "postgres"), c))

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"a composite route with one unresolvable parameter must stay 403, not become 404")
	assert.NoError(t, mock.ExpectationsWereMet())
}
