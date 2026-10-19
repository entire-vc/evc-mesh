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

type uiFixture struct {
	h              *ExtensionUIHandler
	signer         *fakeSigner
	user, ws       uuid.UUID
	ownTask, other uuid.UUID
}

func newUIFixture(t *testing.T, enabled bool, domains ...string) *uiFixture {
	t.Helper()
	f := &uiFixture{signer: &fakeSigner{}, user: uuid.New(), ws: uuid.New(), ownTask: uuid.New(), other: uuid.New()}
	man := extensions.Manifest{
		ID: "hello", Runtime: "external",
		Capabilities: extensions.Capabilities{Network: &extensions.Network{AllowedDomains: domains}},
		External: &extensions.External{
			BaseURLSetting: "hello_url",
			UI:             map[string]extensions.ExternalUI{"sidebar.item": {Path: "/mesh/ui/hello", Title: "Hello"}},
		},
	}
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(uiProvider{
		BaseProvider: extensions.BaseProvider{Man: man},
		res:          extensions.Resolved{ExtensionID: "hello", Enabled: enabled, Settings: map[string]any{"hello_url": "https://hello.example.com"}},
	}))
	ownProj, otherProj := uuid.New(), uuid.New()
	f.h = NewExtensionUIHandler(reg, f.signer, fakeMembers{f.user: "admin"},
		fakeUsers{&domain.User{ID: f.user, Email: "me@example.com"}},
		fakeTasks{f.ownTask: {ID: f.ownTask, ProjectID: ownProj}, f.other: {ID: f.other, ProjectID: otherProj}},
		fakeProjects{ownProj: {ID: ownProj, WorkspaceID: f.ws}, otherProj: {ID: otherProj, WorkspaceID: uuid.New()}})
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
