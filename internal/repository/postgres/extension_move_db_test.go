package postgres

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

var _ catalog.MoveStore = (*InstalledExtensionRepo)(nil)

// moveEnv is a repository install on the real tables with everything that
// hangs off its id seeded, a signed catalog listing 0.1.0 and 0.2.0, and a
// repository host that fails and counts every request.
type moveEnv struct {
	db         *sqlx.DB
	id         string
	repo       *InstalledExtensionRepo
	repoExt    *RepoExtensionRepo
	policy     *InstanceExtensionPolicyRepo
	installer  *catalog.Installer
	repoSvc    *repoinstall.Service
	repoHits   *int32
	commits    map[string]string // version -> catalog commit
	digests    map[string]string // version -> artifact digest
	connection uuid.UUID
	bound      uuid.UUID
	unbound    uuid.UUID
	workspace  uuid.UUID
}

const moveRepoSHA = "4444444444444444444444444444444444444444"

func newMoveEnv(t *testing.T) *moveEnv {
	t.Helper()
	// Real Postgres via DATABASE_URL, so the `test` job runs it; skipped without one.
	db := agentDigestTestDB(t)
	ctx := context.Background()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(7 + i)
	}
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(key))
	t.Cleanup(encryption.ResetForTest)

	raw, err := os.ReadFile("../../extensions/catalog/testdata/mesh-ext-hello.json")
	require.NoError(t, err)
	var mf map[string]any
	require.NoError(t, json.Unmarshal(raw, &mf))
	id := "move-" + uuid.NewString()[:8]
	mf["id"] = id
	manifest := func(v string) []byte {
		mf["version"] = v
		b, merr := json.Marshal(mf)
		require.NoError(t, merr)
		return b
	}
	artifact := func(m []byte) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "mesh-extension.json", Mode: 0o644, Size: int64(len(m)), Typeflag: tar.TypeReg}))
		_, werr := tw.Write(m)
		require.NoError(t, werr)
		require.NoError(t, tw.Close())
		require.NoError(t, zw.Close())
		return buf.Bytes()
	}
	e := &moveEnv{db: db, id: id, commits: map[string]string{"0.1.0": "c010c010", "0.2.0": "c020c020"}, digests: map[string]string{}}
	arts := map[string][]byte{}
	for _, v := range []string{"0.1.0", "0.2.0"} {
		arts[v] = artifact(manifest(v))
		sum := sha256.Sum256(arts[v])
		e.digests[v] = hex.EncodeToString(sum[:])
	}
	caps, err := json.Marshal(mf["capabilities"])
	require.NoError(t, err)
	d1, d2 := e.digests["0.1.0"], e.digests["0.2.0"]
	body, err := json.Marshal(catalog.Catalog{Schema: 1, Extensions: []catalog.Entry{{
		ID: id, Name: "Hello", Runtime: "external", Trust: "verified",
		Latest:   catalog.Release{Version: "0.2.0", MeshAPI: ">=1 <3", CommitSHA: e.commits["0.2.0"], ArtifactSHA256: &d2, Capabilities: caps},
		Releases: []catalog.Release{{Version: "0.1.0", MeshAPI: ">=1 <3", CommitSHA: e.commits["0.1.0"], ArtifactSHA256: &d1, Capabilities: caps}},
	}}})
	require.NoError(t, err)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))
	detail := map[string]any{"id": id, "releases": []any{
		map[string]any{"version": "0.1.0", "installable": true, "status": "published"},
		map[string]any{"version": "0.2.0", "installable": true, "status": "published"},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/mesh-extensions/catalog.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(catalog.SignatureHeader, sig)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/api/v1/mesh-extensions/"+id, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(detail) })
	for v, a := range arts {
		a := a
		mux.HandleFunc("/api/v1/mesh-extensions/"+id+"/releases/"+v+"/artifact", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(a) })
	}
	spark := httptest.NewServer(mux)
	t.Cleanup(spark.Close)

	// The old repository: every request fails and is counted.
	var hits int32
	gitHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(gitHost.Close)
	e.repoHits = &hits

	e.repo, e.repoExt, e.policy = NewInstalledExtensionRepo(db), NewRepoExtensionRepo(db), NewInstanceExtensionPolicyRepo(db)
	cat := catalog.NewClient(func() string { return spark.URL }, func() string { return base64.StdEncoding.EncodeToString(pub) })
	require.NoError(t, cat.Refresh(ctx, extensions.ExtensionsAPIVersion))
	e.installer = &catalog.Installer{Catalog: cat, SparkURL: func() string { return spark.URL }, CounterOn: func() bool { return false }, Store: e.repo, Journal: e.policy}
	e.repoSvc = &repoinstall.Service{Client: &repoinstall.Client{HTTP: gitHost.Client(), AllowInsecure: true}, Repo: e.repoExt, Installed: e.repo, Policy: e.policy}

	// The repository install the partner has: 0.1.0 from GitLab, with a token.
	actor := uuid.New()
	require.NoError(t, e.repoExt.InsertRepo(ctx, &domain.InstalledExtension{
		ExtensionID: id, Version: "0.1.0", Manifest: manifest("0.1.0"), RepoURL: gitHost.URL + "/grp/hello", RepoRef: "v0.1.0",
		CommitSHA: moveRepoSHA, TrustLevel: domain.ExtensionTrustCustom, InstalledBy: &actor,
	}, "glpat-db-move-secret", &domain.InstanceExtensionJournalEntry{ActorID: &actor, ExtensionID: id, Action: domain.InstanceJournalInstalled}))

	// Everything that hangs off the id: a connection with consent, a project
	// binding (one project bound, one not), an event subscription, an actor,
	// instance settings with the disable flag, the allow-list entry.
	wsID := newDigestAgent(t, db, "mv-"+uuid.NewString()).WorkspaceID
	proj := &domain.Project{ID: uuid.New(), WorkspaceID: wsID, Name: "Bound", Slug: "mv-" + uuid.NewString()[:8], DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, NewProjectRepo(db).Create(ctx, proj))
	e.workspace, e.bound = wsID, proj.ID
	e.unbound = uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id, workspace_id, name, slug, default_assignee_type, settings, created_at, updated_at)
		VALUES ($1, $2, 'Unbound', $3, 'none', '{}', now(), now())`, e.unbound, wsID, "ub-"+uuid.NewString()[:8])
	require.NoError(t, err)
	require.NoError(t, db.GetContext(ctx, &e.connection, `
		INSERT INTO integration_configs (workspace_id, provider, config, is_active, consented_capabilities, consented_at, consented_by, project_scope)
		VALUES ($1, $2, '{"hello_url":"https://svc.example","token_ref":"keep:ext-move-key"}', true, '["api:tasks:read","events:task.created"]', now(), $3, 'bound')
		RETURNING id`, wsID, id, actor))
	_, err = db.ExecContext(ctx, `INSERT INTO project_integrations (id, project_id, type, enabled, settings, created_at, updated_at)
		VALUES ($1, $2, $3, true, '{}', now(), now())`, uuid.New(), proj.ID, id)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO extension_event_subscriptions (connection_id, event, delivery, target) VALUES ($1, 'task.created', 'webhook', 'https://svc.example/mesh/events')`, e.connection)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO extension_actors (connection_id, workspace_id, actor_id, extension_id, display_name, token_hash, capabilities)
		VALUES ($1, $2, $3, $4, 'Hello', $5, '["api:tasks:read"]')`, e.connection, wsID, uuid.New(), id, fmt.Sprintf("%064x", e.connection[:8]))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO instance_extension_settings (extension_id, settings, enabled, source) VALUES ($1, '{"region":"eu"}', false, 'admin')`, id)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO instance_extension_policy (extension_id, allowed) VALUES ($1, true)`, id)
	require.NoError(t, err)

	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM extension_actors WHERE extension_id = $1`,
			`DELETE FROM integration_configs WHERE provider = $1`,
			`DELETE FROM project_integrations WHERE type = $1`,
			`DELETE FROM instance_extension_settings WHERE extension_id = $1`,
			`DELETE FROM instance_extension_policy WHERE extension_id = $1`,
			`DELETE FROM extension_repo_credentials WHERE extension_id = $1`,
			`DELETE FROM installed_extensions WHERE extension_id = $1`,
			`DELETE FROM instance_extension_journal WHERE extension_id = $1`,
		} {
			_, _ = db.Exec(q, id)
		}
		_, _ = db.Exec(`DELETE FROM projects WHERE id = $1 OR id = $2`, e.unbound, e.bound)
	})
	return e
}

// hangingOff is every row keyed by the extension id outside installed_extensions.
func (e *moveEnv) hangingOff(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for name, q := range map[string]string{
		"connections":   `SELECT to_jsonb(t)::text FROM integration_configs t WHERE provider = $1 ORDER BY 1`,
		"bindings":      `SELECT to_jsonb(t)::text FROM project_integrations t WHERE extension_id = $1 ORDER BY 1`,
		"subscriptions": `SELECT to_jsonb(s)::text FROM extension_event_subscriptions s JOIN integration_configs c ON c.id = s.connection_id WHERE c.provider = $1 ORDER BY 1`,
		"actors":        `SELECT to_jsonb(t)::text FROM extension_actors t WHERE extension_id = $1 ORDER BY 1`,
		"settings":      `SELECT to_jsonb(t)::text FROM instance_extension_settings t WHERE extension_id = $1 ORDER BY 1`,
		"allow-list":    `SELECT to_jsonb(t)::text FROM instance_extension_policy t WHERE extension_id = $1 ORDER BY 1`,
	} {
		var rows []string
		require.NoError(t, e.db.SelectContext(context.Background(), &rows, q, e.id), name)
		require.NotEmpty(t, rows, "seeded %s must be present, or the comparison proves nothing", name)
		out[name] = rows
	}
	return out
}

func (e *moveEnv) reach(t *testing.T) (bound, unbound bool) {
	t.Helper()
	scope := NewExtensionProjectScopeRepo(e.db)
	var err error
	bound, err = scope.AllowsProject(context.Background(), e.connection, e.bound)
	require.NoError(t, err)
	unbound, err = scope.AllowsProject(context.Background(), e.connection, e.unbound)
	require.NoError(t, err)
	return bound, unbound
}

func (e *moveEnv) journal(t *testing.T, action string) []domain.InstanceExtensionJournalEntry {
	t.Helper()
	all, err := e.policy.SearchJournal(context.Background(), domain.InstanceJournalFilter{ExtensionID: e.id, Limit: 50})
	require.NoError(t, err)
	var out []domain.InstanceExtensionJournalEntry
	for _, j := range all {
		if j.Action == action {
			out = append(out, j)
		}
	}
	return out
}

func TestExtensionMoveToCatalog_KeepsEverythingOnTheID(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	before := e.hangingOff(t)
	b0, u0 := e.reach(t)
	require.True(t, b0, "positive control: the bound project is reachable")
	require.False(t, u0, "the unbound project is not")

	plan, err := e.installer.PlanMoveToCatalog(ctx, e.id, "0.1.0", "")
	require.NoError(t, err)
	assert.Equal(t, "repo", plan.Current.Source)
	assert.Equal(t, "0.1.0", plan.Target.Version, "the same version as the repository install")

	req := catalog.MoveRequest{ExpectedVersion: "0.1.0", ExpectedCommitSHA: moveRepoSHA, TargetVersion: "0.1.0", TargetCommitSHA: e.commits["0.1.0"]}
	res, err := e.installer.MoveToCatalog(ctx, uuid.New(), e.id, req)
	require.NoError(t, err)
	assert.Equal(t, catalog.OutcomeMoved, res.Outcome)

	row, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionSourceCatalog, row.Source)
	assert.Equal(t, domain.ExtensionTrustLevel("verified"), row.TrustLevel)
	assert.Equal(t, e.commits["0.1.0"], row.CommitSHA)
	assert.Equal(t, e.digests["0.1.0"], row.ArtifactSHA256)
	assert.Empty(t, row.RepoURL)
	assert.Empty(t, row.RepoRef)
	st, err := e.repoExt.GetState(ctx, e.id)
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionRepoState{}, *st, "previous / pending / skipped are cleared")
	tok, err := e.repoExt.GetToken(ctx, e.id)
	require.NoError(t, err)
	assert.Empty(t, tok, "the repository token is deleted")

	assert.Equal(t, before, e.hangingOff(t), "connections, consent, scope, bindings, subscriptions, actors, settings, disable flag, allow-list, Keep refs: unchanged")
	b1, u1 := e.reach(t)
	assert.True(t, b1)
	assert.False(t, u1, "no project outside the bindings becomes reachable")

	moved := e.journal(t, domain.InstanceJournalMovedToCatalog)
	require.Len(t, moved, 1)
	var det map[string]any
	require.NoError(t, json.Unmarshal(moved[0].Details, &det))
	assert.Equal(t, true, det["repository_token_deleted"])
	assert.NotContains(t, string(moved[0].Details), "glpat-db-move-secret")

	// One way: the repository state was not kept, so a catalog rollback right
	// after the move has nothing to go back to and changes nothing.
	movedRow, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	_, err = e.installer.Rollback(ctx, nil, e.id)
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, catalog.CodeRollbackUnavailable, ae.ErrorCode)
	stillMoved, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	assert.Equal(t, movedRow, stillMoved)

	// The retry after a lost answer: nothing more is written.
	again, err := e.installer.MoveToCatalog(ctx, uuid.New(), e.id, req)
	require.NoError(t, err)
	assert.Equal(t, catalog.OutcomeAlreadyMoved, again.Outcome)
	assert.Len(t, e.journal(t, domain.InstanceJournalMovedToCatalog), 1)

	// From now on the id lives on the catalog path; the old repository is never asked.
	atomic.StoreInt32(e.repoHits, 0)
	_, err = e.repoSvc.Update(ctx, nil, e.id, "")
	require.Error(t, err, "the repository path no longer owns the id")
	up, err := e.installer.Update(ctx, nil, e.id, "")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, up.Outcome)
	assert.Equal(t, "0.2.0", up.Version)
	back, err := e.installer.Rollback(ctx, nil, e.id)
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", back.Version)
	assert.Equal(t, domain.ExtensionSourceCatalog, back.Source)
	assert.Zero(t, atomic.LoadInt32(e.repoHits), "update and rollback never called the old repository")
	assert.Equal(t, before, e.hangingOff(t))
}

// A stale plan, a wrong release or a token change race leaves the row, the
// token and everything else alone.
// A repository install blocked at its old version is moved to a catalog
// release the move checked as not blocked: the moved row is enabled, not left
// disabled by the stale block until a later catalog refresh.
func TestExtensionMoveToCatalog_ClearsStaleBlock(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	changed, err := e.repo.SetBlocked(ctx, e.id, true, "the repository release was blocked")
	require.NoError(t, err)
	require.True(t, changed)
	before, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	require.True(t, before.Blocked, "positive control: the repository row is blocked")

	req := catalog.MoveRequest{ExpectedVersion: "0.1.0", ExpectedCommitSHA: moveRepoSHA, TargetVersion: "0.1.0", TargetCommitSHA: e.commits["0.1.0"]}
	res, err := e.installer.MoveToCatalog(ctx, uuid.New(), e.id, req)
	require.NoError(t, err)
	assert.Equal(t, catalog.OutcomeMoved, res.Outcome)

	row, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionSourceCatalog, row.Source)
	assert.False(t, row.Blocked, "the stale block of the repository release is cleared")
	assert.Empty(t, row.BlockedReason)
}

func TestExtensionMoveToCatalog_RefusalsChangeNothing(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	rowBefore, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	before := e.hangingOff(t)
	for name, req := range map[string]catalog.MoveRequest{
		"stale version":    {ExpectedVersion: "0.0.9", ExpectedCommitSHA: moveRepoSHA, TargetVersion: "0.1.0", TargetCommitSHA: e.commits["0.1.0"]},
		"stale commit":     {ExpectedVersion: "0.1.0", ExpectedCommitSHA: strings.Repeat("5", 40), TargetVersion: "0.1.0", TargetCommitSHA: e.commits["0.1.0"]},
		"wrong target sha": {ExpectedVersion: "0.1.0", ExpectedCommitSHA: moveRepoSHA, TargetVersion: "0.1.0", TargetCommitSHA: e.commits["0.2.0"]},
		"unknown version":  {ExpectedVersion: "0.1.0", ExpectedCommitSHA: moveRepoSHA, TargetVersion: "0.9.0", TargetCommitSHA: "x"},
	} {
		_, moveErr := e.installer.MoveToCatalog(ctx, uuid.New(), e.id, req)
		assert.Error(t, moveErr, name)
	}
	// The store's own guard: a write against a row that changed in between.
	err = e.repo.MoveRepoToCatalog(ctx, &domain.InstalledExtension{ExtensionID: e.id, Version: "0.1.0", Manifest: rowBefore.Manifest, CommitSHA: "c", ArtifactSHA256: "d", TrustLevel: "verified"},
		domain.ExtensionStateGuard{Source: domain.ExtensionSourceRepo, Version: "0.1.0", CommitSHA: strings.Repeat("6", 40)}, nil)
	require.Error(t, err)

	rowAfter, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	assert.Equal(t, rowBefore, rowAfter)
	tok, err := e.repoExt.GetToken(ctx, e.id)
	require.NoError(t, err)
	assert.Equal(t, "glpat-db-move-secret", tok)
	assert.Equal(t, before, e.hangingOff(t))
	assert.Empty(t, e.journal(t, domain.InstanceJournalMovedToCatalog))
}

// Re-pin on the real tables: guarded, token replaced encrypted, journal
// without the token, and previous_* kept only on the same URL.
func TestExtensionRepin_DB(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	before := e.hangingOff(t)
	row, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	next := &domain.InstalledExtension{ExtensionID: e.id, RepoURL: row.RepoURL, RepoRef: strings.Repeat("7", 40), CommitSHA: strings.Repeat("7", 40), Version: "0.1.1", Manifest: row.Manifest}
	guard := domain.ExtensionStateGuard{Source: domain.ExtensionSourceRepo, Version: row.Version, CommitSHA: row.CommitSHA}
	entry := &domain.InstanceExtensionJournalEntry{ExtensionID: e.id, Action: domain.InstanceJournalRepinned, Details: []byte(`{"token":"replace"}`)}

	stale := guard
	stale.CommitSHA = strings.Repeat("8", 40)
	require.Error(t, e.repoExt.Repin(ctx, next, stale, domain.RepoTokenReplace, "glpat-new", entry))
	tok, _ := e.repoExt.GetToken(ctx, e.id)
	assert.Equal(t, "glpat-db-move-secret", tok, "a refused re-pin keeps the token")

	require.NoError(t, e.repoExt.Repin(ctx, next, guard, domain.RepoTokenReplace, "glpat-new", entry))
	after, err := e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	assert.Equal(t, next.RepoRef, after.RepoRef)
	assert.Equal(t, "0.1.0", after.PreviousVersion, "same URL keeps the old pin for rollback")
	tok, _ = e.repoExt.GetToken(ctx, e.id)
	assert.Equal(t, "glpat-new", tok)
	var stored string
	require.NoError(t, e.db.GetContext(ctx, &stored, `SELECT token_enc FROM extension_repo_credentials WHERE extension_id = $1`, e.id))
	assert.NotContains(t, stored, "glpat-new", "stored encrypted")
	require.Len(t, e.journal(t, domain.InstanceJournalRepinned), 1)

	// Another URL: previous_* cleared, token removed.
	next2 := *next
	next2.RepoURL, next2.CommitSHA, next2.RepoRef = "https://gitlab.example/grp/hello", strings.Repeat("9", 40), strings.Repeat("9", 40)
	require.NoError(t, e.repoExt.Repin(ctx, &next2, domain.ExtensionStateGuard{Source: domain.ExtensionSourceRepo, Version: "0.1.1", CommitSHA: next.CommitSHA}, domain.RepoTokenRemove, "", nil))
	after, err = e.repo.Get(ctx, e.id)
	require.NoError(t, err)
	assert.Empty(t, after.PreviousVersion)
	tok, _ = e.repoExt.GetToken(ctx, e.id)
	assert.Empty(t, tok)
	assert.Equal(t, before, e.hangingOff(t))
}
