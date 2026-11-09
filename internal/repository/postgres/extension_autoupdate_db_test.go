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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/catalog"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
)

// The scheduled auto-update, end to end on the real tables: a signed catalog
// served over HTTP, a source=catalog row in installed_extensions at an older
// release, the same repoinstall.Service wiring cmd/api uses (RunAutoUpdate with
// the catalog pass hooked in), and the real policy repo holding auto_update.
//
//	RED     the old wiring (no catalog pass): auto_update=true, row stays old.
//	CONTROL the new wiring with auto_update=false: row stays old.
//	GREEN   the new wiring with auto_update=true: the row moves old -> new,
//	        with the snapshot, the digest and the journal entry.
type autoUpdateEnv struct {
	id       string
	digest   string
	repo     *InstalledExtensionRepo
	policy   *InstanceExtensionPolicyRepo
	catalog  *catalog.Client
	installr *catalog.Installer
}

func newAutoUpdateEnv(t *testing.T, signWith ed25519.PrivateKey, pub ed25519.PublicKey) *autoUpdateEnv {
	t.Helper()
	db := testDB(t)
	ctx := context.Background()

	raw, err := os.ReadFile("../../extensions/catalog/testdata/mesh-ext-hello.json")
	require.NoError(t, err)
	var mf map[string]any
	require.NoError(t, json.Unmarshal(raw, &mf))
	id := "autoupd-" + uuid.NewString()[:8]
	mf["id"] = id

	withVersion := func(v string) []byte {
		mf["version"] = v
		b, merr := json.Marshal(mf)
		require.NoError(t, merr)
		return b
	}
	oldManifest, newManifest := withVersion("0.0.9"), withVersion("0.1.0")

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "mesh-extension.json", Mode: 0o644, Size: int64(len(newManifest)), Typeflag: tar.TypeReg}))
	_, err = tw.Write(newManifest)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	artifact := buf.Bytes()
	sum := sha256.Sum256(artifact)
	digest := hex.EncodeToString(sum[:])

	caps, err := json.Marshal(mf["capabilities"])
	require.NoError(t, err)
	body, err := json.Marshal(catalog.Catalog{Schema: 1, Extensions: []catalog.Entry{{
		ID: id, Name: "Hello", Runtime: "external", Trust: "verified",
		Latest: catalog.Release{Version: "0.1.0", MeshAPI: ">=1 <3", CommitSHA: "newcommit", ArtifactSHA256: &digest, Capabilities: caps},
	}}})
	require.NoError(t, err)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(signWith, body))
	detail := map[string]any{"id": id, "releases": []any{map[string]any{"version": "0.1.0", "manifest": json.RawMessage(newManifest), "installable": true, "status": "published"}}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/mesh-extensions/catalog.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(catalog.SignatureHeader, sig)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/api/v1/mesh-extensions/"+id, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(detail) })
	mux.HandleFunc("/api/v1/mesh-extensions/"+id+"/releases/0.1.0/artifact", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(artifact) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	e := &autoUpdateEnv{id: id, digest: digest, repo: NewInstalledExtensionRepo(db), policy: NewInstanceExtensionPolicyRepo(db)}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	e.catalog = catalog.NewClient(func() string { return srv.URL }, func() string { return pubB64 })
	e.installr = &catalog.Installer{Catalog: e.catalog, SparkURL: func() string { return srv.URL }, CounterOn: func() bool { return false },
		Store: e.repo, Journal: e.policy}

	prev, err := e.policy.GetConfig(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.policy.SetAutoUpdate(context.Background(), prev.AutoUpdate, nil)
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, id)
	})
	require.NoError(t, e.repo.InsertCatalog(ctx, &domain.InstalledExtension{ExtensionID: id, Version: "0.0.9", Manifest: oldManifest,
		CommitSHA: "oldcommit", TrustLevel: domain.ExtensionTrustLevel("verified")}))
	return e
}

// service is the production wiring of cmd/api; withCatalog=false is the wiring
// before the fix (only the repo pass).
func (e *autoUpdateEnv) service(withCatalog bool) *repoinstall.Service {
	s := &repoinstall.Service{Installed: e.repo, Policy: e.policy}
	if withCatalog {
		s.CatalogUpdate = func(ctx context.Context) ([]repoinstall.UpdateResult, error) { return e.installr.UpdateAll(ctx, nil) }
	}
	return s
}

// runTimer starts the real RunAutoUpdate loop and lets it tick a few times.
func runTimer(s *repoinstall.Service, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	s.RunAutoUpdate(ctx, 25*time.Millisecond)
}

func (e *autoUpdateEnv) row(t *testing.T) *domain.InstalledExtension {
	t.Helper()
	r, err := e.repo.Get(context.Background(), e.id)
	require.NoError(t, err)
	require.NotNil(t, r)
	return r
}

func TestExtensionAutoUpdate_TimerPathMovesCatalogRow(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ctx := context.Background()
	e := newAutoUpdateEnv(t, priv, pub)
	require.NoError(t, e.catalog.Refresh(ctx, extensions.ExtensionsAPIVersion))

	before := e.row(t)
	t.Logf("BEFORE version=%s commit=%s source=%s", before.Version, before.CommitSHA, before.Source)
	require.Equal(t, "0.0.9", before.Version)

	// RED: auto_update on, but the timer has no catalog pass (the old wiring).
	require.NoError(t, e.policy.SetAutoUpdate(ctx, true, nil))
	runTimer(e.service(false), 200*time.Millisecond)
	red := e.row(t)
	t.Logf("RED (old wiring, auto_update=true) version=%s commit=%s", red.Version, red.CommitSHA)
	assert.Equal(t, "0.0.9", red.Version, "without the catalog pass the row is never updated: this is the defect")

	// CONTROL: the fixed wiring, auto_update=false: nothing moves.
	require.NoError(t, e.policy.SetAutoUpdate(ctx, false, nil))
	runTimer(e.service(true), 200*time.Millisecond)
	ctl := e.row(t)
	t.Logf("CONTROL (fixed wiring, auto_update=false) version=%s commit=%s", ctl.Version, ctl.CommitSHA)
	assert.Equal(t, "0.0.9", ctl.Version, "auto_update=false updates nothing")
	assert.Equal(t, "oldcommit", ctl.CommitSHA)

	// GREEN: the fixed wiring, auto_update=true.
	require.NoError(t, e.policy.SetAutoUpdate(ctx, true, nil))
	runTimer(e.service(true), 400*time.Millisecond)
	after := e.row(t)
	t.Logf("AFTER (fixed wiring, auto_update=true) version=%s previous=%s commit=%s artifact=%s", after.Version, after.PreviousVersion, after.CommitSHA, after.ArtifactSHA256)
	assert.Equal(t, "0.1.0", after.Version)
	assert.Equal(t, "0.0.9", after.PreviousVersion)
	assert.Equal(t, "newcommit", after.CommitSHA)
	assert.Equal(t, e.digest, after.ArtifactSHA256, "stored digest must equal the catalog fixture digest")
	assert.Equal(t, domain.ExtensionSourceCatalog, after.Source)

	// The release it left is sealed for rollback, and the journal says updated.
	snap, err := e.repo.GetCatalogSnapshot(ctx, e.id)
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, "0.0.9", snap.Version)
	entries, err := e.policy.SearchJournal(ctx, domain.InstanceJournalFilter{ExtensionID: e.id, Limit: 10})
	require.NoError(t, err)
	var updated int
	for _, j := range entries {
		if j.Action == domain.InstanceJournalUpdated {
			updated++
		}
	}
	assert.Equal(t, 1, updated, "later cycles find nothing newer and write nothing more")
	assert.Len(t, entries, 1, "the fixture extension has exactly one journal entry in total: RED and CONTROL cycles wrote none")
}

// A catalog signed by a key the instance does not trust is never accepted, so
// the timer cannot move the row to it.
func TestExtensionAutoUpdate_UntrustedCatalogMovesNothing(t *testing.T) {
	_, attackerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trustedPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ctx := context.Background()
	e := newAutoUpdateEnv(t, attackerPriv, trustedPub)
	require.Error(t, e.catalog.Refresh(ctx, extensions.ExtensionsAPIVersion))

	require.NoError(t, e.policy.SetAutoUpdate(ctx, true, nil))
	runTimer(e.service(true), 200*time.Millisecond)
	assert.Equal(t, "0.0.9", e.row(t).Version)
	assert.Equal(t, "oldcommit", e.row(t).CommitSHA)
}
