package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type installMem struct {
	rows map[string]*domain.InstalledExtension
}

func (m *installMem) Get(_ context.Context, id string) (*domain.InstalledExtension, error) {
	return m.rows[id], nil
}

func (m *installMem) List(context.Context) ([]domain.InstalledExtension, error) {
	var out []domain.InstalledExtension
	for _, r := range m.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (m *installMem) UpdateCatalog(_ context.Context, e *domain.InstalledExtension) error {
	r := m.rows[e.ExtensionID]
	r.PreviousVersion = r.Version
	r.Version, r.Manifest, r.CommitSHA, r.ArtifactSHA256, r.TrustLevel = e.Version, e.Manifest, e.CommitSHA, e.ArtifactSHA256, e.TrustLevel
	return nil
}

func (m *installMem) InsertCatalog(_ context.Context, e *domain.InstalledExtension) error {
	m.rows[e.ExtensionID] = e
	return nil
}

type installFixture struct {
	t        *testing.T
	inst     *Installer
	store    *installMem
	artifact []byte
	pings    int32
	journal  *memJournal
	changes  int
	manifest []byte
}

func newInstallFixture(t *testing.T, mutate func(e *Entry, cat *Catalog)) *installFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/mesh-ext-hello.json")
	require.NoError(t, err)
	var mf map[string]any
	require.NoError(t, json.Unmarshal(raw, &mf))
	id, ver := mf["id"].(string), mf["version"].(string)

	f := &installFixture{t: t, artifact: tarGz(t, map[string][]byte{"mesh-extension.json": raw, "README.md": []byte("x")}), store: &installMem{rows: map[string]*domain.InstalledExtension{}}, journal: &memJournal{}}
	sum := sha256.Sum256(f.artifact)
	digest := hex.EncodeToString(sum[:])
	detail := map[string]any{"id": id, "releases": []any{map[string]any{"version": ver, "manifest": mf, "installable": true, "status": "published", "yanked": false}}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/mesh-extensions/"+id, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(detail) })
	mux.HandleFunc("/api/v1/mesh-extensions/"+id+"/releases/"+ver+"/artifact", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(f.artifact) })
	mux.HandleFunc("/api/v1/mesh-extensions/"+id+"/installs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			atomic.AddInt32(&f.pings, 1)
		}
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	caps, err := json.Marshal(mf["capabilities"])
	require.NoError(t, err)
	e := Entry{ID: id, Name: "Hello", Runtime: "external", Trust: "verified",
		Latest: Release{Version: ver, MeshAPI: ">=1 <3", CommitSHA: "abc123", ArtifactSHA256: &digest, Capabilities: caps}}
	f.manifest = raw
	cat := &Catalog{Schema: 1, Extensions: []Entry{e}}
	if mutate != nil {
		mutate(&cat.Extensions[0], cat)
	}
	c := NewClient(func() string { return srv.URL }, nil)
	c.current = cat
	f.inst = &Installer{Catalog: c, SparkURL: func() string { return srv.URL }, Store: f.store, Journal: f.journal,
		OnChange: func(context.Context) { f.changes++ }}
	return f
}

func (f *installFixture) id() string { return f.inst.Catalog.current.Extensions[0].ID }

func TestInstall_FromCatalog(t *testing.T) {
	f := newInstallFixture(t, nil)
	row, err := f.inst.Install(context.Background(), uuid.New(), f.id())
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionSourceCatalog, row.Source)
	assert.Equal(t, domain.ExtensionTrustLevel("verified"), row.TrustLevel)
	assert.Equal(t, "abc123", row.CommitSHA)
	assert.NotEmpty(t, row.ArtifactSHA256)
	assert.Equal(t, 1, f.changes)
	assert.Equal(t, []string{f.id() + ":installed"}, f.journal.actions)
	assert.Eventually(t, func() bool { return atomic.LoadInt32(&f.pings) == 1 }, time.Second, 10*time.Millisecond, "anonymous install ping")
}

// Red control: bytes that do not hash to the signed digest install nothing.
func TestInstall_ArtifactDigestMismatchInstallsNothing(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.artifact = []byte("tampered")
	_, err := f.inst.Install(context.Background(), uuid.New(), f.id())
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, CodeDigest, ae.ErrorCode)
	assert.Empty(t, f.store.rows)
	assert.Zero(t, f.changes)
}

func TestInstall_Refusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(e *Entry, cat *Catalog)
		code   string
	}{
		"blocked":      {func(e *Entry, c *Catalog) { c.Blocklist = []Block{{ID: e.ID, Reason: "malware"}} }, CodeBlocked},
		"incompatible": {func(e *Entry, _ *Catalog) { e.Latest.MeshAPI = ">=7 <9" }, "extension_incompatible"},
		"no digest":    {func(e *Entry, _ *Catalog) { e.Latest.ArtifactSHA256 = nil }, CodeDigest},
		"builtin":      {func(e *Entry, _ *Catalog) { e.Runtime = "builtin" }, "extension_builtin_runtime"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t, tc.mutate)
			_, err := f.inst.Install(context.Background(), uuid.New(), f.id())
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			assert.Equal(t, tc.code, ae.ErrorCode)
			assert.Empty(t, f.store.rows)
		})
	}
}

func TestInstall_UnknownAndAlreadyInstalled(t *testing.T) {
	f := newInstallFixture(t, nil)
	_, err := f.inst.Install(context.Background(), uuid.New(), "nope")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, CodeNotInCatalog, ae.ErrorCode)

	f.store.rows[f.id()] = &domain.InstalledExtension{ExtensionID: f.id()}
	_, err = f.inst.Install(context.Background(), uuid.New(), f.id())
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusConflict, ae.Code)
}

func TestInstall_CounterSwitchedOff(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.inst.CounterOn = func() bool { return false }
	_, err := f.inst.Install(context.Background(), uuid.New(), f.id())
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, atomic.LoadInt32(&f.pings))
}

// installedAt puts the fixture's extension in the store at an older version
// whose manifest declares no permissions, so the catalog release adds them.
func (f *installFixture) installedAt(version string, withCaps bool) {
	var mf map[string]any
	require.NoError(f.t, json.Unmarshal(f.manifest, &mf))
	mf["version"] = version
	if !withCaps {
		mf["capabilities"] = map[string]any{"api": []any{}, "events": []any{}, "webhooks_in": []any{}, "ui": []any{}, "secrets": []any{}}
	}
	raw, err := json.Marshal(mf)
	require.NoError(f.t, err)
	f.store.rows[f.id()] = &domain.InstalledExtension{ExtensionID: f.id(), Version: version, Manifest: raw, Source: domain.ExtensionSourceCatalog, CommitSHA: "old"}
}

func TestUpdates_OnCatalogData(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	list, err := f.inst.Updates(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].UpdateAvailable)
	assert.Equal(t, "0.1.0", list[0].LatestVersion)
	assert.False(t, list[0].NeedsConsent, "same permissions, no consent")

	// a repo-sourced row is not this tab's business
	f.store.rows["other"] = &domain.InstalledExtension{ExtensionID: "other", Source: domain.ExtensionSourceRepo}
	list, _ = f.inst.Updates(context.Background())
	assert.Len(t, list, 1)
}

func TestUpdates_NothingOfferedWhenYankedBlockedOrNotNewer(t *testing.T) {
	// installed is the version on the instance; the catalog fixture's latest is 0.1.0.
	cases := map[string]struct {
		installed string
		mutate    func(e *Entry, c *Catalog)
	}{
		// a yanked latest is simply absent: the catalog lists an older latest
		"latest is older than installed": {"0.1.0", func(e *Entry, _ *Catalog) { e.Latest.Version = "0.0.5" }},
		"same version":                   {"0.1.0", nil},
		"latest flagged yanked":          {"0.0.9", func(e *Entry, _ *Catalog) { e.Latest.Yanked = true }},
		"no artifact digest":             {"0.0.9", func(e *Entry, _ *Catalog) { e.Latest.ArtifactSHA256 = nil }},
		"blocked":                        {"0.0.9", func(e *Entry, c *Catalog) { c.Blocklist = []Block{{ID: e.ID, Reason: "x"}} }},
		"incompatible":                   {"0.0.9", func(e *Entry, _ *Catalog) { e.Latest.MeshAPI = ">=9" }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t, tc.mutate)
			f.installedAt(tc.installed, true)
			list, err := f.inst.Updates(context.Background())
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.False(t, list[0].UpdateAvailable)
			assert.False(t, f.store.rows[f.id()].Blocked, "hiding from Updates never switches an install off")
		})
	}
}

func TestUpdate_AppliesAndKeepsPreviousVersion(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	res, err := f.inst.Update(context.Background(), nil, f.id(), "")
	require.NoError(t, err)
	assert.Equal(t, "updated", string(res.Outcome))
	row := f.store.rows[f.id()]
	assert.Equal(t, "0.1.0", row.Version)
	assert.Equal(t, "0.0.9", row.PreviousVersion)
	assert.Equal(t, "abc123", row.CommitSHA)
	assert.Equal(t, 1, f.changes)

	res, err = f.inst.Update(context.Background(), nil, f.id(), "")
	require.NoError(t, err)
	assert.Equal(t, "up_to_date", string(res.Outcome))
}

func TestUpdate_NewPermissionsNeedConsentOnThatCommit(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", false)
	res, err := f.inst.Update(context.Background(), nil, f.id(), "")
	require.NoError(t, err)
	assert.Equal(t, "pending_consent", string(res.Outcome))
	assert.NotEmpty(t, res.NewCapabilities)
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version, "nothing moved without consent")

	res, err = f.inst.Update(context.Background(), nil, f.id(), "some-other-commit")
	require.NoError(t, err)
	assert.Equal(t, "pending_consent", string(res.Outcome), "consent for another commit does not count")

	res, err = f.inst.Update(context.Background(), nil, f.id(), "abc123")
	require.NoError(t, err)
	assert.Equal(t, "updated", string(res.Outcome))
}

// Red control: tampered artifact bytes leave the installed version untouched.
func TestUpdate_DigestMismatchChangesNothing(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	f.artifact = []byte("tampered")
	_, err := f.inst.Update(context.Background(), nil, f.id(), "")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, CodeDigest, ae.ErrorCode)
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version)
	assert.Zero(t, f.changes)
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, data := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// The installed manifest is the one inside the digest-verified artifact, not
// whatever Spark's unsigned detail answer carries.
func TestInstall_ManifestComesFromVerifiedArtifact(t *testing.T) {
	f := newInstallFixture(t, nil)
	var mf map[string]any
	require.NoError(t, json.Unmarshal(f.manifest, &mf))
	mf["description"] = "from the artifact"
	inArtifact, err := json.Marshal(mf)
	require.NoError(t, err)
	art := tarGz(t, map[string][]byte{"mesh-extension.json": inArtifact})
	sum := sha256.Sum256(art)
	digest := hex.EncodeToString(sum[:])
	f.artifact = art
	f.inst.Catalog.current.Extensions[0].Latest.ArtifactSHA256 = &digest

	row, err := f.inst.Install(context.Background(), uuid.New(), f.id())
	require.NoError(t, err)
	assert.Contains(t, string(row.Manifest), "from the artifact")
}

func TestInstall_ArtifactWithoutManifestInstallsNothing(t *testing.T) {
	f := newInstallFixture(t, nil)
	art := tarGz(t, map[string][]byte{"README.md": []byte("x")})
	sum := sha256.Sum256(art)
	digest := hex.EncodeToString(sum[:])
	f.artifact = art
	f.inst.Catalog.current.Extensions[0].Latest.ArtifactSHA256 = &digest

	_, err := f.inst.Install(context.Background(), uuid.New(), f.id())
	require.Error(t, err)
	assert.Empty(t, f.store.rows)
}

// A digest-verified archive that inflates past the cap before reaching the
// manifest is refused instead of being decompressed without bound.
func TestManifestFromArtifact_BoundsDecompression(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	big := make([]byte, 1<<20)
	for i := 0; i < maxUnpackedBytes>>20+2; i++ {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("pad%d.bin", i), Mode: 0o644, Size: int64(len(big)), Typeflag: tar.TypeReg}))
		_, err := tw.Write(big)
		require.NoError(t, err)
	}
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "mesh-extension.json", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte("{}"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())

	_, err = manifestFromArtifact(buf.Bytes())
	require.Error(t, err, "the manifest sits past the unpacked-size cap")
}
