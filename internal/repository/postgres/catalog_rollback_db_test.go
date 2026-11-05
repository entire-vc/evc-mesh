//go:build integration

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
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// sparkFixture is a signed fake Spark: catalog.json, the release list and the
// artifacts of two releases of one extension, A (0.1.13) and B (0.1.20).
type sparkFixture struct {
	t              *testing.T
	id             string
	srv            *httptest.Server
	priv           ed25519.PrivateKey
	mu             sync.Mutex
	latest         string
	blocklist      []map[string]string
	yanked         map[string]bool
	manifests      map[string][]byte
	artifacts      map[string][]byte
	digests        map[string]string
	versions       []string
	beforeArtifact func()
	client         *catalog.Client
}

func newSparkFixture(t *testing.T, versions ...string) *sparkFixture {
	t.Helper()
	if len(versions) == 0 {
		versions = []string{"0.1.13", "0.1.20"}
	}
	raw, err := os.ReadFile("../../extensions/catalog/testdata/mesh-ext-hello.json")
	require.NoError(t, err)
	var base map[string]any
	require.NoError(t, json.Unmarshal(raw, &base))
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	f := &sparkFixture{t: t, id: base["id"].(string), priv: priv, latest: versions[0], versions: versions, yanked: map[string]bool{},
		manifests: map[string][]byte{}, artifacts: map[string][]byte{}, digests: map[string]string{}}
	for _, v := range versions {
		base["version"] = v
		m, err := json.Marshal(base)
		require.NoError(t, err)
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "mesh-extension.json", Mode: 0o644, Size: int64(len(m)), Typeflag: tar.TypeReg}))
		_, err = tw.Write(m)
		require.NoError(t, err)
		require.NoError(t, tw.Close())
		require.NoError(t, zw.Close())
		sum := sha256.Sum256(buf.Bytes())
		f.manifests[v], f.artifacts[v], f.digests[v] = m, buf.Bytes(), hex.EncodeToString(sum[:])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/mesh-extensions/catalog.json", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		caps := base["capabilities"]
		release := func(v string) map[string]any {
			return map[string]any{"version": v, "mesh_api": ">=1 <3", "commit_sha": fixtureCommit(v), "artifact_sha256": f.digests[v], "capabilities": caps}
		}
		var releases []any
		for _, v := range f.versions {
			releases = append(releases, release(v))
		}
		body, _ := json.Marshal(map[string]any{"schema": 1, "generated_at": "2026-10-31T00:00:00Z", "blocklist": f.blocklist,
			"extensions": []any{map[string]any{"id": f.id, "name": "Hello", "runtime": "external", "trust": "verified",
				"latest": release(f.latest), "releases": releases}}})
		w.Header().Set(catalog.SignatureHeader, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body)))
		w.Header().Set("ETag", catalog.ETagOf(body))
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/api/v1/mesh-extensions/"+f.id, func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var rels []any
		for _, v := range f.versions {
			rels = append(rels, map[string]any{"version": v, "installable": true, "yanked": f.yanked[v]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.id, "releases": rels})
	})
	for v := range f.artifacts {
		art := f.artifacts[v]
		mux.HandleFunc("/api/v1/mesh-extensions/"+f.id+"/releases/"+v+"/artifact", func(w http.ResponseWriter, _ *http.Request) {
			if f.beforeArtifact != nil {
				f.beforeArtifact()
			}
			_, _ = w.Write(art)
		})
	}
	mux.HandleFunc("/api/v1/mesh-extensions/"+f.id+"/installs", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	cl := catalog.NewClient(func() string { return f.srv.URL }, func() string { return base64.StdEncoding.EncodeToString(pub) })
	f.client = cl
	return f
}

func fixtureCommit(version string) string {
	sum := sha256.Sum256([]byte("commit-" + version))
	return hex.EncodeToString(sum[:])[:40]
}

func (f *sparkFixture) setLatest(v string) {
	f.mu.Lock()
	f.latest = v
	f.mu.Unlock()
	require.NoError(f.t, f.client.Refresh(context.Background(), extensions.ExtensionsAPIVersion))
}

func (f *sparkFixture) setBlocklist(b []map[string]string) {
	f.mu.Lock()
	f.blocklist = b
	f.mu.Unlock()
	require.NoError(f.t, f.client.Refresh(context.Background(), extensions.ExtensionsAPIVersion))
}

type rollbackEnv struct {
	t     *testing.T
	f     *sparkFixture
	repo  *InstalledExtensionRepo
	inst  *catalog.Installer
	actor uuid.UUID
}

// newRollbackEnv installs A from the signed fake Spark and updates to B.
func newRollbackEnv(t *testing.T) (*rollbackEnv, domain.InstalledExtension) {
	t.Helper()
	db := testDB(t)
	f := newSparkFixture(t)
	e := &rollbackEnv{t: t, f: f, repo: NewInstalledExtensionRepo(db), actor: uuid.New()}
	e.inst = &catalog.Installer{
		Catalog: f.client, SparkURL: func() string { return f.srv.URL }, CounterOn: func() bool { return false },
		Store: e.repo, Journal: NewInstanceExtensionPolicyRepo(db),
	}
	clean := func() {
		db.Exec(`DELETE FROM installed_extensions WHERE extension_id IN ($1, 'rb-other-repo', 'rb-other-builtin', 'rb-other-catalog')`, f.id)
		db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, f.id)
	}
	clean()
	t.Cleanup(clean)

	f.setLatest("0.1.13")
	ctx := context.Background()
	_, err := e.inst.Install(ctx, e.actor, f.id)
	require.NoError(t, err)
	a, err := e.repo.Get(ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, "0.1.13", a.Version)

	f.setLatest("0.1.20")
	res, err := e.inst.Update(ctx, &e.actor, f.id, "")
	require.NoError(t, err)
	require.Equal(t, "updated", string(res.Outcome))
	return e, *a
}

func (e *rollbackEnv) row() domain.InstalledExtension {
	e.t.Helper()
	r, err := e.repo.Get(context.Background(), e.f.id)
	require.NoError(e.t, err)
	require.NotNil(e.t, r)
	return *r
}

func (e *rollbackEnv) rollbackErr() *apierror.Error {
	e.t.Helper()
	_, err := e.inst.Rollback(context.Background(), &e.actor, e.f.id)
	var ae *apierror.Error
	require.ErrorAs(e.t, err, &ae)
	return ae
}

func sameTime(r domain.InstalledExtension) domain.InstalledExtension {
	r.CreatedAt, r.UpdatedAt = r.CreatedAt.UTC(), r.UpdatedAt.UTC()
	return r
}

func TestCatalogRollback_UpdateThenRollbackRestoresA(t *testing.T) {
	e, a := newRollbackEnv(t)
	ctx := context.Background()
	db := testDB(t)

	// rows that are not ours: a repo row, a builtin row, another catalog row
	for _, q := range []string{
		`INSERT INTO installed_extensions (extension_id, version, manifest, source, repo_url, trust_level) VALUES ('rb-other-repo', '1.0.0', '{"id":"rb-other-repo"}', 'repo', 'https://example.test/r.git', 'custom')`,
		`INSERT INTO installed_extensions (extension_id, version, manifest, source, trust_level) VALUES ('rb-other-builtin', '2.0.0', '{"id":"rb-other-builtin"}', 'builtin', 'builtin')`,
		`INSERT INTO installed_extensions (extension_id, version, manifest, source, commit_sha, trust_level) VALUES ('rb-other-catalog', '3.0.0', '{"id":"rb-other-catalog"}', 'catalog', 'c3', 'verified')`,
	} {
		_, err := db.Exec(q)
		require.NoError(t, err)
	}
	others, err := e.repo.List(ctx)
	require.NoError(t, err)
	var othersBefore []domain.InstalledExtension
	for _, r := range others {
		if r.ExtensionID != e.f.id {
			othersBefore = append(othersBefore, r)
		}
	}
	require.GreaterOrEqual(t, len(othersBefore), 3)

	b := e.row()
	require.Equal(t, "0.1.20", b.Version)
	require.NotEqual(t, a.CommitSHA, b.CommitSHA)
	require.NotEqual(t, a.ArtifactSHA256, b.ArtifactSHA256)
	require.NotEqual(t, string(a.Manifest), string(b.Manifest))

	updates, err := e.inst.Updates(ctx)
	require.NoError(t, err)
	var info *struct{ can bool }
	for _, u := range updates {
		if u.ExtensionID == e.f.id {
			info = &struct{ can bool }{u.CanRollback}
		}
	}
	require.NotNil(t, info)
	assert.True(t, info.can, "can_rollback is true only with an intact snapshot")

	got, err := e.inst.Rollback(ctx, &e.actor, e.f.id)
	require.NoError(t, err)
	assert.Equal(t, a.Version, got.Version)
	assert.JSONEq(t, string(a.Manifest), string(got.Manifest))
	assert.Equal(t, a.CommitSHA, got.CommitSHA)
	assert.Equal(t, a.ArtifactSHA256, got.ArtifactSHA256)
	assert.Equal(t, a.Source, got.Source)
	assert.Equal(t, a.TrustLevel, got.TrustLevel)
	assert.Equal(t, a.InstalledBy, got.InstalledBy)
	assert.Empty(t, got.PreviousVersion)
	snap, err := e.repo.GetCatalogSnapshot(ctx, e.f.id)
	require.NoError(t, err)
	assert.Nil(t, snap, "the snapshot is consumed")

	// the registry receipt is written with the rollback
	var action string
	var details []byte
	require.NoError(t, db.QueryRow(`SELECT action, details FROM instance_extension_journal WHERE extension_id = $1 AND action = 'rolled_back'`, e.f.id).Scan(&action, &details))
	var d map[string]any
	require.NoError(t, json.Unmarshal(details, &d))
	assert.Equal(t, "0.1.13", d["to"])
	assert.Equal(t, "0.1.20", d["from"])
	assert.Equal(t, a.ArtifactSHA256, d["artifact_sha256"])

	// repeat: 409, nothing changes
	again := e.rollbackErr()
	assert.Equal(t, http.StatusConflict, again.Code)
	assert.Equal(t, catalog.CodeRollbackUnavailable, again.ErrorCode)

	// bulk update does not take B back; an explicit update still can
	res, err := e.inst.UpdateAll(ctx, &e.actor)
	require.NoError(t, err)
	for _, r := range res {
		if r.ExtensionID == e.f.id {
			assert.Equal(t, "up_to_date", string(r.Outcome))
		}
	}
	assert.Equal(t, "0.1.13", e.row().Version)
	r2, err := e.inst.Update(ctx, &e.actor, e.f.id, "")
	require.NoError(t, err)
	assert.Equal(t, "updated", string(r2.Outcome))
	assert.Equal(t, "0.1.20", e.row().Version)

	// untouched: every other row, byte for byte
	after, err := e.repo.List(ctx)
	require.NoError(t, err)
	var othersAfter []domain.InstalledExtension
	for _, r := range after {
		if r.ExtensionID != e.f.id {
			othersAfter = append(othersAfter, r)
		}
	}
	require.Len(t, othersAfter, len(othersBefore))
	for i := range othersBefore {
		assert.Equal(t, sameTime(othersBefore[i]), sameTime(othersAfter[i]))
	}
}

func TestCatalogRollback_RefusalsMutateNothing(t *testing.T) {
	cases := map[string]struct {
		status int
		code   string
		break_ func(e *rollbackEnv)
	}{
		"legacy previous_version, no snapshot": {409, catalog.CodeRollbackUnavailable, func(e *rollbackEnv) {
			e.exec(`UPDATE installed_extensions SET previous_snapshot = NULL WHERE extension_id = $1`)
		}},
		"corrupt snapshot": {409, catalog.CodeSnapshotCorrupt, func(e *rollbackEnv) {
			e.exec(`UPDATE installed_extensions SET previous_snapshot = jsonb_set(previous_snapshot, '{commit_sha}', '"tampered"') WHERE extension_id = $1`)
		}},
		"unreadable snapshot": {409, catalog.CodeSnapshotCorrupt, func(e *rollbackEnv) {
			e.exec(`UPDATE installed_extensions SET previous_snapshot = '"not an object"' WHERE extension_id = $1`)
		}},
		"stale snapshot": {409, catalog.CodeRollbackStale, func(e *rollbackEnv) {
			e.exec(`UPDATE installed_extensions SET commit_sha = 'moved-on' WHERE extension_id = $1`)
		}},
		"blocked row": {409, catalog.CodeBlocked, func(e *rollbackEnv) {
			e.exec(`UPDATE installed_extensions SET blocked = true, blocked_reason = 'x' WHERE extension_id = $1`)
		}},
		"revoked: blocklisted": {409, catalog.CodeBlocked, func(e *rollbackEnv) {
			e.f.setBlocklist([]map[string]string{{"id": e.f.id, "version": "0.1.13", "reason": "bad release"}})
		}},
		"revoked: yanked in Spark": {409, catalog.CodeReleaseRevoked, func(e *rollbackEnv) {
			e.f.mu.Lock()
			e.f.yanked["0.1.13"] = true
			e.f.mu.Unlock()
		}},
		"incompatible with this Mesh": {422, "extension_incompatible", func(e *rollbackEnv) {
			e.exec(`UPDATE installed_extensions SET previous_snapshot = jsonb_set(previous_snapshot, '{manifest,mesh_api}', '">=9"') WHERE extension_id = $1`)
			e.resealSnapshot()
		}},
		"would add permissions (consent)": {409, "consent_required", func(e *rollbackEnv) {
			e.exec(`UPDATE installed_extensions SET manifest = jsonb_set(manifest, '{capabilities}', '{}') WHERE extension_id = $1`)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, _ := newRollbackEnv(t)
			tc.break_(e)
			before := sameTime(e.row())
			snapBefore := e.rawSnapshot()
			ae := e.rollbackErr()
			assert.Equal(t, tc.status, ae.Code)
			assert.Equal(t, tc.code, ae.ErrorCode)
			assert.Equal(t, before, sameTime(e.row()), "a refused rollback mutates nothing")
			assert.Equal(t, snapBefore, e.rawSnapshot())
			var n int
			require.NoError(t, testDB(t).QueryRow(`SELECT count(*) FROM instance_extension_journal WHERE extension_id = $1 AND action = 'rolled_back'`, e.f.id).Scan(&n))
			assert.Zero(t, n, "no receipt for a refused rollback")
		})
	}
}

func (e *rollbackEnv) exec(q string) {
	e.t.Helper()
	_, err := testDB(e.t).Exec(q, e.f.id)
	require.NoError(e.t, err)
}

func (e *rollbackEnv) rawSnapshot() string {
	var s *string
	require.NoError(e.t, testDB(e.t).QueryRow(`SELECT previous_snapshot::text FROM installed_extensions WHERE extension_id = $1`, e.f.id).Scan(&s))
	if s == nil {
		return ""
	}
	return *s
}

// resealSnapshot recomputes the digest after a test edited the snapshot body,
// so the test reaches the check it names instead of the integrity one.
func (e *rollbackEnv) resealSnapshot() {
	snap, err := e.repo.GetCatalogSnapshot(context.Background(), e.f.id)
	require.NoError(e.t, err)
	snap.Seal()
	raw, err := json.Marshal(snap)
	require.NoError(e.t, err)
	_, err = testDB(e.t).Exec(`UPDATE installed_extensions SET previous_snapshot = $2 WHERE extension_id = $1`, e.f.id, raw)
	require.NoError(e.t, err)
}

// Two rollbacks racing: exactly one wins, the other is a 409.
func TestCatalogRollback_ConcurrentRollbackAppliesOnce(t *testing.T) {
	e, a := newRollbackEnv(t)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = e.inst.Rollback(context.Background(), &e.actor, e.f.id)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		var ae *apierror.Error
		require.ErrorAs(t, err, &ae)
		assert.Equal(t, http.StatusConflict, ae.Code)
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, a.Version, e.row().Version)
	var n int
	require.NoError(t, testDB(t).QueryRow(`SELECT count(*) FROM instance_extension_journal WHERE extension_id = $1 AND action = 'rolled_back'`, e.f.id).Scan(&n))
	assert.Equal(t, 1, n, "one receipt")
}

// The guard in the UPDATE itself: a row that moved on after the service read it
// is not rolled back.
func TestCatalogRollback_GuardRejectsMovedRow(t *testing.T) {
	e, _ := newRollbackEnv(t)
	ctx := context.Background()
	snap, err := e.repo.GetCatalogSnapshot(ctx, e.f.id)
	require.NoError(t, err)
	row := e.row()
	g := domain.CatalogRollbackGuard{Version: row.Version, CommitSHA: "someone-else", SnapshotDigest: snap.Digest}
	err = e.repo.RollbackCatalog(ctx, e.f.id, g, *snap, nil)
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusConflict, ae.Code)
	assert.Equal(t, sameTime(row), sameTime(e.row()))
}

// The race the review named: an unattended update read B's predecessor state,
// a rollback committed, then the update writes. The guard refuses it.
func TestCatalogRollback_StaleUpdateCannotReapplyRolledBackRelease(t *testing.T) {
	e, a := newRollbackEnv(t)
	ctx := context.Background()
	b := e.row()
	_, err := e.inst.Rollback(ctx, &e.actor, e.f.id)
	require.NoError(t, err)

	upd := &domain.InstalledExtension{ExtensionID: e.f.id, Version: b.Version, Manifest: b.Manifest, CommitSHA: b.CommitSHA, ArtifactSHA256: b.ArtifactSHA256, TrustLevel: b.TrustLevel}
	// read before the rollback: the row was B then
	stale := domain.CatalogUpdateGuard{FromVersion: b.Version, FromCommitSHA: b.CommitSHA, Auto: true}
	var ae *apierror.Error
	require.ErrorAs(t, e.repo.UpdateCatalog(ctx, upd, stale), &ae)
	assert.Equal(t, http.StatusConflict, ae.Code)
	// read after the rollback, but the unattended pass must still skip B
	fresh := domain.CatalogUpdateGuard{FromVersion: a.Version, FromCommitSHA: a.CommitSHA, Auto: true}
	require.ErrorAs(t, e.repo.UpdateCatalog(ctx, upd, fresh), &ae)
	assert.Equal(t, http.StatusConflict, ae.Code)
	assert.Equal(t, a.Version, e.row().Version)
	// an explicit update from the current row still works
	fresh.Auto = false
	require.NoError(t, e.repo.UpdateCatalog(ctx, upd, fresh))
	assert.Equal(t, b.Version, e.row().Version)
}

func newPinnedUpdateEnv(t *testing.T) *rollbackEnv {
	t.Helper()
	db := testDB(t)
	f := newSparkFixture(t, "0.2.3", "0.3.0", "0.3.9")
	e := &rollbackEnv{t: t, f: f, repo: NewInstalledExtensionRepo(db), actor: uuid.New()}
	e.inst = &catalog.Installer{Catalog: f.client, SparkURL: func() string { return f.srv.URL }, CounterOn: func() bool { return false },
		Store: e.repo, Journal: NewInstanceExtensionPolicyRepo(db)}
	clean := func() {
		db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, f.id)
		db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, f.id)
	}
	clean()
	t.Cleanup(clean)
	f.setLatest("0.2.3")
	_, err := e.inst.Install(context.Background(), e.actor, f.id)
	require.NoError(t, err)
	f.setLatest("0.3.9")
	return e
}

func TestCatalogUpdateTarget_PinnedSequenceCreatesRestorableSnapshot(t *testing.T) {
	e := newPinnedUpdateEnv(t)
	ctx := context.Background()
	changes := 0
	e.inst.OnChange = func(context.Context) { changes++ }
	for _, version := range []string{"0.3.0", "0.3.9"} {
		before := e.row()
		res, err := e.inst.UpdateTarget(ctx, &e.actor, e.f.id, "", version, fixtureCommit(version))
		require.NoError(t, err)
		require.Equal(t, "updated", string(res.Outcome))
		require.Equal(t, version, res.Version)
		require.Equal(t, fixtureCommit(version), e.row().CommitSHA)
		snap, err := e.repo.GetCatalogSnapshot(ctx, e.f.id)
		require.NoError(t, err)
		require.NotNil(t, snap)
		assert.True(t, snap.Intact())
		assert.Equal(t, before.Version, snap.Version)
		assert.JSONEq(t, string(before.Manifest), string(snap.Manifest))
		assert.Equal(t, before.CommitSHA, snap.CommitSHA)
		assert.Equal(t, before.ArtifactSHA256, snap.ArtifactSHA256)
		assert.Equal(t, string(before.TrustLevel), snap.TrustLevel)
		assert.Equal(t, string(before.Source), snap.Source)
	}
	assert.Equal(t, 2, changes)
	assert.Equal(t, "0.3.0", e.row().PreviousVersion)
	cat, err := e.f.client.Current()
	require.NoError(t, err)
	entry, ok := cat.Entry(e.f.id)
	require.True(t, ok)
	assert.Equal(t, "0.3.9", entry.Latest.Version, "target selection never rewrites the verified cache")
	got, err := e.inst.Rollback(ctx, &e.actor, e.f.id)
	require.NoError(t, err)
	assert.Equal(t, "0.3.0", got.Version)
	assert.Equal(t, fixtureCommit("0.3.0"), got.CommitSHA)
	assert.Equal(t, e.f.digests["0.3.0"], got.ArtifactSHA256)
}

func TestCatalogUpdateTarget_ConcurrentRowChangePreservesCAS(t *testing.T) {
	e := newPinnedUpdateEnv(t)
	ctx := context.Background()
	db := testDB(t)
	changes := 0
	e.inst.OnChange = func(context.Context) { changes++ }
	var concurrentErr error
	e.f.beforeArtifact = func() {
		_, concurrentErr = db.Exec(`UPDATE installed_extensions SET commit_sha = 'concurrent-writer' WHERE extension_id = $1`, e.f.id)
	}
	_, err := e.inst.UpdateTarget(ctx, &e.actor, e.f.id, "", "0.3.0", fixtureCommit("0.3.0"))
	require.NoError(t, concurrentErr)
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusConflict, ae.Code)
	assert.Equal(t, "0.2.3", e.row().Version)
	assert.Equal(t, "concurrent-writer", e.row().CommitSHA)
	snap, err := e.repo.GetCatalogSnapshot(ctx, e.f.id)
	require.NoError(t, err)
	assert.Nil(t, snap)
	var updates int
	require.NoError(t, db.Get(&updates, `SELECT count(*) FROM instance_extension_journal WHERE extension_id = $1 AND action = 'updated'`, e.f.id))
	assert.Zero(t, updates)
	assert.Zero(t, changes)
}
