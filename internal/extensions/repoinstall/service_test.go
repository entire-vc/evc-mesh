package repoinstall

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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// fakeGitLab serves one project: refs map name -> (sha, manifest).
type fakeGitLab struct {
	mu       sync.Mutex
	token    string
	tags     []string
	branches []string
	refs     map[string]fakeRef // by ref name or sha
}

type fakeRef struct {
	sha      string
	manifest string
}

func (f *fakeGitLab) set(name, sha, manifest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs[name] = fakeRef{sha, manifest}
	f.refs[sha] = fakeRef{sha, manifest}
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.token != "" && r.Header.Get("PRIVATE-TOKEN") != f.token {
		http.Error(w, "404", http.StatusNotFound)
		return
	}
	p := r.URL.EscapedPath()
	const base = "/api/v4/projects/grp%2Fhello/repository/"
	if !strings.HasPrefix(p, base) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(p, base)
	switch {
	case rest == "tags" || rest == "branches":
		names := f.tags
		if rest == "branches" {
			names = f.branches
		}
		var out []map[string]any
		for _, n := range names {
			out = append(out, map[string]any{"name": n, "commit": map[string]any{"id": f.refs[n].sha}})
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(rest, "commits/"):
		ref, ok := f.refs[strings.TrimPrefix(rest, "commits/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": ref.sha})
	case strings.HasPrefix(rest, "files/"):
		ref, ok := f.refs[r.URL.Query().Get("ref")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(ref.manifest))
	default:
		http.NotFound(w, r)
	}
}

func manifestJSON(t *testing.T, mutate func(m map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile("../schema/fixtures/valid/mesh-ext-hello.json")
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	if mutate != nil {
		mutate(m)
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return string(out)
}

func withVersion(v string, extraAPI ...string) func(m map[string]any) {
	return func(m map[string]any) {
		m["version"] = v
		if len(extraAPI) > 0 {
			caps := m["capabilities"].(map[string]any)
			caps["api"] = append(caps["api"].([]any), extraAPI[0])
		}
	}
}

// memRepo is an in-memory InstalledExtensionRepository + RepoExtensionRepository + policy.
type memRepo struct {
	mu      sync.Mutex
	rows    map[string]*domain.InstalledExtension
	state   map[string]*domain.ExtensionRepoState
	tokens  map[string]string
	auto    bool
	journal []string
}

func newMemRepo() *memRepo {
	return &memRepo{rows: map[string]*domain.InstalledExtension{}, state: map[string]*domain.ExtensionRepoState{}, tokens: map[string]string{}}
}

func (m *memRepo) UpsertBuiltin(context.Context, *domain.InstalledExtension) error { return nil }
func (m *memRepo) Get(_ context.Context, id string) (*domain.InstalledExtension, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok {
		c := *r
		return &c, nil
	}
	return nil, nil
}
func (m *memRepo) List(context.Context) ([]domain.InstalledExtension, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.InstalledExtension
	for _, r := range m.rows {
		out = append(out, *r)
	}
	return out, nil
}
func (m *memRepo) InsertRepo(_ context.Context, e *domain.InstalledExtension, token string, _ *domain.InstanceExtensionJournalEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[e.ExtensionID]; ok {
		return apierror.Conflict("taken")
	}
	c := *e
	m.rows[e.ExtensionID] = &c
	m.state[e.ExtensionID] = &domain.ExtensionRepoState{}
	if token != "" {
		m.tokens[e.ExtensionID] = token
	}
	return nil
}
func (m *memRepo) GetState(_ context.Context, id string) (*domain.ExtensionRepoState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[id], nil
}
func (m *memRepo) ApplyUpdate(_ context.Context, id, ver, ref, sha string, man []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, st := m.rows[id], m.state[id]
	r.PreviousVersion, st.PreviousManifest, st.PreviousCommitSHA, st.PreviousRef = r.Version, r.Manifest, r.CommitSHA, r.RepoRef
	r.Version, r.RepoRef, r.CommitSHA, r.Manifest = ver, ref, sha, man
	*st = domain.ExtensionRepoState{PreviousManifest: st.PreviousManifest, PreviousCommitSHA: st.PreviousCommitSHA, PreviousRef: st.PreviousRef}
	return nil
}
func (m *memRepo) SetPending(_ context.Context, id, ver, ref, sha string, man []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state[id]
	st.PendingVersion, st.PendingRef, st.PendingCommitSHA, st.PendingManifest = ver, ref, sha, man
	return nil
}
func (m *memRepo) Rollback(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, st := m.rows[id], m.state[id]
	if r.PreviousVersion == "" {
		return apierror.Conflict("nothing to roll back to")
	}
	skipped := r.CommitSHA
	r.Version, r.Manifest, r.CommitSHA, r.RepoRef = r.PreviousVersion, st.PreviousManifest, st.PreviousCommitSHA, st.PreviousRef
	r.PreviousVersion = ""
	*st = domain.ExtensionRepoState{SkippedCommitSHA: skipped}
	return nil
}
func (m *memRepo) GetToken(_ context.Context, id string) (string, error) { return m.tokens[id], nil }

func (m *memRepo) Allowed(context.Context, string) (bool, error) { return true, nil }
func (m *memRepo) ListPolicies(context.Context) (map[string]domain.InstanceExtensionPolicy, error) {
	return nil, nil
}
func (m *memRepo) SetAllowed(context.Context, string, bool, *uuid.UUID) error { return nil }
func (m *memRepo) GetConfig(context.Context) (*domain.InstanceExtensionConfig, error) {
	return &domain.InstanceExtensionConfig{AutoUpdate: m.auto}, nil
}
func (m *memRepo) SetAutoUpdate(context.Context, bool, *uuid.UUID) error { return nil }
func (m *memRepo) AppendJournal(_ context.Context, e *domain.InstanceExtensionJournalEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.journal = append(m.journal, e.Action)
	return nil
}
func (m *memRepo) ListJournal(context.Context, int) ([]domain.InstanceExtensionJournalEntry, error) {
	return nil, nil
}

func setup(t *testing.T, token string) (*Service, *memRepo, *fakeGitLab, string) {
	t.Helper()
	gl := &fakeGitLab{token: token, refs: map[string]fakeRef{}}
	srv := httptest.NewServer(gl)
	t.Cleanup(srv.Close)
	repo := newMemRepo()
	svc := &Service{Client: &Client{HTTP: srv.Client(), AllowInsecure: true}, Repo: repo, Installed: repo, Policy: repo}
	return svc, repo, gl, srv.URL + "/grp/hello"
}

func errCode(err error) string {
	var ae *apierror.Error
	if e, ok := err.(*apierror.Error); ok { //nolint:errorlint // the service returns the value itself
		ae = e
		return ae.ErrorCode
	}
	return ""
}

func TestInstallPrivateRepoByTagAndBranch(t *testing.T) {
	svc, repo, gl, url := setup(t, "s3cret")
	gl.tags, gl.branches = []string{"v0.1.0"}, []string{"main"}
	gl.set("v0.1.0", "aaa111", manifestJSON(t, withVersion("0.1.0")))
	gl.set("main", "bbb222", manifestJSON(t, withVersion("0.1.1")))
	actor := uuid.New()

	// no token: a private repo looks absent
	_, err := svc.Preview(context.Background(), url, "v0.1.0", "")
	require.Error(t, err)

	p, err := svc.Preview(context.Background(), url, "v0.1.0", "s3cret")
	require.NoError(t, err)
	assert.Equal(t, "custom", p.TrustLevel)
	assert.Equal(t, "aaa111", p.CommitSHA)
	assert.NotEmpty(t, p.Warning)

	row, err := svc.Install(context.Background(), actor, url, "v0.1.0", "s3cret")
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionSourceRepo, row.Source)
	assert.Equal(t, domain.ExtensionTrustCustom, row.TrustLevel)
	assert.Equal(t, "aaa111", row.CommitSHA)
	assert.Equal(t, "s3cret", repo.tokens["hello"], "token goes to the credentials store")
	assert.NotContains(t, string(row.Manifest), "s3cret")

	// same id again: conflict
	_, err = svc.Install(context.Background(), actor, url, "main", "s3cret")
	assert.Equal(t, CodeIDConflict, errCode(err))

	// branch install of the same fixture under another repo state
	svc2, _, gl2, url2 := setup(t, "s3cret")
	gl2.branches = []string{"main"}
	gl2.set("main", "bbb222", manifestJSON(t, withVersion("0.1.1")))
	row2, err := svc2.Install(context.Background(), actor, url2, "main", "s3cret")
	require.NoError(t, err)
	assert.Equal(t, "main", row2.RepoRef)
	assert.Equal(t, "bbb222", row2.CommitSHA)
}

func TestRedControls(t *testing.T) {
	svc, _, gl, url := setup(t, "")
	gl.branches = []string{"main"}
	cases := map[string]struct {
		mutate func(m map[string]any)
		code   string
	}{
		"builtin runtime": {func(m map[string]any) { m["runtime"] = "builtin" }, CodeBuiltinRuntime},
		"mesh_api >=2":    {func(m map[string]any) { m["mesh_api"] = ">=2" }, CodeIncompatible},
		"schema invalid":  {func(m map[string]any) { delete(m, "publisher") }, CodeManifestInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gl.set("main", "ccc333", manifestJSON(t, tc.mutate))
			_, err := svc.Preview(context.Background(), url, "main", "")
			require.Error(t, err)
			assert.Equal(t, tc.code, errCode(err), "%v", err)
			_, err = svc.Install(context.Background(), uuid.New(), url, "main", "")
			assert.Equal(t, tc.code, errCode(err))
		})
	}
	t.Run("positive control: valid manifest passes", func(t *testing.T) {
		gl.set("main", "ddd444", manifestJSON(t, nil))
		_, err := svc.Preview(context.Background(), url, "main", "")
		require.NoError(t, err)
	})
}

// Updates lists repo-installed extensions only: a built-in row (Runner) is
// never offered there, next to a repo extension that is.
func TestUpdatesSkipBuiltins(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	gl.tags, gl.branches = []string{"v0.1.0"}, []string{"main"}
	gl.set("v0.1.0", "aaa111", manifestJSON(t, withVersion("0.1.0")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, "v0.1.0", "")
	require.NoError(t, err)
	repo.rows["runner"] = &domain.InstalledExtension{ExtensionID: "runner", Source: domain.ExtensionSourceBuiltin, Version: "1.0.0"}

	ups, err := svc.Updates(ctx)
	require.NoError(t, err)
	ids := make([]string, 0, len(ups))
	for _, u := range ups {
		ids = append(ids, u.ExtensionID)
	}
	assert.Equal(t, []string{"hello"}, ids)
}

func TestUpdatesAutoUpdateAndConsent(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	gl.tags, gl.branches = []string{"v0.1.0"}, []string{"main"}
	gl.set("v0.1.0", "aaa111", manifestJSON(t, withVersion("0.1.0")))
	gl.set("main", "bbb222", manifestJSON(t, withVersion("0.1.0")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, "v0.1.0", "")
	require.NoError(t, err)

	ups, err := svc.Updates(ctx)
	require.NoError(t, err)
	require.Len(t, ups, 1)
	assert.False(t, ups[0].UpdateAvailable)

	// a new tag without new permissions shows up in Updates and auto-updates
	gl.tags = []string{"v0.1.0", "v0.1.1"}
	gl.set("v0.1.1", "eee555", manifestJSON(t, withVersion("0.1.1")))
	ups, _ = svc.Updates(ctx)
	assert.True(t, ups[0].UpdateAvailable)
	assert.Equal(t, "v0.1.1", ups[0].LatestRef)
	assert.False(t, ups[0].NeedsConsent)

	svc.AutoUpdateOnce(ctx) // flag off: nothing happens
	row, _ := repo.Get(ctx, "hello")
	assert.Equal(t, "0.1.0", row.Version)
	repo.auto = true
	svc.AutoUpdateOnce(ctx)
	row, _ = repo.Get(ctx, "hello")
	assert.Equal(t, "0.1.1", row.Version)
	assert.Equal(t, "v0.1.1", row.RepoRef)

	// 0.2.0 adds a permission: waits for consent even with auto-update on
	gl.tags = []string{"v0.1.0", "v0.1.1", "v0.2.0"}
	gl.set("v0.2.0", "fff666", manifestJSON(t, withVersion("0.2.0", "comments:write")))
	svc.AutoUpdateOnce(ctx)
	svc.AutoUpdateOnce(ctx)
	row, _ = repo.Get(ctx, "hello")
	assert.Equal(t, "0.1.1", row.Version, "no consent, no update")
	ups, _ = svc.Updates(ctx)
	assert.True(t, ups[0].NeedsConsent)
	assert.True(t, ups[0].Pending)
	assert.Equal(t, []string{"api:comments:write"}, ups[0].NewCapabilities)
	n := 0
	for _, a := range repo.journal {
		if a == domain.InstanceJournalUpdatePending {
			n++
		}
	}
	assert.Equal(t, 1, n, "the pending state is journaled once")

	res, err := svc.Update(ctx, nil, "hello", "wrong-sha")
	require.NoError(t, err)
	assert.Equal(t, OutcomePendingConsent, res.Outcome)
	res, err = svc.Update(ctx, nil, "hello", "fff666")
	require.NoError(t, err)
	assert.Equal(t, OutcomeUpdated, res.Outcome)
	row, _ = repo.Get(ctx, "hello")
	assert.Equal(t, "0.2.0", row.Version)

	// rollback returns to 0.1.1
	back, err := svc.Rollback(ctx, nil, "hello")
	require.NoError(t, err)
	assert.Equal(t, "0.1.1", back.Version)
	_, err = svc.Rollback(ctx, nil, "hello")
	require.Error(t, err, "second rollback has nothing to go back to")
}

func TestBranchHeadUpdate(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	gl.branches = []string{"main"}
	gl.set("main", "bbb222", manifestJSON(t, withVersion("0.1.0")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, "main", "")
	require.NoError(t, err)
	gl.set("main", "bbb333", manifestJSON(t, withVersion("0.1.0")))
	repo.auto = true
	svc.AutoUpdateOnce(ctx)
	row, _ := repo.Get(ctx, "hello")
	assert.Equal(t, "bbb333", row.CommitSHA, "branch head commit picked up")
}

func TestMeshAPICompatible(t *testing.T) {
	for expr, want := range map[string]bool{">=1": true, "1": true, "=1": true, ">=2": false, ">=1 <2": true, ">1": false, "<=1": true, "x": false, "": false} {
		assert.Equal(t, want, MeshAPICompatible(expr, 1), expr)
	}
}

func TestParseURL(t *testing.T) {
	c := &Client{}
	for _, bad := range []string{"", "http://github.com/a/b", "https://github.com/a", "https://u:p@github.com/a/b", "https://gitlab.example/a", "https://github.com/a/../b"} {
		_, err := c.ParseURL(bad)
		assert.Error(t, err, bad)
	}
	s, err := c.ParseURL("https://github.com/o/r.git")
	require.NoError(t, err)
	assert.Equal(t, ProviderGitHub, s.Provider)
	s, err = c.ParseURL("https://gitlab.example.invalid/entire-vc/sub/hello/")
	require.NoError(t, err)
	assert.Equal(t, "entire-vc/sub/hello", s.Path)
}

func TestSSRFGuardBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := NewClient()
	c.AllowInsecure = true
	src, err := c.ParseURL(srv.URL + "/a/b")
	require.NoError(t, err)
	_, err = c.Tags(context.Background(), src, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

func TestTokenNotForwardedOnCrossHostRedirect(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL+"/x")
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()
	c := &Client{HTTP: origin.Client(), AllowInsecure: true}
	src, err := c.ParseURL(origin.URL + "/a/b")
	require.NoError(t, err)

	_, err = c.Tags(context.Background(), src, "glpat-secret")
	require.Error(t, err)
	assert.Empty(t, leaked, "token must not reach the redirect target")
	assert.NotContains(t, err.Error(), "glpat-secret")

	// Positive control: without a token the redirect is followed.
	_, err = c.Tags(context.Background(), src, "")
	require.NoError(t, err)
}

func TestTokenNotForwardedOnSchemeDowngrade(t *testing.T) {
	var leaked string
	// Same host:port is impossible to serve as both schemes in httptest, so check the guard directly
	// through a redirect whose target differs only by scheme: the test server answers http, the
	// redirect asks for https on the same host, which must be refused as well.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("Location", "https://"+strings.TrimPrefix(srv.URL, "http://")+"/x")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), AllowInsecure: true}
	src, err := c.ParseURL(srv.URL + "/a/b")
	require.NoError(t, err)
	_, err = c.Tags(context.Background(), src, "glpat-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")
	assert.Equal(t, "glpat-secret", leaked, "first hop carries the token; only the redirect is refused")
}

func TestAutoUpdateDoesNotUndoRollback(t *testing.T) {
	svc, repo, gl, url := setup(t, "")
	gl.branches = []string{"main"}
	gl.set("main", "aaa111", manifestJSON(t, withVersion("0.1.0")))
	ctx := context.Background()
	_, err := svc.Install(ctx, uuid.New(), url, "main", "")
	require.NoError(t, err)
	repo.auto = true
	gl.set("main", "aaa222", manifestJSON(t, withVersion("0.1.1")))
	svc.AutoUpdateOnce(ctx)
	row, _ := repo.Get(ctx, "hello")
	require.Equal(t, "0.1.1", row.Version)

	_, err = svc.Rollback(ctx, nil, "hello")
	require.NoError(t, err)
	svc.AutoUpdateOnce(ctx)
	svc.AutoUpdateOnce(ctx)
	row, _ = repo.Get(ctx, "hello")
	assert.Equal(t, "0.1.0", row.Version, "auto-update must not reapply the rolled-back commit")
	assert.Equal(t, "aaa111", row.CommitSHA)

	// a manual update still works, and a newer commit is picked up by auto-update
	res, err := svc.Update(ctx, nil, "hello", "")
	require.NoError(t, err)
	assert.Equal(t, OutcomeUpdated, res.Outcome)
	gl.set("main", "aaa333", manifestJSON(t, withVersion("0.1.2")))
	svc.AutoUpdateOnce(ctx)
	row, _ = repo.Get(ctx, "hello")
	assert.Equal(t, "0.1.2", row.Version)
}
