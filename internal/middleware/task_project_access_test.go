package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// taskRoutes are the registered routes that name a task by id, read from the real
// router the same way TestEveryIdentifiedRouteIsWorkspaceScoped reads it.
func taskRoutes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range authenticatedRoutes(t) {
		if strings.Contains(r, ":task_id") || strings.HasPrefix(r, "/tasks/by-short-id/") {
			out = append(out, r)
		}
	}
	// A short list means the router is no longer being read, and every
	// assertion below would pass on nothing.
	require.Greater(t, len(out), 30, "only %d task routes discovered: %v", len(out), out)
	require.Contains(t, out, "/tasks/by-short-id/:short")
	return out
}

// taskRouteCtx builds the context RequireTaskProjectMember sees for route, with
// parameters in path order (the order the guard walks them in) and a fresh uuid
// for each id.
func taskRouteCtx(route string) (echo.Context, *httptest.ResponseRecorder, []string) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)
	c.SetPath("/api/v1" + route)
	names := pathParams(route)
	values := make([]string, len(names))
	for i := range names {
		values[i] = uuid.New().String()
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	return c, rec, names
}

// isTaskProjectParam reports whether the guard resolves a project from name.
func isTaskProjectParam(name string) bool {
	_, child := taskProjectChildParams[name]
	return name == "task_id" || name == "short" || child
}

func asUser(c echo.Context, userID uuid.UUID, role string) {
	c.Set(ContextKeyAuthType, AuthTypeUser)
	c.Set(ContextKeyUserID, userID)
	c.Set(ContextKeyWorkspaceRole, role)
}

// TestTaskRoutes_NonMemberRefused: on every task route, a plain workspace member
// who is not in the task's project is refused before the handler runs.
func TestTaskRoutes_NonMemberRefused(t *testing.T) {
	for _, route := range taskRoutes(t) {
		t.Run(route, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			projID, userID := uuid.New(), uuid.New()
			mock.ExpectQuery("SELECT project_id FROM tasks").
				WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projID))
			mock.ExpectQuery("SELECT EXISTS").
				WithArgs(projID, userID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

			c, rec, _ := taskRouteCtx(route)
			asUser(c, userID, domain.RoleMember)
			hit := false
			require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))

			assert.False(t, hit, "a non-member reached the handler")
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestTaskRoutes_MemberAndOwnerPass: the same routes still let a project member
// and a workspace owner/admin through.
func TestTaskRoutes_MemberAndOwnerPass(t *testing.T) {
	for _, route := range taskRoutes(t) {
		for _, role := range []string{domain.RoleMember, domain.RoleOwner, domain.RoleAdmin} {
			t.Run(role+" "+route, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				defer func() { _ = db.Close() }()

				projID, userID := uuid.New(), uuid.New()
				c, rec, names := taskRouteCtx(route)
				for _, name := range names {
					if !isTaskProjectParam(name) {
						continue
					}
					mock.ExpectQuery("SELECT (t\\.)?project_id FROM").
						WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projID))
					if role == domain.RoleMember {
						mock.ExpectQuery("SELECT EXISTS").
							WithArgs(projID, userID).
							WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
					}
				}
				asUser(c, userID, role)
				hit := false
				require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))

				assert.True(t, hit, "an allowed caller was refused: %d %s", rec.Code, rec.Body.String())
				assert.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

// TestTaskRoutes_Agents: an agent passes on its own project's task and is
// refused on another's — and an owner-role agent connection gets no bypass.
func TestTaskRoutes_Agents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		member bool
		want   int
	}{
		{"member", true, http.StatusOK},
		{"not a member", false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			projID, agentID := uuid.New(), uuid.New()
			mock.ExpectQuery("SELECT project_id FROM tasks").
				WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projID))
			mock.ExpectQuery("SELECT EXISTS").
				WithArgs(projID, agentID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.member))

			c, rec, _ := taskRouteCtx("/tasks/:task_id")
			c.Set(ContextKeyAuthType, AuthTypeAgent)
			c.Set(ContextKeyAgentID, agentID)
			c.Set(ContextKeyWorkspaceRole, domain.RoleOwner)
			hit := false
			require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))

			assert.Equal(t, tc.member, hit)
			assert.Equal(t, tc.want, rec.Code)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestTaskRoutes_UnresolvedTaskIs404: a task id the guard cannot place is a 404,
// never a pass-through.
func TestTaskRoutes_UnresolvedTaskIs404(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("SELECT project_id FROM tasks").
		WillReturnRows(sqlmock.NewRows([]string{"project_id"}))

	c, rec, _ := taskRouteCtx("/tasks/:task_id")
	asUser(c, uuid.New(), domain.RoleOwner)
	hit := false
	require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))
	assert.False(t, hit)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestTaskRoutes_InertElsewhere: a route naming no task is not touched (nil db:
// any query would panic).
func TestTaskRoutes_InertElsewhere(t *testing.T) {
	c, _, _ := taskRouteCtx("/projects/:proj_id/statuses")
	asUser(c, uuid.New(), domain.RoleMember)
	hit := false
	require.NoError(t, RequireTaskProjectMember(nil)(reachedHandler(&hit))(c))
	assert.True(t, hit)
}

// TestTaskProjectGuardIsRegistered: the guard covers the task routes only because
// it is mounted on the API group, after the workspace guard whose 404 and role it
// relies on. Without this a refactor that drops the line leaves every test above
// green and the routes open.
func TestTaskProjectGuardIsRegistered(t *testing.T) {
	src, err := os.ReadFile("../../cmd/api/main.go")
	require.NoError(t, err)
	body := string(src)
	ws := strings.Index(body, "api.Use(mw.RequireWorkspaceMemberScoped(db))")
	task := strings.Index(body, "api.Use(mw.RequireTaskProjectMember(db))")
	require.NotEqual(t, -1, ws, "workspace guard registration not found")
	require.NotEqual(t, -1, task, "RequireTaskProjectMember is not registered on the API group")
	assert.Greater(t, task, ws, "RequireTaskProjectMember must run after RequireWorkspaceMemberScoped")
}

// TestTaskOwnedRecordRoutes_NonMemberRefused: a route naming a record that hangs
// off a task (a decision, a comment, ...) without the task itself is refused to a
// non-member of the task's project the same way.
func TestTaskOwnedRecordRoutes_NonMemberRefused(t *testing.T) {
	for _, route := range []string{
		"/decision-requests/:decision_request_id/answer",
		"/human-gate-decisions/:decision_id/revoke",
		"/comments/:comment_id",
	} {
		t.Run(route, func(t *testing.T) {
			require.Contains(t, authenticatedRoutes(t), route, "route no longer registered; update this test")
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			projID, userID := uuid.New(), uuid.New()
			mock.ExpectQuery("SELECT t\\.project_id FROM").
				WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projID))
			mock.ExpectQuery("SELECT EXISTS").
				WithArgs(projID, userID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

			c, rec, _ := taskRouteCtx(route)
			asUser(c, userID, domain.RoleMember)
			hit := false
			require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))

			assert.False(t, hit, "a non-member reached the handler")
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// notTaskOwnedParams are the route parameters that name no task and no record of
// one, so RequireTaskProjectMember rightly passes over them. The guard skips any
// name it does not know; this list is what turns that from a silent gap into a
// decision: a new parameter fails TestEveryRouteParamIsClassified until someone
// says which side it is on.
var notTaskOwnedParams = map[string]bool{
	// workspace / project scope, guarded elsewhere
	"ws_id": true, "proj_id": true,
	// native-review ids: records of a trusted enrollment, authorized by the enrolled identity in the repository
	"authority_id": true, "receipt_id": true,
	// project-owned records reached only under /projects/:proj_id, guarded there
	"status_id": true, "atr_id": true, "thread_id": true, "attachment_id": true,
	// workspace configuration, identities and runtime
	"ext_id": true, "agent_id": true, "member_agent_id": true, "user_id": true, "binding_id": true,
	"webhook_id": true, "int_id": true, "init_id": true, "reservation_id": true,
	"controller_ref": true, "provenance_artifact_id": true, "secret_id": true, "invite_id": true,
	"sub_id": true, "runner_id": true, "pref_id": true, "oauth_grant_id": true, "key_id": true,
	"grant_id": true, "command_id": true, "receiver_id": true, "action": true, "scope": true, "id": true,
	// a row of the calling extension's own event log; the route is extension-actor
	// only and the service scopes the delivery to that extension's connection
	"delivery_id": true,
}

func TestEveryRouteParamIsClassified(t *testing.T) {
	for _, route := range authenticatedRoutes(t) {
		for _, name := range pathParams(route) {
			if isTaskProjectParam(name) || projectRecordParams[name] != "" {
				assert.False(t, notTaskOwnedParams[name], "%s is listed on both sides", name)
				continue
			}
			assert.True(t, notTaskOwnedParams[name],
				"route %s: parameter :%s is unclassified. If it names a task or a record of one, add its project resolver to taskProjectChildParams; if it names a project's record, to projectRecordParams; otherwise add it to notTaskOwnedParams", route, name)
		}
	}
}

// projectRecordRoutes are the registered routes that name a project's record by
// id, except the caller's own mention inbox.
func projectRecordRoutes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range authenticatedRoutes(t) {
		if taskProjectExemptRoutes["/api/v1"+r] {
			continue
		}
		for _, name := range pathParams(r) {
			if projectRecordParams[name] != "" {
				out = append(out, r)
				break
			}
		}
	}
	require.Greater(t, len(out), 20, "only %d project record routes discovered: %v", len(out), out)
	for _, want := range []string{
		"/documents/:doc_id", "/document-attachments/:att_id", "/document-comments/:dcom_id",
		"/custom-fields/:field_id", "/views/:view_id", "/templates/:tmpl_id/create-task",
		"/recurring/:recurring_id/trigger", "/rules/:rule_id", "/events/:event_id",
	} {
		require.Contains(t, out, want)
	}
	return out
}

// expectRecordProject queues the project lookups the guard makes on route,
// answering every one with projID (nil = a workspace record) and, for a plain
// member, the membership answer.
func expectRecordProject(mock sqlmock.Sqlmock, names []string, projID *uuid.UUID, userID uuid.UUID, member *bool) {
	for _, name := range names {
		if !isTaskProjectParam(name) && projectRecordParams[name] == "" {
			continue
		}
		rows := sqlmock.NewRows([]string{"project_id"})
		if projID == nil {
			rows.AddRow(nil)
		} else {
			rows.AddRow(*projID)
		}
		mock.ExpectQuery("SELECT (\\w\\.)?project_id FROM").WillReturnRows(rows)
		if projID != nil && member != nil {
			mock.ExpectQuery("SELECT EXISTS").
				WithArgs(*projID, userID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(*member))
			if !*member {
				return
			}
		}
	}
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestProjectRecordRoutes_Enforce: with EnvProjectRecordAccess=enforce, a plain
// workspace member outside the record's project is refused on every route, a
// member and an owner/admin pass, and a workspace record (NULL project) passes.
func TestProjectRecordRoutes_Enforce(t *testing.T) {
	t.Setenv(EnvProjectRecordAccess, "enforce")
	no, yes := false, true
	for _, route := range projectRecordRoutes(t) {
		for _, tc := range []struct {
			name      string
			role      string
			workspace bool
			member    *bool
			want      int
		}{
			{"non-member", domain.RoleMember, false, &no, http.StatusForbidden},
			{"member", domain.RoleMember, false, &yes, http.StatusOK},
			{"owner", domain.RoleOwner, false, nil, http.StatusOK},
			{"admin", domain.RoleAdmin, false, nil, http.StatusOK},
			{"workspace record", domain.RoleMember, true, nil, http.StatusOK},
		} {
			t.Run(tc.name+" "+route, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				defer func() { _ = db.Close() }()

				userID := uuid.New()
				var projID *uuid.UUID
				if !tc.workspace {
					p := uuid.New()
					projID = &p
				}
				c, rec, names := taskRouteCtx(route)
				expectRecordProject(mock, names, projID, userID, tc.member)
				asUser(c, userID, tc.role)
				hit := false
				require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))

				assert.Equal(t, tc.want == http.StatusOK, hit)
				assert.Equal(t, tc.want, rec.Code, rec.Body.String())
				assert.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

// TestProjectRecordRoutes_Agents: an agent is held to its memberships on a
// project record even with an owner-role connection.
func TestProjectRecordRoutes_Agents(t *testing.T) {
	t.Setenv(EnvProjectRecordAccess, "enforce")
	for _, member := range []bool{true, false} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		projID, agentID := uuid.New(), uuid.New()
		c, rec, names := taskRouteCtx("/documents/:doc_id")
		expectRecordProject(mock, names, &projID, agentID, &member)
		c.Set(ContextKeyAuthType, AuthTypeAgent)
		c.Set(ContextKeyAgentID, agentID)
		c.Set(ContextKeyWorkspaceRole, domain.RoleOwner)
		hit := false
		require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))
		assert.Equal(t, member, hit)
		if !member {
			assert.Equal(t, http.StatusForbidden, rec.Code)
		}
		assert.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}

// TestProjectRecordRoutes_ReportMode: by default a refusal is not applied. The
// request reaches the handler and the refusal is logged with who, what and where,
// which is what the memberships are granted from before enforce is switched on.
func TestProjectRecordRoutes_ReportMode(t *testing.T) {
	for _, mode := range []string{"", "report"} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Setenv(EnvProjectRecordAccess, mode)
			logs := captureSlog(t)
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()

			no := false
			projID, agentID := uuid.New(), uuid.New()
			c, rec, names := taskRouteCtx("/documents/:doc_id")
			expectRecordProject(mock, names, &projID, agentID, &no)
			c.Set(ContextKeyAuthType, AuthTypeAgent)
			c.Set(ContextKeyAgentID, agentID)
			hit := false
			require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))

			assert.True(t, hit, "report mode refused: %d", rec.Code)
			line := logs.String()
			assert.Contains(t, line, "project_record_access_would_deny")
			assert.Contains(t, line, "agent="+agentID.String())
			assert.Contains(t, line, "param=doc_id")
			assert.Contains(t, line, "project="+projID.String())
			assert.Contains(t, line, "path=/api/v1/documents/:doc_id")
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestBodyAccessCheck: handlers handed a task or project in the body ask the
// request. The guard puts the rule there on every route, including the ones it
// otherwise leaves alone, and without it the answer is no.
func TestBodyAccessCheck(t *testing.T) {
	bare, _, _ := taskRouteCtx("/projects/:proj_id/statuses")
	assert.False(t, MayAccessTask(bare, uuid.New()), "no check on the request must refuse")
	assert.False(t, MayAccessProject(bare, uuid.New()), "no check on the request must refuse")

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	wsID, projID, userID, taskID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mock.ExpectQuery("SELECT project_id FROM tasks").
		WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projID))
	mock.ExpectQuery("SELECT workspace_id FROM projects").WithArgs(projID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(projID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT workspace_id FROM projects").WithArgs(projID).
		WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(wsID))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(projID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	c, _, _ := taskRouteCtx("/vcs-links/ship-sha")
	asUser(c, userID, domain.RoleMember)
	c.Set(ContextKeyWorkspaceID, wsID)
	var task, project bool
	require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(func(c echo.Context) error {
		task, project = MayAccessTask(c, taskID), MayAccessProject(c, projID)
		return nil
	})(c))
	assert.False(t, task, "a task in a project the caller is not in")
	assert.True(t, project, "a project the caller is in")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestBodyAccessCheck_HeldToRequestWorkspace: an id in a body never passed the
// workspace guard. The owner/admin bypass and the extension actor skip the
// membership lookup, so without the workspace comparison they would reach a
// project of any workspace by naming it.
func TestBodyAccessCheck_HeldToRequestWorkspace(t *testing.T) {
	wsID, otherWS, projID := uuid.New(), uuid.New(), uuid.New()
	for _, tc := range []struct {
		name   string
		actor  func(echo.Context)
		projWS uuid.UUID
		reqWS  *uuid.UUID
		want   bool
	}{
		{"owner, project of another workspace", func(c echo.Context) { asUser(c, uuid.New(), domain.RoleOwner) }, otherWS, &wsID, false},
		{"admin, project of another workspace", func(c echo.Context) { asUser(c, uuid.New(), domain.RoleAdmin) }, otherWS, &wsID, false},
		{"extension, project of another workspace", func(c echo.Context) { c.Set(ContextKeyAuthType, AuthTypeExtension) }, otherWS, &wsID, false},
		{"owner, no workspace on the request", func(c echo.Context) { asUser(c, uuid.New(), domain.RoleOwner) }, wsID, nil, false},
		{"owner, project of the request workspace", func(c echo.Context) { asUser(c, uuid.New(), domain.RoleOwner) }, wsID, &wsID, true},
		{"extension, project of the request workspace", func(c echo.Context) { c.Set(ContextKeyAuthType, AuthTypeExtension) }, wsID, &wsID, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			if tc.reqWS != nil {
				mock.ExpectQuery("SELECT workspace_id FROM projects").WithArgs(projID).
					WillReturnRows(sqlmock.NewRows([]string{"workspace_id"}).AddRow(tc.projWS))
			}
			c, _, _ := taskRouteCtx("/tasks/move-to-project-batch")
			tc.actor(c)
			if tc.reqWS != nil {
				c.Set(ContextKeyWorkspaceID, *tc.reqWS)
			}
			var got bool
			require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(func(c echo.Context) error {
				got = MayAccessProject(c, projID)
				return nil
			})(c))
			assert.Equal(t, tc.want, got)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// TestProjectRecordRoutes_ReportModeSilentOnAllowed: a member's request logs
// nothing, so the report is a list of refusals only.
func TestProjectRecordRoutes_ReportModeSilentOnAllowed(t *testing.T) {
	t.Setenv(EnvProjectRecordAccess, "report")
	logs := captureSlog(t)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	yes := true
	projID, userID := uuid.New(), uuid.New()
	c, _, names := taskRouteCtx("/rules/:rule_id")
	expectRecordProject(mock, names, &projID, userID, &yes)
	asUser(c, userID, domain.RoleMember)
	hit := false
	require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))
	assert.True(t, hit)
	assert.NotContains(t, logs.String(), "would_deny")
}

// TestProjectRecordRoutes_Unresolved: a record the guard cannot find is a 404 in
// enforce mode and untouched in report mode.
func TestProjectRecordRoutes_Unresolved(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want int
	}{{"enforce", http.StatusNotFound}, {"report", http.StatusOK}} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv(EnvProjectRecordAccess, tc.mode)
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectQuery("SELECT project_id FROM event_bus_messages").
				WillReturnRows(sqlmock.NewRows([]string{"project_id"}))
			c, rec, _ := taskRouteCtx("/events/:event_id")
			asUser(c, uuid.New(), domain.RoleMember)
			hit := false
			require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))
			assert.Equal(t, tc.want == http.StatusOK, hit)
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

// TestDocumentMentionSeenIsExempt: clearing one's own document mention is not an
// access to the document (nil db: any query would panic).
func TestDocumentMentionSeenIsExempt(t *testing.T) {
	t.Setenv(EnvProjectRecordAccess, "enforce")
	c, _, _ := taskRouteCtx("/me/document-mentions/:dcom_id/seen")
	asUser(c, uuid.New(), domain.RoleMember)
	hit := false
	require.NoError(t, RequireTaskProjectMember(nil)(reachedHandler(&hit))(c))
	assert.True(t, hit)
}

// TestProjectRecordRoutes_DeletedRecordIsUnresolved: the handler answers 404 for a
// soft-deleted document, attachment, comment or recurring schedule (or one on a deleted document), so
// the guard's lookup must skip them too: a non-member would otherwise get a 403
// that confirms the id once existed. The query pattern is the assertion: a lookup
// without the deleted_at filters does not match and the test fails.
func TestProjectRecordRoutes_DeletedRecordIsUnresolved(t *testing.T) {
	t.Setenv(EnvProjectRecordAccess, "enforce")
	for _, tc := range []struct{ route, query string }{
		{"/documents/:doc_id", `FROM documents WHERE id = \$1 AND deleted_at IS NULL`},
		{"/document-attachments/:att_id", `a\.deleted_at IS NULL AND d\.deleted_at IS NULL`},
		{"/document-comments/:dcom_id", `c\.deleted_at IS NULL AND d\.deleted_at IS NULL`},
		{"/recurring/:recurring_id", `FROM recurring_schedules WHERE id = \$1 AND deleted_at IS NULL`},
	} {
		t.Run(tc.route, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectQuery(tc.query).WillReturnRows(sqlmock.NewRows([]string{"project_id"}))
			c, rec, _ := taskRouteCtx(tc.route)
			asUser(c, uuid.New(), domain.RoleMember)
			hit := false
			require.NoError(t, RequireTaskProjectMember(sqlx.NewDb(db, "sqlmock"))(reachedHandler(&hit))(c))
			assert.False(t, hit)
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
