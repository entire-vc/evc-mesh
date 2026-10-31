package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/agentchat"
	"github.com/entire-vc/evc-mesh/internal/extensions/external"
	"github.com/entire-vc/evc-mesh/internal/extensions/github"
	"github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
	teamrelay "github.com/entire-vc/evc-mesh/internal/extensions/team-relay"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// bindingStore is the database boundary of the generic project binding: a row
// per (project, extension), the way project_integrations keeps them.
type bindingStore struct {
	rows map[string]*domain.ProjectIntegration
}

func newBindingStore() *bindingStore {
	return &bindingStore{rows: map[string]*domain.ProjectIntegration{}}
}

func (b *bindingStore) key(p uuid.UUID, ext string) string { return p.String() + "/" + ext }

func (b *bindingStore) service() *MockProjectIntegrationService {
	return &MockProjectIntegrationService{
		GetBindingFunc: func(_ context.Context, p uuid.UUID, ext string) (*domain.ProjectIntegration, error) {
			if r := b.rows[b.key(p, ext)]; r != nil {
				return r, nil
			}
			return nil, apierror.NotFound("ProjectExtension")
		},
		UpsertBindingFunc: func(_ context.Context, p uuid.UUID, ext string, in service.UpsertProjectBindingInput) (*domain.ProjectIntegration, error) {
			raw, _ := json.Marshal(in.Settings)
			now := time.Now().UTC()
			r := &domain.ProjectIntegration{ID: uuid.New(), ProjectID: p, ExtensionID: ext, Enabled: in.Enabled, Settings: raw, AgentKey: in.AgentKey, CreatedAt: now, UpdatedAt: now}
			if old := b.rows[b.key(p, ext)]; old != nil && in.AgentKey == "" {
				r.AgentKey = old.AgentKey
			}
			b.rows[b.key(p, ext)] = r
			return r, nil
		},
		DeleteBindingFunc: func(_ context.Context, p uuid.UUID, ext string) error {
			delete(b.rows, b.key(p, ext))
			return nil
		},
		ListFunc: func(_ context.Context, p uuid.UUID) ([]domain.ProjectIntegration, error) {
			var out []domain.ProjectIntegration
			for _, r := range b.rows {
				if r.ProjectID == p {
					out = append(out, *r)
				}
			}
			return out, nil
		},
	}
}

func serveExt(h *ExtensionProjectHandler, method, path, body string) (rec *httptest.ResponseRecorder, out map[string]any) {
	e := echo.New()
	e.GET("/projects/:proj_id/extensions", h.List)
	e.GET("/projects/:proj_id/extensions/:ext_id", h.Get)
	e.PUT("/projects/:proj_id/extensions/:ext_id", h.Put)
	e.DELETE("/projects/:proj_id/extensions/:ext_id", h.Delete)
	e.GET("/projects/:proj_id/extensions/:ext_id/actions", h.ListActions)
	e.GET("/projects/:proj_id/extensions/:ext_id/actions/:action", h.Action)
	e.POST("/projects/:proj_id/extensions/:ext_id/actions/:action", h.Action)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func realRegistry() stubExtensionLookup {
	return stubExtensionLookup{
		"gitlab":     gitlab.New(extensions.Deps{}),
		"github":     github.New(extensions.Deps{}),
		"team-relay": teamrelay.New(extensions.Deps{}),
	}
}

func TestExtensionProjectHandler_GitlabBindingLifecycle(t *testing.T) {
	store := newBindingStore()
	h := NewExtensionProjectHandler(realRegistry(), store.service())
	p := uuid.NewString()
	path := "/projects/" + p + "/extensions/gitlab"

	rec, _ := serveExt(h, http.MethodGet, path, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "never set")

	rec, out := serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"repo_path":"entire-vc/evc-mesh"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "gitlab", out["extension_id"])
	assert.Equal(t, true, out["enabled"])
	assert.Equal(t, "entire-vc/evc-mesh", out["settings"].(map[string]any)["repo_path"])
	assert.Equal(t, false, out["key_expiring_soon"], "an unknown expiry is not 'expiring soon'")

	rec, out = serveExt(h, http.MethodGet, path, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "entire-vc/evc-mesh", out["settings"].(map[string]any)["repo_path"])

	// The GET answer is accepted back by PUT unchanged.
	body, _ := json.Marshal(map[string]any{"enabled": out["enabled"], "settings": out["settings"]})
	rec, _ = serveExt(h, http.MethodPut, path, string(body))
	assert.Equal(t, http.StatusOK, rec.Code, "GET -> PUT round trip")

	rec, _ = serveExt(h, http.MethodDelete, path, "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	rec, _ = serveExt(h, http.MethodGet, path, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "deleted")
}

// Red control for the schema: each refusal is a 400 and writes nothing.
func TestExtensionProjectHandler_GenericPutRefusals(t *testing.T) {
	store := newBindingStore()
	h := NewExtensionProjectHandler(realRegistry(), store.service())
	path := "/projects/" + uuid.NewString() + "/extensions/gitlab"

	for name, body := range map[string]string{
		"key outside the schema":      `{"enabled":true,"settings":{"repo_path":"a/b","bogus":1}}`,
		"repo_path not group/project": `{"enabled":true,"settings":{"repo_path":"nogroup"}}`,
		"no settings":                 `{"enabled":true}`,
		"settings not an object":      `{"enabled":true,"settings":[1]}`,
		"another extension's key":     `{"enabled":true,"settings":{"repo_path":"a/b","repo":"a/b"}}`,
	} {
		rec, _ := serveExt(h, http.MethodPut, path, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name+": "+rec.Body.String())
	}
	assert.Empty(t, store.rows, "a refused save writes nothing")

	rec, _ := serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"repo_path":"a/b","bogus":1}}`)
	assert.Contains(t, rec.Body.String(), "bogus", "the answer names the key")

	// The same save with the stray key removed passes: the 400 above is about the key.
	rec, _ = serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"repo_path":"a/b"}}`)
	assert.Equal(t, http.StatusOK, rec.Code)

	for _, ext := range []string{"nope", "spark"} {
		rec, _ = serveExt(h, http.MethodPut, "/projects/"+uuid.NewString()+"/extensions/"+ext, `{"enabled":true,"settings":{}}`)
		assert.Equal(t, http.StatusNotFound, rec.Code, ext+" has no project level")
	}
}

// An installed external extension that declares levels.project=true accepts its
// project binding (the QA manifest: empty project schema, settings {}); one that
// does not declare it is still refused.
func TestExtensionProjectHandler_ExternalProjectBinding(t *testing.T) {
	const tmpl = `{"schema":1,"id":"qa-scope","name":"QA scope","version":"0.1.0","kind":"plugin","runtime":"external",
 "mesh_api":">=1","publisher":"example","category":"development",
 "levels":{"instance":false,"workspace":true,"project":PROJECT},
 "settings":{"workspace":{"type":"object","properties":{"qa_url":{"type":"string"}}},"project":{"type":"object","properties":{}}},
 "capabilities":{"api":["tasks:read"],"events":[],"ui":[]},
 "external":{"base_url_setting":"qa_url"}}`
	provider := func(project string) extensions.Provider {
		m, err := extensions.ParseManifest([]byte(strings.Replace(tmpl, "PROJECT", project, 1)))
		require.NoError(t, err)
		p, err := external.NewProvider(m, extensions.Deps{}, http.DefaultClient, nil)
		require.NoError(t, err)
		return p
	}

	store := newBindingStore()
	h := NewExtensionProjectHandler(stubExtensionLookup{"qa-scope": provider("true")}, store.service())
	path := "/projects/" + uuid.NewString() + "/extensions/qa-scope"
	for _, body := range []string{`{"enabled":true,"settings":{}}`, `{"enabled":true}`} {
		rec, out := serveExt(h, http.MethodPut, path, body)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, true, out["enabled"])
	}
	rec, _ := serveExt(h, http.MethodGet, path, "")
	assert.Equal(t, http.StatusOK, rec.Code, "the binding was stored")

	off := NewExtensionProjectHandler(stubExtensionLookup{"qa-scope": provider("false")}, newBindingStore().service())
	rec, _ = serveExt(off, http.MethodPut, path, `{"enabled":true,"settings":{}}`)
	assert.NotEqual(t, http.StatusOK, rec.Code, "levels.project=false stays refused")
}

// secretProvider is a project-level extension with a declared secret, to prove
// the generic path masks and keeps it without any Team Relay code.
type secretProvider struct {
	extensions.Provider
	man extensions.Manifest
}

func (s secretProvider) Manifest() extensions.Manifest                   { return s.man }
func (s secretProvider) Validate(extensions.Level, map[string]any) error { return nil }

func TestExtensionProjectHandler_GenericSecretIsMaskedAndKept(t *testing.T) {
	man := extensions.Manifest{
		ID:       "vault",
		Levels:   extensions.Levels{Project: true},
		Settings: json.RawMessage(`{"project":{"properties":{"url":{"type":"string"},"agent_key":{"type":"string","x-secret":true},"token":{"type":"string","x-secret":true}}}}`),
	}
	store := newBindingStore()
	h := NewExtensionProjectHandler(stubExtensionLookup{"vault": secretProvider{man: man}}, store.service())
	p := uuid.New()
	path := "/projects/" + p.String() + "/extensions/vault"

	rec, out := serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"url":"u","agent_key":"s3cret"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "***", out["settings"].(map[string]any)["agent_key"])
	assert.NotContains(t, rec.Body.String(), "s3cret", "the value never leaves the server")
	assert.NotContains(t, string(store.rows[store.key(p, "vault")].Settings), "s3cret", "the secret is not stored in settings")

	rec, _ = serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"url":"u2","agent_key":"***"}}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "s3cret", store.rows[store.key(p, "vault")].AgentKey, `"***" keeps the stored secret`)

	rec, _ = serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"url":"u","token":"x"}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a secret with no column to live in is refused, not stored in the clear")
}

func TestExtensionProjectHandler_ListShowsBindingsWithSyncState(t *testing.T) {
	store := newBindingStore()
	h := NewExtensionProjectHandler(realRegistry(), store.service())
	p := uuid.New()
	checked := time.Now().Add(-time.Minute).UTC()
	status, errMsg := "error", "relay unreachable"
	expires := time.Now().Add(3 * 24 * time.Hour).UTC()
	src := "source"
	store.rows[store.key(p, "team-relay")] = &domain.ProjectIntegration{
		ProjectID: p, ExtensionID: "team-relay", Enabled: true, Settings: json.RawMessage(`{"share_slug":"docs"}`),
		AgentKey: "k", LastSyncCheckedAt: &checked, LastSyncStatus: &status, LastSyncError: &errMsg,
		KeyExpiresAt: &expires, KeyExpirySource: &src,
	}
	store.rows[store.key(p, "gitlab")] = &domain.ProjectIntegration{ProjectID: p, ExtensionID: "gitlab", Settings: json.RawMessage(`{"repo_path":"a/b"}`)}
	// A binding of an extension that has no project level is not listed.
	store.rows[store.key(p, "spark")] = &domain.ProjectIntegration{ProjectID: p, ExtensionID: "spark", Settings: json.RawMessage(`{}`)}
	other := uuid.New()
	store.rows[store.key(other, "github")] = &domain.ProjectIntegration{ProjectID: other, ExtensionID: "github", Settings: json.RawMessage(`{}`)}

	rec, out := serveExt(h, http.MethodGet, "/projects/"+p.String()+"/extensions", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var ids []string
	byID := map[string]map[string]any{}
	for _, it := range out["extensions"].([]any) {
		m := it.(map[string]any)
		ids = append(ids, m["extension_id"].(string))
		byID[m["extension_id"].(string)] = m
	}
	sort.Strings(ids)
	assert.Equal(t, []string{"gitlab", "team-relay"}, ids, "this project's bindings only")
	tr := byID["team-relay"]
	assert.Equal(t, "error", tr["last_sync_status"])
	assert.Equal(t, "relay unreachable", tr["last_sync_error"])
	assert.NotEmpty(t, tr["last_sync_checked_at"])
	assert.Equal(t, true, tr["key_expiring_soon"])
	assert.Equal(t, "source", tr["key_expiry_source"])
	assert.NotContains(t, rec.Body.String(), `"k"`)
}

// project.view is served only for the projects where the extension is enabled,
// and a requires entry that is not enabled in the project becomes unmet_requires.
func TestExtensionProjectHandler_ListServesProjectViewPerProject(t *testing.T) {
	man := extensions.Manifest{
		ID: "chat-agent", Levels: extensions.Levels{Project: true},
		Requires:    []string{"team-relay"},
		ProjectView: &extensions.ProjectView{Icon: "message-square", Title: "Chat", After: "board"},
	}
	reg := realRegistry()
	reg["chat-agent"] = projectManifestProvider{man: man}
	store := newBindingStore()
	h := NewExtensionProjectHandler(reg, store.service())
	with, without := uuid.New(), uuid.New()
	store.rows[store.key(with, "chat-agent")] = &domain.ProjectIntegration{ProjectID: with, ExtensionID: "chat-agent", Enabled: true, Settings: json.RawMessage(`{}`)}
	store.rows[store.key(without, "chat-agent")] = &domain.ProjectIntegration{ProjectID: without, ExtensionID: "chat-agent", Enabled: false, Settings: json.RawMessage(`{}`)}
	store.rows[store.key(without, "team-relay")] = &domain.ProjectIntegration{ProjectID: without, ExtensionID: "team-relay", Enabled: true, Settings: json.RawMessage(`{}`)}

	item := func(p uuid.UUID, ext string) map[string]any {
		_, out := serveExt(h, http.MethodGet, "/projects/"+p.String()+"/extensions", "")
		for _, it := range out["extensions"].([]any) {
			if m := it.(map[string]any); m["extension_id"] == ext {
				return m
			}
		}
		return nil
	}
	enabled := item(with, "chat-agent")
	require.NotNil(t, enabled)
	view := enabled["project_view"].(map[string]any)
	assert.Equal(t, "Chat", view["title"])
	assert.Equal(t, "message-square", view["icon"])
	assert.Equal(t, "board", view["after"])
	assert.Equal(t, "/w/:ws/p/:proj/chat-agent/*", view["route"])
	assert.Equal(t, []any{"team-relay"}, enabled["unmet_requires"], "team-relay is not enabled in this project")

	off := item(without, "chat-agent")
	require.NotNil(t, off)
	assert.NotContains(t, off, "project_view", "disabled in this project: no tab")
	assert.NotContains(t, off, "unmet_requires")
	assert.NotContains(t, item(without, "team-relay"), "project_view")
}

// Chat with Agent requires the Runner, which is hidden from the extension lists
// and has no project level: enabling Chat in a project leaves nothing unmet.
func TestExtensionProjectHandler_ChatRequiresHiddenRunner(t *testing.T) {
	reg := realRegistry()
	reg["agent-chat"] = agentchat.New(extensions.Deps{})
	reg["runner"] = runner.New(extensions.Deps{})
	require.Equal(t, []string{"runner"}, reg["agent-chat"].Manifest().Requires)
	store := newBindingStore()
	h := NewExtensionProjectHandler(reg, store.service())
	p := uuid.New()
	store.rows[store.key(p, "agent-chat")] = &domain.ProjectIntegration{ProjectID: p, ExtensionID: "agent-chat", Enabled: true, Settings: json.RawMessage(`{}`)}

	_, out := serveExt(h, http.MethodGet, "/projects/"+p.String()+"/extensions", "")
	items := out["extensions"].([]any)
	require.Len(t, items, 1)
	chat := items[0].(map[string]any)
	assert.Equal(t, "agent-chat", chat["extension_id"])
	assert.NotContains(t, chat, "unmet_requires")
}

// The Team Relay PUT is typed in Go and described in the manifest; they must
// name the same keys, or a setting the old block had is lost on one side.
// share_id and share_slug are accepted from API clients but are not form
// fields: the share follows from the agent key and the form shows its name.
func TestTeamRelayProjectSettings_MatchManifest(t *testing.T) {
	apiOnly := map[string]bool{"share_id": true, "share_slug": true}
	var fromStruct []string
	rt := reflect.TypeOf(teamRelayProjectSettings{})
	for i := 0; i < rt.NumField(); i++ {
		if k := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]; !apiOnly[k] {
			fromStruct = append(fromStruct, k)
		}
	}
	var fromManifest []string
	for k, f := range teamrelay.New(extensions.Deps{}).Manifest().SettingFields("project") {
		if !f.ReadOnly {
			fromManifest = append(fromManifest, k)
		}
	}
	sort.Strings(fromStruct)
	sort.Strings(fromManifest)
	assert.Equal(t, fromStruct, fromManifest)
	for _, k := range []string{"include_project_slug", "docs_mount_path", "excluded_paths", "sync_interval_minutes"} {
		assert.Contains(t, fromManifest, k)
	}
	fields := teamrelay.New(extensions.Deps{}).Manifest().SettingFields("project")
	for k := range apiOnly {
		assert.NotContains(t, fields, k, "the form must not ask for the share's id or slug")
	}
	require.Contains(t, fields, "share_name")
	assert.True(t, fields["share_name"].ReadOnly)
}

func TestExtensionProjectHandler_TeamRelayMountSettingsRoundTrip(t *testing.T) {
	projectID := uuid.New()
	var got service.UpsertProjectIntegrationInput
	svc := &MockProjectIntegrationService{
		UpsertTeamRelayFunc: func(_ context.Context, _ uuid.UUID, in service.UpsertProjectIntegrationInput) (*domain.ProjectIntegration, error) {
			got = in
			pi := sampleTeamRelayIntegration(projectID, "k")
			pi.Settings, _ = json.Marshal(domain.TeamRelaySettings{
				ShareSlug: in.ShareSlug, IncludeProjectSlug: in.IncludeProjectSlug, DocsMountPath: in.DocsMountPath,
				ExcludedPaths: in.ExcludedPaths, SyncIntervalMinutes: in.SyncIntervalMinutes,
			})
			return pi, nil
		},
	}
	h := NewExtensionProjectHandler(realRegistry(), svc)
	path := "/projects/" + projectID.String() + "/extensions/team-relay"

	rec, out := serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"share_slug":"docs","include_project_slug":true,"docs_mount_path":"Team Relay","excluded_paths":["artifacts","tmp"],"sync_interval_minutes":30}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, got.IncludeProjectSlug)
	assert.Equal(t, "Team Relay", got.DocsMountPath)
	assert.Equal(t, []string{"artifacts", "tmp"}, got.ExcludedPaths)
	require.NotNil(t, got.SyncIntervalMinutes)
	assert.Equal(t, 30, *got.SyncIntervalMinutes)
	s := out["settings"].(map[string]any)
	assert.Equal(t, true, s["include_project_slug"])
	assert.Equal(t, "Team Relay", s["docs_mount_path"])
	assert.Equal(t, []any{"artifacts", "tmp"}, s["excluded_paths"])
	assert.EqualValues(t, 30, s["sync_interval_minutes"])

	// key_expires_at is read off the relay, never set by a client.
	rec, _ = serveExt(h, http.MethodPut, path, `{"enabled":true,"settings":{"share_slug":"docs","key_expires_at":"2027-01-01T00:00:00Z"}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

type healthProvider struct {
	extensions.Provider
	res    extensions.Resolved
	report extensions.HealthReport
	called *bool
}

func (p healthProvider) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: "gitlab", Levels: extensions.Levels{Project: true}}
}

func (p healthProvider) Resolve(context.Context, uuid.UUID) (extensions.Resolved, error) {
	return p.res, nil
}

func (p healthProvider) Health(context.Context, extensions.Resolved) extensions.HealthReport {
	*p.called = true
	return p.report
}

func TestExtensionProjectHandler_Actions(t *testing.T) {
	store := newBindingStore()
	p := uuid.New()
	store.rows[store.key(p, "gitlab")] = &domain.ProjectIntegration{ProjectID: p, ExtensionID: "gitlab", Enabled: true, Settings: json.RawMessage(`{"repo_path":"a/b"}`)}
	called := false
	ran := map[string]int{}
	provider := healthProvider{res: extensions.Resolved{Enabled: true, Source: extensions.SourceWorkspace}, report: extensions.HealthReport{OK: true}, called: &called}
	projects := &MockProjectService{GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Project, error) {
		return &domain.Project{ID: id, WorkspaceID: uuid.New()}, nil
	}}
	h := NewExtensionProjectHandler(stubExtensionLookup{"gitlab": provider, "team-relay": manifestOnlyProvider(true)}, store.service()).
		WithProjects(projects).
		WithActions("team-relay",
			ProjectAction{Name: "search", Method: http.MethodGet, Title: "s", Run: func(c echo.Context) error {
				ran["search"]++
				return c.JSON(http.StatusOK, map[string]any{"docs": []any{}})
			}},
			ProjectAction{Name: "mount", Method: http.MethodPost, Title: "m", Run: func(c echo.Context) error {
				ran["mount"]++
				return c.NoContent(http.StatusOK)
			}},
		)
	base := "/projects/" + p.String() + "/extensions/"

	rec, out := serveExt(h, http.MethodGet, base+"team-relay/actions", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var names []string
	for _, a := range out["actions"].([]any) {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	assert.Equal(t, []string{"check", "search", "mount"}, names)

	rec, _ = serveExt(h, http.MethodGet, base+"team-relay/actions/search?q=x", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	rec, _ = serveExt(h, http.MethodPost, base+"team-relay/actions/mount", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]int{"search": 1, "mount": 1}, ran)

	// An action answers only on its own method: a write called as GET never runs.
	rec, _ = serveExt(h, http.MethodGet, base+"team-relay/actions/mount", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec, _ = serveExt(h, http.MethodPost, base+"team-relay/actions/search", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec, _ = serveExt(h, http.MethodPost, base+"team-relay/actions/nope", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	// Another extension does not inherit them.
	rec, _ = serveExt(h, http.MethodPost, base+"gitlab/actions/mount", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, map[string]int{"search": 1, "mount": 1}, ran)

	// check: healthy, then unhealthy, then off for the workspace (the provider is not asked).
	rec, out = serveExt(h, http.MethodPost, base+"gitlab/actions/check", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, out["ok"])
	assert.Equal(t, "workspace", out["source"])
	assert.NotEmpty(t, out["checked_at"])
	assert.True(t, called)

	provider.report = extensions.HealthReport{Message: "gitlab unreachable"}
	h.registry = stubExtensionLookup{"gitlab": provider}
	_, out = serveExt(h, http.MethodPost, base+"gitlab/actions/check", "")
	assert.Equal(t, false, out["ok"])
	assert.Equal(t, "gitlab unreachable", out["message"])

	called = false
	provider.res = extensions.Resolved{}
	h.registry = stubExtensionLookup{"gitlab": provider}
	_, out = serveExt(h, http.MethodPost, base+"gitlab/actions/check", "")
	assert.Equal(t, false, out["ok"])
	assert.False(t, called, "an extension that is off for the workspace is not probed")

	// No binding, no check.
	rec, _ = serveExt(h, http.MethodPost, "/projects/"+uuid.NewString()+"/extensions/gitlab/actions/check", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
