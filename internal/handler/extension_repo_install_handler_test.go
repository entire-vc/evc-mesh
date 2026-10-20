package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type repoAdmins struct {
	ok  bool
	err error
}

func (a repoAdmins) IsInstanceAdmin(context.Context, uuid.UUID) (bool, error) { return a.ok, a.err }

// repoStore is one in-memory store behind the three repository interfaces the service needs.
type repoStore struct {
	mu    sync.Mutex
	rows  map[string]*domain.InstalledExtension
	state map[string]*domain.ExtensionRepoState
}

func newRepoStore() *repoStore {
	return &repoStore{rows: map[string]*domain.InstalledExtension{}, state: map[string]*domain.ExtensionRepoState{}}
}

func (m *repoStore) UpsertBuiltin(context.Context, *domain.InstalledExtension) error { return nil }
func (m *repoStore) Get(_ context.Context, id string) (*domain.InstalledExtension, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok {
		c := *r
		return &c, nil
	}
	return nil, nil
}
func (m *repoStore) List(context.Context) ([]domain.InstalledExtension, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.InstalledExtension
	for _, r := range m.rows {
		out = append(out, *r)
	}
	return out, nil
}
func (m *repoStore) InsertRepo(_ context.Context, e *domain.InstalledExtension, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *e
	m.rows[e.ExtensionID] = &c
	m.state[e.ExtensionID] = &domain.ExtensionRepoState{}
	return nil
}
func (m *repoStore) GetState(_ context.Context, id string) (*domain.ExtensionRepoState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[id], nil
}
func (m *repoStore) ApplyUpdate(_ context.Context, id, ver, ref, sha string, man []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, st := m.rows[id], m.state[id]
	r.PreviousVersion, st.PreviousManifest, st.PreviousCommitSHA, st.PreviousRef = r.Version, r.Manifest, r.CommitSHA, r.RepoRef
	r.Version, r.RepoRef, r.CommitSHA, r.Manifest = ver, ref, sha, man
	return nil
}
func (m *repoStore) SetPending(context.Context, string, string, string, string, []byte) error {
	return nil
}
func (m *repoStore) Rollback(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, st := m.rows[id], m.state[id]
	if r == nil || r.PreviousVersion == "" {
		return apierror.Conflict("nothing to roll back to")
	}
	r.Version, r.Manifest, r.CommitSHA, r.RepoRef = r.PreviousVersion, st.PreviousManifest, st.PreviousCommitSHA, st.PreviousRef
	r.PreviousVersion = ""
	return nil
}
func (m *repoStore) GetToken(context.Context, string) (string, error) { return "", nil }

func (m *repoStore) Allowed(context.Context, string) (bool, error) { return true, nil }
func (m *repoStore) ListPolicies(context.Context) (map[string]domain.InstanceExtensionPolicy, error) {
	return nil, nil
}
func (m *repoStore) SetAllowed(context.Context, string, bool, *uuid.UUID) error { return nil }
func (m *repoStore) GetConfig(context.Context) (*domain.InstanceExtensionConfig, error) {
	return &domain.InstanceExtensionConfig{}, nil
}
func (m *repoStore) SetAutoUpdate(context.Context, bool, *uuid.UUID) error { return nil }
func (m *repoStore) AppendJournal(context.Context, *domain.InstanceExtensionJournalEntry) error {
	return nil
}
func (m *repoStore) ListJournal(context.Context, int) ([]domain.InstanceExtensionJournalEntry, error) {
	return nil, nil
}

// repoGitLab serves one public project "grp/hello": a tag, a branch and a manifest per ref.
type repoGitLab struct {
	mu        sync.Mutex
	manifests map[string]string // ref -> manifest
	shas      map[string]string // ref -> commit sha
}

func (g *repoGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	const base = "/api/v4/projects/grp%2Fhello/repository/"
	p := r.URL.EscapedPath()
	if !strings.HasPrefix(p, base) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(p, base)
	switch {
	case rest == "tags":
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "v1", "commit": map[string]any{"id": g.shas["v1"]}}})
	case rest == "branches":
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "main", "commit": map[string]any{"id": g.shas["main"]}}})
	case strings.HasPrefix(rest, "commits/"):
		ref := strings.TrimPrefix(rest, "commits/")
		if sha, ok := g.shas[ref]; ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": sha})
			return
		}
		http.NotFound(w, r)
	case strings.HasPrefix(rest, "files/"):
		ref := r.URL.Query().Get("ref")
		if m, ok := g.manifests[ref]; ok {
			_, _ = w.Write([]byte(m))
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func repoManifest(t *testing.T, version string) string {
	t.Helper()
	raw, err := os.ReadFile("../extensions/schema/fixtures/valid/mesh-ext-hello.json")
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m["version"] = version
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

type repoFixture struct {
	h    *ExtensionRepoInstallHandler
	gl   *repoGitLab
	url  string
	repo *repoStore
}

func newRepoFixture(t *testing.T, admin bool) *repoFixture {
	t.Helper()
	gl := &repoGitLab{manifests: map[string]string{}, shas: map[string]string{}}
	srv := httptest.NewServer(gl)
	t.Cleanup(srv.Close)
	store := newRepoStore()
	svc := &repoinstall.Service{
		Client: &repoinstall.Client{HTTP: srv.Client(), AllowInsecure: true},
		Repo:   store, Installed: store, Policy: store,
	}
	return &repoFixture{h: NewExtensionRepoInstallHandler(svc, repoAdmins{ok: admin}), gl: gl, url: srv.URL + "/grp/hello", repo: store}
}

func (f *repoFixture) publish(t *testing.T, ref, sha, version string) {
	t.Helper()
	f.gl.mu.Lock()
	defer f.gl.mu.Unlock()
	m := repoManifest(t, version)
	f.gl.manifests[ref], f.gl.shas[ref] = m, sha
	f.gl.manifests[sha] = m // the client reads the manifest at the resolved commit
}

func repoCall(t *testing.T, h echo.HandlerFunc, body, extID string, agent bool) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := adminCtx(http.MethodPost, body, extID, agent)
	require.NoError(t, h(c))
	return rec
}

func TestRepoInstallHandlerRejectsNonAdmins(t *testing.T) {
	f := newRepoFixture(t, false)
	handlers := map[string]echo.HandlerFunc{
		"refs": f.h.Refs, "preview": f.h.Preview, "install": f.h.Install, "updates": f.h.Updates,
		"update": f.h.Update, "update-all": f.h.UpdateAll, "rollback": f.h.Rollback,
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusForbidden, repoCall(t, h, `{"repo_url":"x"}`, "hello", false).Code)
			assert.Equal(t, http.StatusForbidden, repoCall(t, h, `{"repo_url":"x"}`, "hello", true).Code, "agents are refused too")
		})
	}
}

func TestRepoInstallHandlerBadBodies(t *testing.T) {
	f := newRepoFixture(t, true)
	for name, h := range map[string]echo.HandlerFunc{"refs": f.h.Refs, "preview": f.h.Preview, "install": f.h.Install} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, http.StatusBadRequest, repoCall(t, h, `{`, "", false).Code, "malformed JSON")
			assert.Equal(t, http.StatusBadRequest, repoCall(t, h, `{"repo_url":"  "}`, "", false).Code, "blank repo_url")
		})
	}
	rec := repoCall(t, f.h.Install, `{"source":"ftp","repo_url":"`+f.url+`"}`, "", false)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "source")
}

func TestRepoInstallHandlerFlow(t *testing.T) {
	f := newRepoFixture(t, true)
	f.publish(t, "v1", "aaa111", "0.1.0")
	f.publish(t, "main", "bbb222", "0.1.0")
	body := `{"source":"repo","repo_url":"` + f.url + `","ref":"v1","auth_token":" tok "}`

	rec := repoCall(t, f.h.Refs, body, "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"v1"`)
	assert.Contains(t, rec.Body.String(), `"main"`)

	rec = repoCall(t, f.h.Preview, body, "", false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"trust_level":"custom"`)

	rec = repoCall(t, f.h.Install, body, "", false)
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"commit_sha":"aaa111"`)
	assert.NotContains(t, rec.Body.String(), "tok", "the token is never echoed")

	rec = repoCall(t, f.h.Updates, ``, "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"updates"`)

	// a new tag content at the same ref is not an update; move the branch install instead
	rec = repoCall(t, f.h.Update, `{"consent_commit_sha":" "}`, "hello", false)
	assert.Equal(t, http.StatusOK, rec.Code)

	f.publish(t, "v1", "ccc333", "0.1.1")
	rec = repoCall(t, f.h.UpdateAll, ``, "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"results"`)

	rec = repoCall(t, f.h.Rollback, ``, "hello", false)
	assert.Contains(t, []int{http.StatusOK, http.StatusConflict}, rec.Code)
}

func TestRepoInstallHandlerServiceErrorsMapToStatus(t *testing.T) {
	f := newRepoFixture(t, true)
	assert.Equal(t, http.StatusNotFound, repoCall(t, f.h.Refs, `{"repo_url":"`+f.url+`x"}`, "", false).Code)
	assert.Equal(t, http.StatusNotFound, repoCall(t, f.h.Preview, `{"repo_url":"`+f.url+`","ref":"nope"}`, "", false).Code)
	assert.Equal(t, http.StatusNotFound, repoCall(t, f.h.Install, `{"source":"repo","repo_url":"`+f.url+`","ref":"nope"}`, "", false).Code)
	assert.GreaterOrEqual(t, repoCall(t, f.h.Update, `{}`, "missing", false).Code, 400)
	assert.GreaterOrEqual(t, repoCall(t, f.h.Rollback, ``, "missing", false).Code, 400)
}

type fakeCatalogInstaller struct {
	id      string
	consent string
	err     error
}

func (f *fakeCatalogInstaller) Install(_ context.Context, _ uuid.UUID, id string) (*domain.InstalledExtension, error) {
	f.id = id
	if f.err != nil {
		return nil, f.err
	}
	return &domain.InstalledExtension{ExtensionID: id, Version: "1.0.0", Source: domain.ExtensionSourceCatalog,
		TrustLevel: "verified", CommitSHA: "abc", ArtifactSHA256: "dead"}, nil
}

func (f *fakeCatalogInstaller) Updates(context.Context) ([]repoinstall.UpdateInfo, error) {
	return []repoinstall.UpdateInfo{{ExtensionID: "cat-ext", CurrentVersion: "1.0.0", LatestVersion: "1.1.0", UpdateAvailable: true, NewCapabilities: []string{}}}, nil
}

func (f *fakeCatalogInstaller) Update(_ context.Context, _ *uuid.UUID, id, consent string) (*repoinstall.UpdateResult, error) {
	f.id, f.consent = id, consent
	return &repoinstall.UpdateResult{ExtensionID: id, Outcome: repoinstall.OutcomeUpdated, NewCapabilities: []string{}}, nil
}

func (f *fakeCatalogInstaller) UpdateAll(context.Context, *uuid.UUID) ([]repoinstall.UpdateResult, error) {
	return []repoinstall.UpdateResult{{ExtensionID: "cat-ext", Outcome: repoinstall.OutcomeUpdated, NewCapabilities: []string{}}}, nil
}

func TestRepoInstallHandlerCatalogUpdates(t *testing.T) {
	f := newRepoFixture(t, true)
	fake := &fakeCatalogInstaller{}
	f.h.WithCatalogInstall(fake)

	rec := repoCall(t, f.h.Updates, ``, "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"cat-ext"`, "catalog rows join the Updates tab")

	// an id the repo service does not know falls through to the catalog installer
	rec = repoCall(t, f.h.Update, `{"consent_commit_sha":" abc "}`, "cat-ext", false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "cat-ext", fake.id)
	assert.Equal(t, "abc", fake.consent)

	rec = repoCall(t, f.h.UpdateAll, ``, "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"cat-ext"`)
}

func TestRepoInstallHandlerCatalogSource(t *testing.T) {
	f := newRepoFixture(t, true)
	body := `{"source":"catalog","id":" mesh-ext-hello "}`

	assert.Equal(t, http.StatusNotImplemented, repoCall(t, f.h.Install, body, "", false).Code, "no installer wired")

	fake := &fakeCatalogInstaller{}
	f.h.WithCatalogInstall(fake)
	rec := repoCall(t, f.h.Install, body, "", false)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "mesh-ext-hello", fake.id, "id is trimmed")
	assert.Contains(t, rec.Body.String(), `"source":"catalog"`)
	assert.Contains(t, rec.Body.String(), `"artifact_sha256":"dead"`)

	assert.Equal(t, http.StatusBadRequest, repoCall(t, f.h.Install, `{"source":"catalog"}`, "", false).Code, "id required")

	fake.err = apierror.Conflict("blocked by the catalog: malware")
	rec = repoCall(t, f.h.Install, body, "", false)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "malware")

	denied := newRepoFixture(t, false)
	denied.h.WithCatalogInstall(&fakeCatalogInstaller{})
	assert.Equal(t, http.StatusForbidden, repoCall(t, denied.h.Install, body, "", false).Code)
}
