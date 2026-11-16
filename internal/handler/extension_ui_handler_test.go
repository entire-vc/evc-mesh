package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/dashboards"
	"github.com/entire-vc/evc-mesh/internal/extensions/uictx"
)

type uiProvider struct {
	extensions.BaseProvider
	res extensions.Resolved
}

func (p uiProvider) Validate(extensions.Level, map[string]any) error { return nil }
func (p uiProvider) Resolve(context.Context, uuid.UUID) (extensions.Resolved, error) {
	return p.res, nil
}

type fakeSigner struct{ got uictx.Claims }

func (f *fakeSigner) Issue(_ context.Context, c uictx.Claims) (string, time.Time, error) {
	f.got = c
	return "signed.jwt.token", time.Unix(1_800_000_060, 0), nil
}
func (f *fakeSigner) JWKS(context.Context) ([]byte, error) { return []byte(`{"keys":[]}`), nil }

type fakeMembers map[uuid.UUID]string

func (m fakeMembers) GetRole(_ context.Context, _, u uuid.UUID) (string, error) {
	if r, ok := m[u]; ok {
		return r, nil
	}
	return "", errors.New("not a member")
}

type fakeUsers struct{ u *domain.User }

func (f fakeUsers) GetByID(context.Context, uuid.UUID) (*domain.User, error) { return f.u, nil }

type fakeTasks map[uuid.UUID]*domain.Task

func (f fakeTasks) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	if t, ok := f[id]; ok {
		return t, nil
	}
	return nil, errors.New("not found")
}

type fakeProjects map[uuid.UUID]*domain.Project

func (f fakeProjects) GetByID(_ context.Context, id uuid.UUID) (*domain.Project, error) {
	if p, ok := f[id]; ok {
		return p, nil
	}
	return nil, errors.New("not found")
}

// fakeProjectMembers holds project_members rows as project -> users.
type fakeProjectMembers map[uuid.UUID][]uuid.UUID

func (f fakeProjectMembers) ExistsMember(_ context.Context, projectID uuid.UUID, userID, _ *uuid.UUID) (bool, error) {
	for _, u := range f[projectID] {
		if userID != nil && u == *userID {
			return true, nil
		}
	}
	return false, nil
}

type fakeBindings map[uuid.UUID]bool

func (f fakeBindings) GetBinding(_ context.Context, projectID uuid.UUID, _ string) (*domain.ProjectIntegration, error) {
	if on, ok := f[projectID]; ok {
		return &domain.ProjectIntegration{ProjectID: projectID, Enabled: on}, nil
	}
	return nil, errors.New("not found")
}

// fakeDashboard is one dashboard: its workspace, who reads it, and its saved instances.
type fakeDashboard struct {
	ws      uuid.UUID
	readers map[uuid.UUID]bool
	inst    map[uuid.UUID]dashboards.WidgetInstance
}

// fakeDashboards answers like dashboards.Service.WidgetInstance: anything the user may not read is a 404.
type fakeDashboards map[uuid.UUID]fakeDashboard

func (f fakeDashboards) WidgetInstance(_ context.Context, ws, user, dash, inst uuid.UUID) (*dashboards.WidgetInstance, error) {
	notFound := &dashboards.Error{Status: http.StatusNotFound, Code: "not_found", Message: "Dashboard not found"}
	d, ok := f[dash]
	if !ok || d.ws != ws || !d.readers[user] {
		return nil, notFound
	}
	i, ok := d.inst[inst]
	if !ok {
		return nil, notFound
	}
	return &i, nil
}

type uiFixture struct {
	h              *ExtensionUIHandler
	signer         *fakeSigner
	user, ws       uuid.UUID
	ownTask, other uuid.UUID
	// member is a plain workspace member who is in the task's project;
	// stranger is a plain workspace member who is not.
	member, stranger uuid.UUID
	ownProj          uuid.UUID
	// dash is a shared dashboard of the workspace with a workspace widget (inst), a project
	// widget set to ownProj (projInst) and one set to another project (otherProjInst);
	// otherDash, foreignDash and elsewhereDash are another dashboard of the workspace,
	// a personal dashboard the user does not read, and a dashboard of another workspace.
	dash, inst, projInst, otherProjInst              uuid.UUID
	otherDash, otherInst, foreignDash, elsewhereDash uuid.UUID
}

func newUIFixture(t *testing.T, enabled bool, domains ...string) *uiFixture {
	t.Helper()
	f := &uiFixture{signer: &fakeSigner{}, user: uuid.New(), ws: uuid.New(), ownTask: uuid.New(), other: uuid.New(),
		member: uuid.New(), stranger: uuid.New()}
	man := extensions.Manifest{
		ID: "hello", Runtime: "external",
		Capabilities: extensions.Capabilities{Network: &extensions.Network{AllowedDomains: domains}, UI: []string{"sidebar.item", "project.view", "dashboard.widget"}},
		External: &extensions.External{
			BaseURLSetting: "hello_url",
			UI: map[string]extensions.ExternalUI{
				"sidebar.item": {Path: "/mesh/ui/hello", Title: "Hello"},
				"project.view": {Path: "/mesh/ui/project", Title: "Hello"},
			},
		},
		Widgets: []extensions.Widget{
			{ID: "status", Title: "Hello widget", Scope: "workspace", Render: extensions.WidgetRender{Frame: &extensions.WidgetFrame{Path: "/mesh/ui/widget"}}},
			{ID: "proj", Title: "Hello project widget", Scope: "project", Render: extensions.WidgetRender{Frame: &extensions.WidgetFrame{Path: "/mesh/ui/widget"}}},
		},
	}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(uiProvider{
		BaseProvider: extensions.BaseProvider{Man: man},
		res:          extensions.Resolved{ExtensionID: "hello", Enabled: enabled, Settings: map[string]any{"hello_url": "https://hello.example.com"}},
	}))
	ownProj, otherProj := uuid.New(), uuid.New()
	f.ownProj = ownProj
	f.h = NewExtensionUIHandler(reg, f.signer, fakeMembers{f.user: "admin", f.member: "member", f.stranger: "member"},
		fakeUsers{&domain.User{ID: f.user, Email: "me@example.com"}},
		fakeTasks{f.ownTask: {ID: f.ownTask, ProjectID: ownProj}, f.other: {ID: f.other, ProjectID: otherProj}},
		fakeProjects{ownProj: {ID: ownProj, WorkspaceID: f.ws}, otherProj: {ID: otherProj, WorkspaceID: uuid.New()}},
		fakeProjectMembers{ownProj: {f.member}})
	f.dash, f.inst, f.projInst, f.otherProjInst = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f.otherDash, f.otherInst, f.foreignDash, f.elsewhereDash = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	all := map[uuid.UUID]bool{f.user: true, f.member: true, f.stranger: true}
	f.h.WithDashboards(fakeDashboards{
		f.dash: {ws: f.ws, readers: all, inst: map[uuid.UUID]dashboards.WidgetInstance{
			f.inst:          {Type: "hello.status", Settings: json.RawMessage(`{}`)},
			f.projInst:      {Type: "hello.proj", Settings: json.RawMessage(`{"project_id":"` + ownProj.String() + `"}`)},
			f.otherProjInst: {Type: "hello.proj", Settings: json.RawMessage(`{"project_id":"` + otherProj.String() + `"}`)},
		}},
		f.otherDash:     {ws: f.ws, readers: all, inst: map[uuid.UUID]dashboards.WidgetInstance{f.otherInst: {Type: "hello.status"}}},
		f.foreignDash:   {ws: f.ws, readers: map[uuid.UUID]bool{f.stranger: true}, inst: map[uuid.UUID]dashboards.WidgetInstance{f.inst: {Type: "hello.status"}}},
		f.elsewhereDash: {ws: uuid.New(), readers: all, inst: map[uuid.UUID]dashboards.WidgetInstance{f.inst: {Type: "hello.status"}}},
	})
	return f
}

func (f *uiFixture) call(t *testing.T, user uuid.UUID, body string) (rec *httptest.ResponseRecorder, out map[string]any) {
	t.Helper()
	return f.callOn(t, user, body, "")
}

// callOn is call with the request's Host header set (the host the Mesh UI was served from).
func (f *uiFixture) callOn(t *testing.T, user uuid.UUID, body, host string) (rec *httptest.ResponseRecorder, out map[string]any) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/ext/hello/ui-context", strings.NewReader(body))
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ext_id")
	c.SetParamValues("hello")
	if user != uuid.Nil {
		c.Set("user_id", user)
	}
	require.NoError(t, f.h.UIContext(c))
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestUIContext_Issues(t *testing.T) {
	f := newUIFixture(t, true, "hello.example.com")
	rec, out := f.call(t, f.user, `{"workspace_id":"`+f.ws.String()+`","slot":"sidebar.item","task_id":"`+f.ownTask.String()+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "signed.jwt.token", out["token"])
	assert.Equal(t, "https://hello.example.com/mesh/ui/hello", out["frame_url"])
	assert.Equal(t, "https://hello.example.com", out["origin"])
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "me@example.com", f.signer.got.Email)
	assert.Equal(t, "hello", f.signer.got.Audience)
	assert.Equal(t, []string{"admin"}, f.signer.got.Roles)
	require.NotNil(t, f.signer.got.TaskID)
	require.NotNil(t, f.signer.got.ProjectID, "project_id comes from the task, not from the caller")
}

func TestUIContext_RedControls(t *testing.T) {
	f := newUIFixture(t, true, "hello.example.com")
	ws := `"workspace_id":"` + f.ws.String() + `"`

	rec, _ := f.call(t, uuid.New(), `{`+ws+`,"slot":"sidebar.item"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, "not a workspace member")
	assert.Empty(t, f.signer.got.Email, "nothing is signed for a non-member")

	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"sidebar.item","task_id":"`+f.other.String()+`"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "a task of another workspace")

	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"task.badge"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "only iframe slots")

	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"task.panel"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "slot without a declared screen")

	rec, _ = f.call(t, uuid.Nil, `{`+ws+`,"slot":"sidebar.item"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, "no user in the request")

	off := newUIFixture(t, false, "hello.example.com")
	rec, _ = off.call(t, off.user, `{"workspace_id":"`+off.ws.String()+`","slot":"sidebar.item"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "extension disabled in the workspace")

	bad := newUIFixture(t, true, "somewhere-else.example")
	rec, _ = bad.call(t, bad.user, `{"workspace_id":"`+bad.ws.String()+`","slot":"sidebar.item"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "service host outside allowed_domains")
}

func TestJWKS_Public(t *testing.T) {
	f := newUIFixture(t, true)
	e := echo.New()
	rec := httptest.NewRecorder()
	require.NoError(t, f.h.JWKS(e.NewContext(httptest.NewRequest(http.MethodGet, "/", http.NoBody), rec)))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"keys":[]}`, rec.Body.String())
}

type errSigner struct{ err error }

func (e errSigner) Issue(context.Context, uictx.Claims) (string, time.Time, error) {
	return "", time.Time{}, e.err
}
func (e errSigner) JWKS(context.Context) ([]byte, error) { return nil, e.err }

func TestUIContext_ErrorBranches(t *testing.T) {
	body := func(f *uiFixture, extra string) string {
		return `{"workspace_id":"` + f.ws.String() + `","slot":"sidebar.item"` + extra + `}`
	}
	t.Run("no signed-in user", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		rec, _ := f.call(t, uuid.Nil, body(f, ""))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("malformed body", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		rec, _ := f.call(t, f.user, `{"workspace_id":`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("slot without a frame", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		rec, _ := f.call(t, f.user, `{"workspace_id":"`+f.ws.String()+`","slot":"board.card"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("slot the manifest does not declare", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		rec, _ := f.call(t, f.user, `{"workspace_id":"`+f.ws.String()+`","slot":"task.panel"}`)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("host outside allowed_domains", func(t *testing.T) {
		f := newUIFixture(t, true, "other.example")
		rec, _ := f.call(t, f.user, body(f, ""))
		assert.Equal(t, http.StatusConflict, rec.Code)
	})
	t.Run("unknown task", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		rec, _ := f.call(t, f.user, body(f, `,"task_id":"`+uuid.NewString()+`"`))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("project of another workspace", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		rec, _ := f.call(t, f.user, body(f, `,"project_id":"`+uuid.NewString()+`"`))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
	t.Run("own project passes through", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		tk, err := f.h.tasks.GetByID(context.Background(), f.ownTask)
		require.NoError(t, err)
		rec, _ := f.call(t, f.user, body(f, `,"project_id":"`+tk.ProjectID.String()+`"`))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, f.signer.got.ProjectID)
		assert.Equal(t, tk.ProjectID, *f.signer.got.ProjectID)
		assert.Nil(t, f.signer.got.TaskID)
	})
	t.Run("user vanished", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		f.h.users = fakeUsers{nil}
		rec, _ := f.call(t, f.user, body(f, ""))
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("signing key unavailable is 503", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		f.h.signer = errSigner{uictx.ErrKeyUnavailable}
		rec, _ := f.call(t, f.user, body(f, ""))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.NotContains(t, rec.Body.String(), "token")
	})
	t.Run("other signer failure is a server error", func(t *testing.T) {
		f := newUIFixture(t, true, "hello.example.com")
		f.h.signer = errSigner{errors.New("db down")}
		rec, _ := f.call(t, f.user, body(f, ""))
		assert.GreaterOrEqual(t, rec.Code, http.StatusInternalServerError)
	})
}

func TestJWKS_ErrorBranches(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code int
	}{
		"unavailable": {uictx.ErrKeyUnavailable, http.StatusServiceUnavailable},
		"store":       {errors.New("db down"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			f := newUIFixture(t, true, "hello.example.com")
			f.h.signer = errSigner{tc.err}
			req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", http.NoBody)
			rec := httptest.NewRecorder()
			require.NoError(t, f.h.JWKS(echo.New().NewContext(req, rec)))
			assert.Equal(t, tc.code, rec.Code)
			assert.Empty(t, rec.Header().Get("Cache-Control"), "an error must not be cacheable")
		})
	}
}

// An allow-scripts + allow-same-origin frame served from the Mesh origin could reach into the
// parent page and drop its own sandbox, so the service may never live on the host Mesh runs on.
func TestUIContext_ServiceOnMeshOriginRefused(t *testing.T) {
	body := func(f *uiFixture) string {
		return `{"workspace_id":"` + f.ws.String() + `","slot":"sidebar.item"}`
	}

	f := newUIFixture(t, true, "hello.example.com")
	rec, _ := f.callOn(t, f.user, body(f), "hello.example.com:8443")
	assert.Equal(t, http.StatusConflict, rec.Code, "the request itself came in on the service host")
	assert.Empty(t, f.signer.got.Email, "nothing is signed for a same-origin frame")

	f = newUIFixture(t, true, "hello.example.com")
	rec, _ = f.callOn(t, f.user, body(f), "mesh.example.com")
	assert.Equal(t, http.StatusOK, rec.Code, "positive control: a different Mesh host is fine")

	f = newUIFixture(t, true, "hello.example.com")
	f.h.WithSelfOrigins("https://HELLO.example.com", "")
	rec, _ = f.callOn(t, f.user, body(f), "mesh.example.com")
	assert.Equal(t, http.StatusConflict, rec.Code, "the configured public Mesh URL is the service host")
}

// A workspace member who is not in the project must not be handed a token that
// names the project or one of its tasks: the extension service acts on that
// token with the user's identity. Owner/admin and project members keep it, and a
// token naming no project is still a workspace-member matter.
func TestUIContext_ProjectMembership(t *testing.T) {
	f := newUIFixture(t, true, "hello.example.com")
	ws := `"workspace_id":"` + f.ws.String() + `","slot":"sidebar.item"`
	byTask := `{` + ws + `,"task_id":"` + f.ownTask.String() + `"}`
	byProject := `{` + ws + `,"project_id":"` + f.ownProj.String() + `"}`

	for _, tc := range []struct {
		name string
		user uuid.UUID
		body string
		want int
	}{
		{"stranger by task", f.stranger, byTask, http.StatusNotFound},
		{"stranger by project", f.stranger, byProject, http.StatusNotFound},
		{"stranger without project", f.stranger, `{` + ws + `}`, http.StatusOK},
		{"member by task", f.member, byTask, http.StatusOK},
		{"member by project", f.member, byProject, http.StatusOK},
		{"admin by task", f.user, byTask, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.signer.got = uictx.Claims{}
			rec, out := f.call(t, tc.user, tc.body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.want != http.StatusOK {
				assert.Nil(t, out["token"], "a refused request carried a token")
				assert.Equal(t, uuid.Nil, f.signer.got.UserID, "a token was signed for a refused request")
			}
		})
	}
}

func TestUIContext_ProjectView(t *testing.T) {
	f := newUIFixture(t, true, "hello.example.com")
	unbound := uuid.New()
	f.h.projects.(fakeProjects)[unbound] = &domain.Project{ID: unbound, WorkspaceID: f.ws}
	f.h.WithProjectBindings(fakeBindings{f.ownProj: true})
	ws := `"workspace_id":"` + f.ws.String() + `"`

	rec, out := f.call(t, f.user, `{`+ws+`,"slot":"project.view","project_id":"`+f.ownProj.String()+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "https://hello.example.com/mesh/ui/project", out["frame_url"])
	require.NotNil(t, f.signer.got.ProjectID, "the project tab token names the project")
	assert.Equal(t, f.ownProj, *f.signer.got.ProjectID)
	assert.Nil(t, f.signer.got.TaskID)

	f.signer.got = uictx.Claims{}
	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"project.view"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "project.view without a project")
	assert.Nil(t, f.signer.got.ProjectID, "nothing is signed without a project")

	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"project.view","task_id":"`+f.ownTask.String()+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "project.view does not take its project from a task")
	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"project.view","project_id":"`+f.ownProj.String()+`","task_id":"`+f.ownTask.String()+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "project.view carries no task")
	assert.Nil(t, f.signer.got.ProjectID, "nothing is signed for a task-shaped project.view")

	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"project.view","project_id":"`+unbound.String()+`"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "extension not enabled in this project")
	assert.Nil(t, f.signer.got.ProjectID, "nothing is signed for an unbound project")

	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"project.view","project_id":"`+uuid.NewString()+`"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "unknown project")

	// The per-project sidebar screen stands in for project.view, so it takes the same gate and the same project.
	f.signer.got = uictx.Claims{}
	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"sidebar.item","project_id":"`+unbound.String()+`"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "red control: no binding, no sidebar fallback token")
	assert.Nil(t, f.signer.got.ProjectID, "nothing is signed for an unbound project")
	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"sidebar.item","project_id":"`+f.ownProj.String()+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, f.signer.got.ProjectID)
	assert.Equal(t, f.ownProj, *f.signer.got.ProjectID)
	// A task names its project too: the same gate, so a task cannot be used to dodge the binding.
	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"sidebar.item","task_id":"`+f.ownTask.String()+`"}`)
	assert.Equal(t, http.StatusOK, rec.Code, "task of a bound project")
	f.h.WithProjectBindings(fakeBindings{})
	f.signer.got = uictx.Claims{}
	rec, _ = f.call(t, f.user, `{`+ws+`,"slot":"sidebar.item","task_id":"`+f.ownTask.String()+`"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "red control: task of an unbound project")
	assert.Nil(t, f.signer.got.ProjectID, "nothing is signed for an unbound project")
}

func TestUIContext_DashboardWidget(t *testing.T) {
	f := newUIFixture(t, true, "hello.example.com")
	ws := `"workspace_id":"` + f.ws.String() + `","slot":"dashboard.widget"`
	dash, inst := f.dash, f.inst
	ids := `"dashboard_id":"` + dash.String() + `","instance_id":"` + inst.String() + `","widget_id":"status"`

	// Workspace-scoped widget: the token names the dashboard and the instance, no project.
	rec, out := f.call(t, f.user, `{`+ws+`,`+ids+`}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "https://hello.example.com/mesh/ui/widget", out["frame_url"])
	require.NotNil(t, f.signer.got.DashboardID)
	require.NotNil(t, f.signer.got.InstanceID)
	assert.Equal(t, dash, *f.signer.got.DashboardID)
	assert.Equal(t, inst, *f.signer.got.InstanceID)
	assert.Nil(t, f.signer.got.ProjectID)

	// Project-scoped widget: membership is checked like for ui-context of any project.
	projIDs := `"dashboard_id":"` + dash.String() + `","instance_id":"` + f.projInst.String() + `","widget_id":"proj"`
	byProject := `{` + ws + `,` + projIDs + `,"project_id":"` + f.ownProj.String() + `"}`
	f.signer.got = uictx.Claims{}
	rec, _ = f.call(t, f.stranger, byProject)
	assert.Equal(t, http.StatusNotFound, rec.Code, "a non-member of the project gets no token")
	assert.Equal(t, uuid.Nil, f.signer.got.UserID, "nothing is signed for a non-member")
	rec, _ = f.call(t, f.member, byProject)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, f.signer.got.ProjectID)
	assert.Equal(t, f.ownProj, *f.signer.got.ProjectID)

	// Project context only where the widget declares scope "project", and always there.
	f.signer.got = uictx.Claims{}
	rec, _ = f.call(t, f.member, `{`+ws+`,`+ids+`,"project_id":"`+f.ownProj.String()+`"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "workspace widget with a project_id")
	rec, _ = f.call(t, f.member, `{`+ws+`,`+projIDs+`}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "project widget without a project_id")
	assert.Equal(t, uuid.Nil, f.signer.got.UserID, "nothing is signed for a scope mismatch")

	// A widget id the manifest does not declare: 409, nothing signed.
	f.signer.got = uictx.Claims{}
	rec, _ = f.call(t, f.user, `{`+ws+`,"dashboard_id":"`+dash.String()+`","instance_id":"`+inst.String()+`","widget_id":"nope"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, "unknown widget id")
	assert.Equal(t, uuid.Nil, f.signer.got.UserID, "nothing is signed for an unknown widget")

	// Malformed requests sign nothing.
	f.signer.got = uictx.Claims{}
	for name, body := range map[string]string{
		"no ids":        `{` + ws + `}`,
		"no instance":   `{` + ws + `,"dashboard_id":"` + dash.String() + `"}`,
		"no widget id":  `{` + ws + `,"dashboard_id":"` + dash.String() + `","instance_id":"` + inst.String() + `"}`,
		"carries task":  `{` + ws + `,` + ids + `,"task_id":"` + f.ownTask.String() + `"}`,
		"ids elsewhere": `{"workspace_id":"` + f.ws.String() + `","slot":"sidebar.item",` + ids + `}`,
	} {
		rec, _ = f.call(t, f.user, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
	}
	assert.Equal(t, uuid.Nil, f.signer.got.UserID, "nothing is signed for a malformed widget request")
}

// The token names a dashboard and an instance; both are checked before anything is signed:
// the dashboard is one the user reads in this workspace, the instance is on it and is this
// extension's widget, and a project widget is set to the project the token names. Every
// refusal is the same 404, so it confirms nothing about what the user cannot see.
func TestUIContext_DashboardWidgetInstance(t *testing.T) {
	f := newUIFixture(t, true, "hello.example.com")
	ws := `"workspace_id":"` + f.ws.String() + `","slot":"dashboard.widget"`
	body := func(dash, inst uuid.UUID, widget, project string) string {
		b := `{` + ws + `,"dashboard_id":"` + dash.String() + `","instance_id":"` + inst.String() + `","widget_id":"` + widget + `"`
		if project != "" {
			b += `,"project_id":"` + project + `"`
		}
		return b + `}`
	}
	own := f.ownProj.String()
	for name, b := range map[string]string{
		"missing dashboard":                    body(uuid.New(), f.inst, "status", ""),
		"dashboard of another workspace":       body(f.elsewhereDash, f.inst, "status", ""),
		"someone else's personal dashboard":    body(f.foreignDash, f.inst, "status", ""),
		"instance of another dashboard":        body(f.dash, f.otherInst, "status", ""),
		"missing instance":                     body(f.dash, uuid.New(), "status", ""),
		"instance of another widget":           body(f.dash, f.projInst, "status", ""),
		"workspace instance as project widget": body(f.dash, f.inst, "proj", own),
		"project instance of another project":  body(f.dash, f.otherProjInst, "proj", own),
	} {
		f.signer.got = uictx.Claims{}
		rec, _ := f.call(t, f.user, b)
		assert.Equal(t, http.StatusNotFound, rec.Code, name)
		assert.Equal(t, uuid.Nil, f.signer.got.UserID, "nothing is signed: "+name)
	}

	// Positive controls on the same fixture: the real instances get their token.
	rec, _ := f.call(t, f.user, body(f.dash, f.inst, "status", ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec, _ = f.call(t, f.user, body(f.otherDash, f.otherInst, "status", ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec, _ = f.call(t, f.member, body(f.dash, f.projInst, "proj", own))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Without the dashboards service no widget token is issued (fail closed).
	f.h.WithDashboards(nil)
	f.signer.got = uictx.Claims{}
	rec, _ = f.call(t, f.user, body(f.dash, f.inst, "status", ""))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, uuid.Nil, f.signer.got.UserID, "nothing is signed without the dashboards service")
}
