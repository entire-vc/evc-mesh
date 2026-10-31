package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// Real Postgres, skipped when none is reachable (same convention as the other
// *_db_test.go files). Authentication is replaced by a one-line middleware that
// stamps the context exactly as mw.DualAuth does (user_id + auth_type), because
// the thing under test is the handler's own rules, not token parsing.

func uiStateDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type uiStateFixture struct {
	db        *sqlx.DB
	e         *echo.Echo
	member    uuid.UUID // project member
	other     uuid.UUID // member of the project too, to prove rows are per user
	stranger  uuid.UUID // not a member of anything
	admin     uuid.UUID // workspace admin, not a project member
	projectID uuid.UUID
}

func newUIStateFixture(t *testing.T) *uiStateFixture {
	t.Helper()
	db := uiStateDB(t)
	ctx := context.Background()
	sfx := uuid.New().String()[:8]

	mkUser := func(name string) uuid.UUID {
		u := &domain.User{
			ID: uuid.New(), Email: "uis-" + name + "-" + sfx + "@example.com", PasswordHash: "x",
			Name: name, Username: "uis-" + name + "-" + sfx, IsActive: true,
		}
		require.NoError(t, postgres.NewUserRepo(db).Create(ctx, u))
		return u.ID
	}
	member, other, stranger, admin := mkUser("member"), mkUser("other"), mkUser("stranger"), mkUser("admin")

	ws := &domain.Workspace{ID: uuid.New(), Name: "uis-ws", Slug: "uis-ws-" + sfx, OwnerID: member}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	require.NoError(t, postgres.NewWorkspaceMemberRepo(db).Create(ctx, &domain.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: ws.ID, UserID: admin, Role: domain.RoleAdmin,
	}))
	proj := &domain.Project{
		ID: uuid.New(), WorkspaceID: ws.ID, Name: "uis-proj", Slug: "uis-proj-" + sfx,
		DefaultAssigneeType: domain.DefaultAssigneeNone,
	}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, proj))
	for _, uid := range []uuid.UUID{member, other} {
		require.NoError(t, postgres.NewWorkspaceMemberRepo(db).Create(ctx, &domain.WorkspaceMember{
			ID: uuid.New(), WorkspaceID: ws.ID, UserID: uid, Role: domain.RoleMember,
		}))
		_, err := db.ExecContext(ctx,
			`INSERT INTO project_members (id, project_id, user_id, role, workspace_id) VALUES ($1,$2,$3,'member',$4)`,
			uuid.New(), proj.ID, uid, ws.ID)
		require.NoError(t, err)
	}

	h := NewUserUIStateHandler(db)
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	// X-Test-User = session user; X-Test-Agent = an agent key request (no user).
	stamp := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if v := c.Request().Header.Get("X-Test-User"); v != "" {
				c.Set(mw.ContextKeyUserID, uuid.MustParse(v))
				c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
			}
			// An agent principal that also carries a user id (a connector acting
			// for its owner): the auth type, not the id, must decide.
			if c.Request().Header.Get("X-Test-Agent") != "" {
				c.Set(mw.ContextKeyAuthType, mw.AuthTypeAgent)
			}
			return next(c)
		}
	}
	g := e.Group("/api/v1", stamp)
	g.GET("/me/ui-state/:scope", h.Get)
	g.PUT("/me/ui-state/:scope", h.Put)

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM user_ui_state WHERE user_id = ANY($1)`, "{"+strings.Join([]string{member.String(), other.String(), stranger.String(), admin.String()}, ",")+"}")
	})
	return &uiStateFixture{db: db, e: e, member: member, other: other, stranger: stranger, admin: admin, projectID: proj.ID}
}

func (f *uiStateFixture) do(method, scope, user, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/me/ui-state/"+url.PathEscape(scope), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	return rec
}

func (f *uiStateFixture) scope() string { return "docs.tree:" + f.projectID.String() }

func TestUserUIState_PutThenGet_RoundTrip(t *testing.T) {
	f := newUIStateFixture(t)
	body := `{"state":{"expanded":["a","b"]}}`

	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.scope(), f.member.String(), body).Code)
	rec := f.do(http.MethodGet, f.scope(), f.member.String(), "")
	require.Equal(t, http.StatusOK, rec.Code)

	var got struct {
		State     map[string][]string `json:"state"`
		UpdatedAt string              `json:"updated_at"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{"a", "b"}, got.State["expanded"])
	require.NotEmpty(t, got.UpdatedAt)

	// Last write wins.
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.scope(), f.member.String(), `{"state":{"expanded":["z"]}}`).Code)
	rec = f.do(http.MethodGet, f.scope(), f.member.String(), "")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, []string{"z"}, got.State["expanded"])
}

func TestUserUIState_GetBeforeAnyWrite_404(t *testing.T) {
	f := newUIStateFixture(t)
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, f.scope(), f.member.String(), "").Code)
}

func TestUserUIState_RowsArePerUser(t *testing.T) {
	f := newUIStateFixture(t)
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.scope(), f.member.String(), `{"state":{"expanded":["mine"]}}`).Code)

	// Another member of the same project sees nothing of it...
	require.Equal(t, http.StatusNotFound, f.do(http.MethodGet, f.scope(), f.other.String(), "").Code)
	// ...and their own write does not touch the first user's row.
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.scope(), f.other.String(), `{"state":{"expanded":["theirs"]}}`).Code)
	rec := f.do(http.MethodGet, f.scope(), f.member.String(), "")
	require.Contains(t, rec.Body.String(), "mine")
	require.NotContains(t, rec.Body.String(), "theirs")
}

func TestUserUIState_NonMember_403(t *testing.T) {
	f := newUIStateFixture(t)
	require.Equal(t, http.StatusForbidden, f.do(http.MethodGet, f.scope(), f.stranger.String(), "").Code)
	require.Equal(t, http.StatusForbidden, f.do(http.MethodPut, f.scope(), f.stranger.String(), `{"state":{}}`).Code)
	// Unknown project id is indistinguishable from a foreign one.
	require.Equal(t, http.StatusForbidden, f.do(http.MethodGet, "docs.tree:"+uuid.NewString(), f.member.String(), "").Code)
	// Nothing was written for the stranger.
	var n int
	require.NoError(t, f.db.Get(&n, `SELECT count(*) FROM user_ui_state WHERE user_id = $1`, f.stranger))
	require.Zero(t, n)
}

func TestUserUIState_WorkspaceAdminWithoutProjectRow_Allowed(t *testing.T) {
	f := newUIStateFixture(t)
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.scope(), f.admin.String(), `{"state":{"expanded":[]}}`).Code)
}

func TestUserUIState_BodyOverLimit_413(t *testing.T) {
	f := newUIStateFixture(t)
	big := `{"state":{"expanded":["` + strings.Repeat("x", 70*1024) + `"]}}`
	require.Equal(t, http.StatusRequestEntityTooLarge, f.do(http.MethodPut, f.scope(), f.member.String(), big).Code)
	// The cap is on the whole request body, envelope included: a body one byte
	// over 64 KB is refused, exactly 64 KB is accepted.
	envelope := `{"state":{"expanded":[""]}}`
	pad := func(n int) string {
		return `{"state":{"expanded":["` + strings.Repeat("x", n-len(envelope)) + `"]}}`
	}
	require.Equal(t, http.StatusRequestEntityTooLarge, f.do(http.MethodPut, f.scope(), f.member.String(), pad(64*1024+1)).Code)
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.scope(), f.member.String(), pad(64*1024)).Code)
	// Just under the limit is accepted (positive control for the cap).
	ok := `{"state":{"expanded":["` + strings.Repeat("x", 60*1024) + `"]}}`
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, f.scope(), f.member.String(), ok).Code)
}

func TestUserUIState_AgentKey_Rejected(t *testing.T) {
	f := newUIStateFixture(t)
	// Even with a valid member's id in the request an agent principal is refused.
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		rec := f.do(m, f.scope(), f.member.String(), `{"state":{}}`, "X-Test-Agent", "1")
		require.Equal(t, http.StatusUnauthorized, rec.Code, m)
	}
	// And an unauthenticated call too.
	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, f.scope(), "", "").Code)
}

func TestUserUIState_ScopeAndBodyValidation(t *testing.T) {
	f := newUIStateFixture(t)
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodGet, "board.filters:"+f.projectID.String(), f.member.String(), "").Code)
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodGet, "docs.tree:not-a-uuid", f.member.String(), "").Code)
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPut, f.scope(), f.member.String(), `{"state":[1,2]}`).Code)
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPut, f.scope(), f.member.String(), `{}`).Code)
	require.Equal(t, http.StatusBadRequest, f.do(http.MethodPut, f.scope(), f.member.String(), `not json`).Code)
}

func TestUserUIState_Appearance_PerUserAndHumanOnly(t *testing.T) {
	f := newUIStateFixture(t)
	// Needs no project: a user who belongs to none can still keep a colour mode.
	unset := f.do(http.MethodGet, "appearance", f.stranger.String(), "")
	require.Equal(t, http.StatusOK, unset.Code)
	require.JSONEq(t, `{"state":{}}`, unset.Body.String())
	require.Equal(t, http.StatusOK, f.do(http.MethodPut, "appearance", f.stranger.String(), `{"state":{"mode":"dark"}}`).Code)
	require.Contains(t, f.do(http.MethodGet, "appearance", f.stranger.String(), "").Body.String(), `"dark"`)
	// Another user's row is untouched, and an agent key never reaches it.
	other := f.do(http.MethodGet, "appearance", f.member.String(), "")
	require.Equal(t, http.StatusOK, other.Code)
	require.JSONEq(t, `{"state":{}}`, other.Body.String())
	require.NotEqual(t, http.StatusOK, f.do(http.MethodGet, "appearance", f.stranger.String(), "", "X-Test-Agent", "1").Code)
}

func TestUserUIState_Appearance_DefaultIsReadOnlyAndSavedStateWins(t *testing.T) {
	f := newUIStateFixture(t)
	for range 3 {
		rec := f.do(http.MethodGet, "appearance", f.member.String(), "")
		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, `{"state":{}}`, rec.Body.String())
	}
	var rows int
	require.NoError(t, f.db.Get(&rows, `SELECT count(*) FROM user_ui_state WHERE user_id = $1 AND scope = 'appearance'`, f.member))
	require.Zero(t, rows, "GET must not create a default that overwrites a later local choice")

	saved := f.do(http.MethodPut, "appearance", f.member.String(), `{"state":{"mode":"dark","density":"compact"}}`)
	require.Equal(t, http.StatusOK, saved.Code)
	for range 3 {
		rec := f.do(http.MethodGet, "appearance", f.member.String(), "")
		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, saved.Body.String(), rec.Body.String())
	}
	// The new default is limited to missing rows, never authentication errors.
	require.Equal(t, http.StatusUnauthorized, f.do(http.MethodGet, "appearance", "", "").Code)
}

func TestUserUIState_Appearance_StorageFailureIsNotDefault(t *testing.T) {
	// A real, closed pool returns a storage error without borrowing or closing
	// the database fixture used by the persistence checks.
	db, err := sqlx.Open("postgres", "")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/me/ui-state/appearance", http.NoBody), rec)
	c.SetParamNames("scope")
	c.SetParamValues("appearance")
	c.Set(mw.ContextKeyUserID, uuid.New())
	c.Set(mw.ContextKeyAuthType, mw.AuthTypeUser)
	err = NewUserUIStateHandler(db).Get(c)
	require.Error(t, err)
	e.HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
