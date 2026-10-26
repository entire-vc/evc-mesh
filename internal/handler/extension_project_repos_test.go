package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type fakeProjectRepos struct {
	rows   []repository.VCSRepoActivity
	prefer string
}

func (f *fakeProjectRepos) ProjectProviderRepos(_ context.Context, _ uuid.UUID, _, prefer string, _ int) ([]repository.VCSRepoActivity, error) {
	f.prefer = prefer
	return f.rows, nil
}

// resolvingProvider is a VCS provider whose workspace connection has baseURL.
type resolvingProvider struct {
	projectManifestProvider
	baseURL string
}

func (p resolvingProvider) Resolve(context.Context, uuid.UUID) (extensions.Resolved, error) {
	return extensions.Resolved{Settings: map[string]any{"base_url": p.baseURL}}, nil
}

func vcsProvider(id string) extensions.Provider {
	return resolvingProvider{projectManifestProvider: projectManifestProvider{man: extensions.Manifest{ID: id, Levels: extensions.Levels{Project: true}}}}
}

func serveProjectRepos(h *ExtensionProjectHandler, path string) (int, []projectRepoResponse) {
	e := echo.New()
	e.GET("/projects/:proj_id/extensions/:ext_id/repos", h.Repos)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
	var out struct {
		Repos []projectRepoResponse `json:"repos"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.Repos
}

// an internal ticket: GET .../repos joins the repositories of the project's links with
// the one its binding names, and says which side each is known from.
func TestExtensionProjectHandler_ReposSources(t *testing.T) {
	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	links := []repository.VCSRepoActivity{
		{Repo: "entire-vc/evc-mesh", Host: "gitlab.example.invalid", Links: 12, LastEventAt: at},
		{Repo: "entire-vc/evc-mesh-mcp", Host: "gitlab.example.invalid", Links: 3, LastEventAt: at},
	}
	bindingTo := func(repoPath string, enabled bool) func(context.Context, uuid.UUID, string) (*domain.ProjectIntegration, error) {
		return func(_ context.Context, _ uuid.UUID, ext string) (*domain.ProjectIntegration, error) {
			require.Equal(t, "gitlab", ext)
			if repoPath == "" {
				return nil, apierror.NotFound("ProjectExtension")
			}
			raw, _ := json.Marshal(map[string]any{"repo_path": repoPath})
			return &domain.ProjectIntegration{Enabled: enabled, Settings: raw}, nil
		}
	}
	path := "/projects/" + uuid.NewString() + "/extensions/gitlab/repos"
	run := func(binding string, enabled bool) ([]projectRepoResponse, *fakeProjectRepos) {
		t.Helper()
		src := &fakeProjectRepos{rows: links}
		h := NewExtensionProjectHandler(stubExtensionLookup{"gitlab": vcsProvider("gitlab")},
			&MockProjectIntegrationService{GetBindingFunc: bindingTo(binding, enabled)}).WithRepos(src)
		code, repos := serveProjectRepos(h, path)
		require.Equal(t, http.StatusOK, code)
		return repos, src
	}

	both, src := run("Entire-VC/evc-mesh/", true)
	assert.Equal(t, "entire-vc/evc-mesh", src.prefer, "the bound repository is asked to rank first")
	require.Len(t, both, 2)
	assert.Equal(t, projectRepoResponse{Repo: "entire-vc/evc-mesh", Host: "gitlab.example.invalid", Links: 12, LastEventAt: &at, Bound: true, Source: "both"}, both[0])
	assert.Equal(t, "webhook", both[1].Source)
	assert.False(t, both[1].Bound)

	bindingOnly, _ := run("entire-vc/evc-site", true)
	require.Len(t, bindingOnly, 3)
	assert.Equal(t, projectRepoResponse{Repo: "entire-vc/evc-site", Host: "gitlab.example.invalid", Bound: true, Source: "binding"}, bindingOnly[2],
		"no connection to ask: the host of the project's own links")
	for _, r := range bindingOnly[:2] {
		assert.Equal(t, "webhook", r.Source, r.Repo)
	}

	unbound, src := run("", false)
	assert.Empty(t, src.prefer)
	require.Len(t, unbound, 2)
	for _, r := range unbound {
		assert.Equal(t, "webhook", r.Source, r.Repo)
	}

	disabled, _ := run("entire-vc/evc-mesh", false)
	assert.Equal(t, "webhook", disabled[0].Source, "a disabled binding binds nothing")
}

// A binding-only repository takes its host from the workspace connection's
// base_url, and is never left without one when a connection says where it lives.
func TestExtensionProjectHandler_ReposBindingOnlyHost(t *testing.T) {
	wsID := uuid.New()
	raw, _ := json.Marshal(map[string]any{"repo_path": "g/p"})
	pis := &MockProjectIntegrationService{GetBindingFunc: func(context.Context, uuid.UUID, string) (*domain.ProjectIntegration, error) {
		return &domain.ProjectIntegration{Enabled: true, Settings: raw}, nil
	}}
	projects := &MockProjectService{GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Project, error) {
		return &domain.Project{ID: id, WorkspaceID: wsID}, nil
	}}
	gl := resolvingProvider{projectManifestProvider: projectManifestProvider{man: extensions.Manifest{ID: "gitlab", Levels: extensions.Levels{Project: true}}}, baseURL: "https://GitLab.Example.com/"}
	h := NewExtensionProjectHandler(stubExtensionLookup{"gitlab": gl}, pis).WithProjects(projects).WithRepos(&fakeProjectRepos{})
	code, repos := serveProjectRepos(h, "/projects/"+uuid.NewString()+"/extensions/gitlab/repos")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []projectRepoResponse{{Repo: "g/p", Host: "gitlab.example.com", Bound: true, Source: "binding"}}, repos)

	raw, _ = json.Marshal(map[string]any{"repo": "o/r"})
	gh := resolvingProvider{projectManifestProvider: projectManifestProvider{man: extensions.Manifest{ID: "github", Levels: extensions.Levels{Project: true}}}}
	h = NewExtensionProjectHandler(stubExtensionLookup{"github": gh}, pis).WithProjects(projects).WithRepos(&fakeProjectRepos{})
	_, repos = serveProjectRepos(h, "/projects/"+uuid.NewString()+"/extensions/github/repos")
	require.Equal(t, []projectRepoResponse{{Repo: "o/r", Host: "github.com", Bound: true, Source: "binding"}}, repos)
}

func TestExtensionProjectHandler_ReposOnlyForVCS(t *testing.T) {
	h := NewExtensionProjectHandler(stubExtensionLookup{teamRelayExtensionID: manifestOnlyProvider(true)},
		&MockProjectIntegrationService{}).WithRepos(&fakeProjectRepos{})
	code, _ := serveProjectRepos(h, "/projects/"+uuid.NewString()+"/extensions/team-relay/repos")
	assert.Equal(t, http.StatusNotFound, code)
}
