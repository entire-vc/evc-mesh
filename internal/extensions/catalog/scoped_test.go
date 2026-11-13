package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
)

// scopedID is the id of the testdata manifest. The default catalogue of the
// scoped fixture does not list it.
const scopedID = "hello"

// scopedRelease is one release Spark serves to the fixture: its artifact, the
// digest of that artifact and its commit.
type scopedRelease struct {
	version, commit, digest string
	artifact                []byte
}

// scopedSpark is Spark's external HTTP boundary for exact receipts: the
// receipt route (catalog.json?scope=scoped&slug=&version=&mesh_api=), the
// ordinary artifact route, the detail route (which, like Spark, hides scoped
// releases) and the install counter. Receipt bodies come from
// testdata/scoped and are signed with the fixture key.
type scopedSpark struct {
	t        *testing.T
	priv     ed25519.PrivateKey
	pub      ed25519.PublicKey
	srv      *httptest.Server
	mu       sync.Mutex
	receipts map[string]servedReceipt // "id@version" as asked
	releases map[string]*scopedRelease
	hits     map[string]int
	queries  []string
	// defaults are the versions the detail route lists (default scope only).
	defaults []string
}

type servedReceipt struct {
	body   []byte
	sig    string
	status int
}

func newScopedSpark(t *testing.T, f *installFixture) *scopedSpark {
	t.Helper()
	pub, priv := keypair(t)
	s := &scopedSpark{t: t, priv: priv, pub: pub, receipts: map[string]servedReceipt{}, releases: map[string]*scopedRelease{}, hits: map[string]int{}}
	id := scopedID
	// mock: external boundary — Spark's public HTTP API.
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		p := r.URL.Path
		switch {
		case p == catalogPath && r.URL.Query().Get("scope") == ScopeScoped:
			s.hits["receipt"]++
			s.queries = append(s.queries, r.URL.RawQuery)
			q := r.URL.Query()
			rc, ok := s.receipts[q.Get("slug")+"@"+q.Get("version")]
			if !ok || rc.status == http.StatusNotFound {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if rc.status != 0 {
				w.WriteHeader(rc.status)
				return
			}
			w.Header().Set(SignatureHeader, rc.sig)
			w.Header().Set("ETag", ETagOf(rc.body))
			_, _ = w.Write(rc.body)
		case p == catalogPath:
			s.hits["default"]++
			w.WriteHeader(http.StatusNotFound)
		case strings.HasPrefix(p, "/api/v1/mesh-extensions/"+id+"/releases/"):
			s.hits["artifact"]++
			v := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/mesh-extensions/"+id+"/releases/"), "/artifact")
			if rel, ok := s.releases[v]; ok {
				_, _ = w.Write(rel.artifact)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case p == "/api/v1/mesh-extensions/"+id:
			s.hits["detail"]++
			listed := []any{}
			for _, v := range s.defaults {
				listed = append(listed, map[string]any{"version": v, "installable": true, "status": "published"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "releases": listed})
		default:
			s.hits["other"]++
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	key := base64Key(pub)
	c := NewClient(func() string { return s.srv.URL }, func() string { return key })
	c.current = &Catalog{Schema: 1}
	f.inst.Catalog = c
	f.inst.SparkURL = func() string { return s.srv.URL }
	f.inst.CounterOn = func() bool { return false }
	return s
}

// release builds a real artifact for version (manifest from testdata, with
// that version) and remembers it under the ordinary artifact route.
func (s *scopedSpark) release(f *installFixture, version string, commitChar byte) *scopedRelease {
	raw := withManifest(s.t, f, func(m map[string]any) { m["version"] = version })
	art := tarGz(s.t, map[string][]byte{"mesh-extension.json": raw})
	sum := sha256.Sum256(art)
	r := &scopedRelease{version: version, commit: strings.Repeat(string(commitChar), 40), digest: hex.EncodeToString(sum[:]), artifact: art}
	s.mu.Lock()
	s.releases[version] = r
	s.mu.Unlock()
	return r
}

// render fills a testdata/scoped template: the entry is id@rel, the selection
// it states is selID@selVersion.
func renderFixture(t *testing.T, file, id string, rel *scopedRelease, selID, selVersion string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/scoped/" + file)
	require.NoError(t, err)
	out := strings.NewReplacer("{{ID}}", id, "{{VERSION}}", rel.version, "{{COMMIT}}", rel.commit, "{{SHA256}}", rel.digest,
		"{{SEL_ID}}", selID, "{{SEL_VERSION}}", selVersion).Replace(string(raw))
	return []byte(strings.TrimSpace(out))
}

// serve answers the exact receipt asked as askedID@askedVersion.
func (s *scopedSpark) serve(askedID, askedVersion string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receipts[askedID+"@"+askedVersion] = servedReceipt{body: body, sig: sign(s.priv, string(body))}
}

// serveValid answers id@rel.version with the valid receipt for it.
func (s *scopedSpark) serveValid(f *installFixture, rel *scopedRelease) {
	s.serve(scopedID, rel.version, renderFixture(s.t, "receipt.json", scopedID, rel, scopedID, rel.version))
}

func (s *scopedSpark) withdraw(id, version string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receipts[id+"@"+version] = servedReceipt{status: http.StatusNotFound}
}

func (s *scopedSpark) hit(k string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[k]
}

func newScopedFixture(t *testing.T) (*installFixture, *scopedSpark) {
	t.Helper()
	f := newInstallFixture(t, nil)
	s := newScopedSpark(t, f)
	return f, s
}

// Acceptance 1: a valid scoped receipt installs an id the default catalogue
// does not list and records source, version, commit, digest and the pin.
func TestInstallExact_ValidScopedReceiptInstallsPinned(t *testing.T) {
	f, s := newScopedFixture(t)
	id := scopedID
	_, listed := f.inst.Catalog.current.Entry(id)
	require.False(t, listed, "the id must not be in the default catalogue")
	rel := s.release(f, "0.4.0", 'a')
	s.serveValid(f, rel)

	row, err := f.inst.InstallExact(context.Background(), uuid.New(), id, "0.4.0")
	require.NoError(t, err)
	assert.Equal(t, domain.ExtensionSourceCatalog, row.Source)
	assert.Equal(t, ScopeScoped, row.CatalogScope)
	assert.Equal(t, "0.4.0", row.Version)
	assert.Equal(t, rel.commit, row.CommitSHA)
	assert.Equal(t, rel.digest, row.ArtifactSHA256)
	assert.Equal(t, domain.ExtensionTrustLevel("community"), row.TrustLevel)
	assert.Equal(t, []string{id + ":installed"}, f.journal.actions)
	assert.Equal(t, 1, f.changes)
	assert.Equal(t, 0, s.hit("detail"), "the detail list hides scoped releases; the receipt decides")
	assert.Equal(t, 0, s.hit("default"), "no default catalogue request")
	require.Len(t, s.queries, 1)
	assert.Equal(t, "mesh_api=1&scope=scoped&slug="+id+"&version=0.4.0", s.queries[0])
}

// refusedReceipts are the receipts every exact path must refuse.
func refusedReceipts(t *testing.T, f *installFixture, s *scopedSpark, rel *scopedRelease) map[string]func() {
	id := scopedID
	_, evil := keypair(t)
	return map[string]func(){
		"receipt for another version": func() {
			s.serve(id, rel.version, renderFixture(t, "receipt.json", id, rel, id, "0.9.9"))
		},
		"receipt for another id": func() {
			s.serve(id, rel.version, renderFixture(t, "receipt.json", "someone-else", rel, "someone-else", rel.version))
		},
		"entry for another id under the right selection": func() {
			s.serve(id, rel.version, renderFixture(t, "receipt.json", "someone-else", rel, id, rel.version))
		},
		"two releases": func() {
			s.serve(id, rel.version, renderFixture(t, "receipt-two-releases.json", id, rel, id, rel.version))
		},
		"two entries": func() {
			s.serve(id, rel.version, renderFixture(t, "receipt-two-entries.json", id, rel, id, rel.version))
		},
		"default catalogue instead of a receipt": func() {
			s.serve(id, rel.version, renderFixture(t, "default-catalog.json", id, rel, id, rel.version))
		},
		"bad signature": func() {
			body := renderFixture(t, "receipt.json", id, rel, id, rel.version)
			s.mu.Lock()
			s.receipts[id+"@"+rel.version] = servedReceipt{body: body, sig: sign(evil, string(body))}
			s.mu.Unlock()
		},
		"404": func() { s.withdraw(id, rel.version) },
	}
}

// Acceptance 1: another id or version, two releases, two entries, a bad
// signature and a 404 are refused and nothing is written.
func TestInstallExact_RefusedReceiptsWriteNothing(t *testing.T) {
	probe, ps := newScopedFixture(t)
	for name := range refusedReceipts(t, probe, ps, ps.release(probe, "0.4.0", 'a')) {
		t.Run(name, func(t *testing.T) {
			f, s := newScopedFixture(t)
			rel := s.release(f, "0.4.0", 'a')
			refusedReceipts(t, f, s, rel)[name]()
			_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "0.4.0")
			require.Error(t, err)
			assert.Empty(t, f.store.rows, "no row")
			assert.Zero(t, f.changes)
			assert.Empty(t, f.journal.actions)
			assert.Equal(t, 0, s.hit("artifact"), "a refused receipt fetches no artifact")
		})
	}
}

// Red control for the digest: bytes that do not hash to the signed receipt
// digest install nothing.
func TestInstallExact_ArtifactDigestMismatchInstallsNothing(t *testing.T) {
	f, s := newScopedFixture(t)
	rel := s.release(f, "0.4.0", 'a')
	s.serveValid(f, rel)
	rel.artifact = []byte("tampered")
	_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "0.4.0")
	assert.Equal(t, CodeDigest, codeOf(t, err))
	assert.Empty(t, f.store.rows)
}

func TestInstallExact_ChecksOfEveryCatalogInstall(t *testing.T) {
	t.Run("already installed", func(t *testing.T) {
		f, s := newScopedFixture(t)
		s.serveValid(f, s.release(f, "0.4.0", 'a'))
		f.store.rows[scopedID] = &domain.InstalledExtension{ExtensionID: scopedID, Source: domain.ExtensionSourceRepo}
		_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "0.4.0")
		assert.Equal(t, repoinstall.CodeIDConflict, codeOf(t, err))
	})
	t.Run("blocked in the receipt", func(t *testing.T) {
		f, s := newScopedFixture(t)
		rel := s.release(f, "0.4.0", 'a')
		s.serve(scopedID, "0.4.0", renderFixture(t, "receipt-blocked.json", scopedID, rel, scopedID, "0.4.0"))
		_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "0.4.0")
		assert.Equal(t, CodeBlocked, codeOf(t, err))
		assert.Empty(t, f.store.rows)
	})
	t.Run("blocked in the default catalogue", func(t *testing.T) {
		f, s := newScopedFixture(t)
		s.serveValid(f, s.release(f, "0.4.0", 'a'))
		f.inst.Catalog.current.Blocklist = []Block{{ID: scopedID, Reason: "malware"}}
		_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "0.4.0")
		assert.Equal(t, CodeBlocked, codeOf(t, err))
		assert.Empty(t, f.store.rows)
	})
	t.Run("not an exact version", func(t *testing.T) {
		f, s := newScopedFixture(t)
		_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "latest")
		assert.Equal(t, CodeReceiptInvalid, codeOf(t, err))
		assert.Equal(t, 0, s.hit("receipt"))
	})
	t.Run("spark down", func(t *testing.T) {
		f, s := newScopedFixture(t)
		s.mu.Lock()
		s.receipts[scopedID+"@0.4.0"] = servedReceipt{status: http.StatusBadGateway}
		s.mu.Unlock()
		_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "0.4.0")
		assert.Equal(t, CodeSparkDown, codeOf(t, err))
	})
}

// scopedInstalled installs id@0.4.0 from its receipt and returns the release.
func scopedInstalled(t *testing.T) (*installFixture, *scopedSpark, *scopedRelease) {
	t.Helper()
	f, s := newScopedFixture(t)
	rel := s.release(f, "0.4.0", 'a')
	s.serveValid(f, rel)
	_, err := f.inst.InstallExact(context.Background(), uuid.New(), scopedID, "0.4.0")
	require.NoError(t, err)
	f.changes = 0
	f.journal.actions = nil
	return f, s, rel
}

// listInDefault makes the default catalogue list the id with a newer release
// (the "id later appears in the default catalogue" case).
func listInDefault(t *testing.T, f *installFixture, s *scopedSpark, version string) *scopedRelease {
	t.Helper()
	rel := s.release(f, version, 'f')
	var cat Catalog
	require.NoError(t, json.Unmarshal(renderFixture(t, "default-catalog.json", scopedID, rel, "", ""), &cat))
	f.inst.Catalog.current = &cat
	s.mu.Lock()
	s.defaults = append(s.defaults, version)
	s.mu.Unlock()
	return rel
}

// Acceptance 2 and item 4: autoupdate and the updates list leave a scoped
// install alone, also after its id appears in the default catalogue.
func TestScoped_AutoUpdateAndUpdatesListLeaveItPinned(t *testing.T) {
	f, s, rel := scopedInstalled(t)
	listInDefault(t, f, s, "0.9.0")
	ctx := context.Background()

	timerService(f, true).AutoUpdateOnce(ctx)
	row := f.store.rows[scopedID]
	assert.Equal(t, "0.4.0", row.Version, "autoupdate never moves a scoped install")
	assert.Equal(t, rel.commit, row.CommitSHA)
	assert.Equal(t, ScopeScoped, row.CatalogScope)
	assert.Zero(t, f.changes)

	list, err := f.inst.Updates(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.False(t, list[0].UpdateAvailable, "the default latest is not offered")
	assert.Empty(t, list[0].LatestVersion)
	assert.Equal(t, ScopeScoped, list[0].CatalogScope)

	res, err := f.inst.Update(ctx, nil, scopedID, "")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpToDate, res.Outcome)
	assert.Equal(t, "0.4.0", f.store.rows[scopedID].Version)
}

// Red control for the pin: the same default-catalogue release does move an
// unpinned install, so the test above fails if the pin is not honoured.
func TestScoped_DefaultInstallStillFollowsLatest(t *testing.T) {
	f, s, _ := scopedInstalled(t)
	listInDefault(t, f, s, "0.9.0")
	f.store.rows[scopedID].CatalogScope = ScopeDefault
	timerService(f, true).AutoUpdateOnce(context.Background())
	assert.Equal(t, "0.9.0", f.store.rows[scopedID].Version)
}

// Acceptance 2: an explicit update to another exact scoped version works and
// rollback returns the previous one, still pinned.
func TestScoped_UpdateExactThenRollback(t *testing.T) {
	f, s, first := scopedInstalled(t)
	ctx := context.Background()
	next := s.release(f, "0.5.0", 'b')
	s.serveValid(f, next)

	res, err := f.inst.UpdateExact(ctx, nil, scopedID, "", "0.5.0", next.commit)
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
	row := f.store.rows[scopedID]
	assert.Equal(t, "0.5.0", row.Version)
	assert.Equal(t, next.digest, row.ArtifactSHA256)
	assert.Equal(t, ScopeScoped, row.CatalogScope)
	assert.Equal(t, ScopeScoped, f.store.snaps[scopedID].CatalogScope)

	list, err := f.inst.Updates(ctx)
	require.NoError(t, err)
	assert.True(t, list[0].CanRollback)

	detailBefore := s.hit("detail")
	back, err := f.inst.Rollback(ctx, nil, scopedID)
	require.NoError(t, err)
	assert.Equal(t, "0.4.0", back.Version)
	assert.Equal(t, first.commit, back.CommitSHA)
	assert.Equal(t, first.digest, back.ArtifactSHA256)
	assert.Equal(t, ScopeScoped, back.CatalogScope, "rollback restores the pin")
	assert.Equal(t, detailBefore, s.hit("detail"), "a scoped snapshot is judged by its exact receipt")
}

func TestScoped_UpdateExactRefusalsLeaveRowUntouched(t *testing.T) {
	cases := map[string]func(f *installFixture, s *scopedSpark, next *scopedRelease) (string, string){
		"wrong commit": func(f *installFixture, s *scopedSpark, next *scopedRelease) (string, string) {
			s.serveValid(f, next)
			return "0.5.0", strings.Repeat("9", 40)
		},
		"not newer": func(f *installFixture, s *scopedSpark, _ *scopedRelease) (string, string) {
			old := s.release(f, "0.3.0", 'c')
			s.serveValid(f, old)
			return "0.3.0", old.commit
		},
	}
	probe, ps, _ := scopedInstalled(t)
	for name := range refusedReceipts(t, probe, ps, ps.release(probe, "0.5.0", 'b')) {
		cases["receipt: "+name] = func(f *installFixture, s *scopedSpark, next *scopedRelease) (string, string) {
			refusedReceipts(t, f, s, next)[name]()
			return "0.5.0", next.commit
		}
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f, s, first := scopedInstalled(t)
			next := s.release(f, "0.5.0", 'b')
			v, sha := setup(f, s, next)
			_, err := f.inst.UpdateExact(context.Background(), nil, scopedID, "", v, sha)
			require.Error(t, err)
			row := f.store.rows[scopedID]
			assert.Equal(t, "0.4.0", row.Version)
			assert.Equal(t, first.commit, row.CommitSHA)
			assert.Equal(t, ScopeScoped, row.CatalogScope)
			assert.Nil(t, f.store.snaps[scopedID])
			assert.Zero(t, f.changes)
		})
	}
}

// Item 4: the pin ends only when an admin chooses a default-catalogue version.
func TestScoped_ChoosingADefaultVersionUnpins(t *testing.T) {
	f, s, _ := scopedInstalled(t)
	latest := listInDefault(t, f, s, "0.9.0")
	res, err := f.inst.UpdateTarget(context.Background(), nil, scopedID, "", "0.9.0", latest.commit)
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
	assert.Equal(t, ScopeDefault, f.store.rows[scopedID].CatalogScope)
	assert.Equal(t, ScopeScoped, f.store.snaps[scopedID].CatalogScope, "the snapshot remembers the pin for rollback")
}

// Item 6 / acceptance 3: rollback to a scoped release Spark has since
// withdrawn is refused and changes nothing.
func TestScoped_RollbackToWithdrawnReleaseRefused(t *testing.T) {
	f, s, first := scopedInstalled(t)
	next := s.release(f, "0.5.0", 'b')
	s.serveValid(f, next)
	_, err := f.inst.UpdateExact(context.Background(), nil, scopedID, "", "0.5.0", next.commit)
	require.NoError(t, err)
	s.withdraw(scopedID, first.version)
	_, err = f.inst.Rollback(context.Background(), nil, scopedID)
	assert.Equal(t, CodeReleaseRevoked, codeOf(t, err))
	assert.Equal(t, "0.5.0", f.store.rows[scopedID].Version)
}

// Acceptance 3 / item 5: a scoped version Spark blocks or yanks after install
// is never in the default blocklist (Spark keeps scoped blocks out of it) and
// its exact receipt answers 404. The blocklist pass re-reads the exact receipt
// of every scoped install: 404 switches it off, a receipt that is back
// switches it on, an unreachable Spark changes nothing.
func TestEnforcer_ScopedBlockedAfterInstall(t *testing.T) {
	f, s, rel := scopedInstalled(t)
	ctx := context.Background()
	c := f.inst.Catalog
	var notified []string
	enf := &Enforcer{Store: f.store, Journal: f.journal,
		Notify: func(_ context.Context, id, reason string, blocked bool) { notified = append(notified, id+":"+reason) },
		Exact: func(ctx context.Context, id, version string) (*ExactReceipt, error) {
			return c.FetchExact(ctx, 1, id, version)
		}}
	empty := &Catalog{Schema: 1}

	enf.Apply(ctx, empty)
	assert.False(t, f.store.rows[scopedID].Blocked, "a served receipt keeps it on")

	s.withdraw(scopedID, "0.4.0")
	enf.Apply(ctx, empty)
	row := f.store.rows[scopedID]
	assert.True(t, row.Blocked, "404 on the exact receipt switches it off")
	assert.Equal(t, ReasonScopedWithdrawn, row.BlockedReason)
	assert.Equal(t, []string{scopedID + ":" + InstanceJournalBlocked}, f.journal.actions)
	assert.Equal(t, []string{scopedID + ":" + ReasonScopedWithdrawn}, notified)

	s.mu.Lock()
	s.receipts[scopedID+"@0.4.0"] = servedReceipt{status: http.StatusBadGateway}
	s.mu.Unlock()
	enf.Apply(ctx, empty)
	assert.True(t, f.store.rows[scopedID].Blocked, "Spark down: state kept")

	s.serveValid(f, rel)
	enf.Apply(ctx, empty)
	assert.False(t, f.store.rows[scopedID].Blocked, "the receipt is back: switched on again")

	s.serve(scopedID, "0.4.0", renderFixture(t, "receipt-blocked.json", scopedID, rel, scopedID, "0.4.0"))
	enf.Apply(ctx, empty)
	assert.True(t, f.store.rows[scopedID].Blocked, "a block in the receipt's own blocklist")
	assert.Equal(t, "Extension suspended", f.store.rows[scopedID].BlockedReason)
}

// A served exact receipt whose commit or artifact digest differs from the
// installed row means the same id@version now names other bytes: the pass
// switches the install off; the matching receipt switches it on again.
func TestEnforcer_ScopedReceiptMismatchBlocks(t *testing.T) {
	f, _, _ := scopedInstalled(t)
	ctx := context.Background()
	c := f.inst.Catalog
	var mutate func(*ExactReceipt)
	enf := &Enforcer{Store: f.store, Journal: f.journal,
		Exact: func(ctx context.Context, id, version string) (*ExactReceipt, error) {
			rc, err := c.FetchExact(ctx, 1, id, version)
			if err == nil && mutate != nil {
				mutate(rc)
			}
			return rc, err
		}}
	empty := &Catalog{Schema: 1}

	enf.Apply(ctx, empty)
	require.False(t, f.store.rows[scopedID].Blocked, "the matching receipt keeps it on")

	other := strings.Repeat("f", 40)
	mutate = func(rc *ExactReceipt) { rc.Entry.Latest.CommitSHA = other }
	enf.Apply(ctx, empty)
	assert.True(t, f.store.rows[scopedID].Blocked, "another commit under the same version")
	assert.Equal(t, ReasonScopedWithdrawn, f.store.rows[scopedID].BlockedReason)

	mutate = nil
	enf.Apply(ctx, empty)
	require.False(t, f.store.rows[scopedID].Blocked, "the matching receipt switches it on again")

	digest := strings.Repeat("e", 64)
	mutate = func(rc *ExactReceipt) { rc.Entry.Latest.ArtifactSHA256 = &digest }
	enf.Apply(ctx, empty)
	assert.True(t, f.store.rows[scopedID].Blocked, "another artifact digest under the same version")

	mutate = nil
	enf.Apply(ctx, empty)
	require.False(t, f.store.rows[scopedID].Blocked)

	mutate = func(rc *ExactReceipt) { rc.Entry.Latest.ArtifactSHA256 = nil }
	enf.Apply(ctx, empty)
	assert.True(t, f.store.rows[scopedID].Blocked, "a receipt without a digest")
}

// A scoped row the default blocklist already names still has its exact
// receipt re-read on every pass; it stays blocked by either source.
func TestEnforcer_ScopedReceiptReadEvenWhenDefaultBlocks(t *testing.T) {
	f, s, _ := scopedInstalled(t)
	ctx := context.Background()
	c := f.inst.Catalog
	reads := 0
	enf := &Enforcer{Store: f.store, Journal: f.journal,
		Exact: func(ctx context.Context, id, version string) (*ExactReceipt, error) {
			reads++
			return c.FetchExact(ctx, 1, id, version)
		}}
	blocked := &Catalog{Schema: 1, Blocklist: []Block{{ID: scopedID, Version: "0.4.0", Reason: "default says no"}}}

	enf.Apply(ctx, blocked)
	assert.Equal(t, 1, reads, "the exact receipt is read although the default blocklist hits")
	assert.True(t, f.store.rows[scopedID].Blocked)
	assert.Equal(t, "default says no", f.store.rows[scopedID].BlockedReason)

	s.withdraw(scopedID, "0.4.0")
	enf.Apply(ctx, blocked)
	assert.Equal(t, 2, reads)
	assert.True(t, f.store.rows[scopedID].Blocked, "both sources block")

	enf.Apply(ctx, &Catalog{Schema: 1})
	assert.Equal(t, 3, reads)
	assert.True(t, f.store.rows[scopedID].Blocked, "the default block lifted, the receipt still 404")
	assert.Equal(t, ReasonScopedWithdrawn, f.store.rows[scopedID].BlockedReason)
}

// Red control for the scoped pass: without it (Exact nil, the old Enforcer)
// the withdrawn scoped release stays on.
func TestEnforcer_ScopedWithoutExactPassMissesTheBlock(t *testing.T) {
	f, s, _ := scopedInstalled(t)
	s.withdraw(scopedID, "0.4.0")
	(&Enforcer{Store: f.store, Journal: f.journal}).Apply(context.Background(), &Catalog{Schema: 1})
	assert.False(t, f.store.rows[scopedID].Blocked)
}

func TestEnforcer_ExactOnlyForScopedRows(t *testing.T) {
	calls := 0
	st := &memStore{rows: map[string]*domain.InstalledExtension{
		"plain":  {ExtensionID: "plain", Version: "1.0.0", Source: domain.ExtensionSourceCatalog, CatalogScope: ScopeDefault},
		"pinned": {ExtensionID: "pinned", Version: "1.0.0", Source: domain.ExtensionSourceCatalog, CatalogScope: ScopeScoped, CommitSHA: "c"},
	}}
	enf := &Enforcer{Store: st, Exact: func(_ context.Context, id, _ string) (*ExactReceipt, error) {
		calls++
		assert.Equal(t, "pinned", id)
		return nil, errors.New("unreachable")
	}}
	enf.Apply(context.Background(), &Catalog{Schema: 1})
	assert.Equal(t, 1, calls)
	assert.False(t, st.rows["pinned"].Blocked)
}

// An exact update out of a withdrawn version switches the install back on at
// once; the receipt just read vouches for the new version.
func TestScoped_UpdateExactOutOfABlockedVersion(t *testing.T) {
	f, s, _ := scopedInstalled(t)
	f.store.rows[scopedID].Blocked, f.store.rows[scopedID].BlockedReason = true, ReasonScopedWithdrawn
	next := s.release(f, "0.5.0", 'b')
	s.serveValid(f, next)
	_, err := f.inst.UpdateExact(context.Background(), nil, scopedID, "", "0.5.0", next.commit)
	require.NoError(t, err)
	assert.False(t, f.store.rows[scopedID].Blocked)
	assert.Equal(t, "0.5.0", f.store.rows[scopedID].Version)
}

// Item 3: the repository -> catalogue move accepts an exact scoped version as
// its target and the moved install is pinned.
func TestMoveToCatalog_ScopedTarget(t *testing.T) {
	f, s := newScopedFixture(t)
	rel := s.release(f, "0.4.0", 'a')
	s.serveValid(f, rel)
	raw := withManifest(t, f, func(m map[string]any) { m["version"] = "0.4.0" })
	f.store.rows[scopedID] = &domain.InstalledExtension{ExtensionID: scopedID, Version: "0.4.0", Manifest: raw, Source: domain.ExtensionSourceRepo,
		RepoURL: "https://gitlab.example/grp/hello", RepoRef: "v0.4.0", CommitSHA: repoSHA, TrustLevel: domain.ExtensionTrustCustom}
	f.store.tokens = map[string]string{scopedID: "glpat-secret-value"} // pragma: allowlist secret
	ctx := context.Background()

	_, err := f.inst.PlanMoveToCatalog(ctx, scopedID, "0.4.0", "")
	assert.Equal(t, CodeNotInCatalog, codeOf(t, err), "the default catalogue does not list it")

	plan, err := f.inst.PlanMoveToCatalog(ctx, scopedID, "0.4.0", ScopeScoped)
	require.NoError(t, err)
	assert.Equal(t, ScopeScoped, plan.Target.CatalogScope)
	assert.Equal(t, rel.commit, plan.Target.CommitSHA)

	req := MoveRequest{ExpectedVersion: "0.4.0", ExpectedCommitSHA: repoSHA, TargetVersion: "0.4.0", TargetCommitSHA: rel.commit, TargetScope: ScopeScoped}
	res, err := f.inst.MoveToCatalog(ctx, uuid.New(), scopedID, req)
	require.NoError(t, err)
	assert.Equal(t, OutcomeMoved, res.Outcome)
	row := f.store.rows[scopedID]
	assert.Equal(t, domain.ExtensionSourceCatalog, row.Source)
	assert.Equal(t, ScopeScoped, row.CatalogScope)
	assert.Equal(t, rel.digest, row.ArtifactSHA256)
	assert.Empty(t, f.store.tokens)
	assert.Equal(t, 0, s.hit("detail"))

	again, err := f.inst.MoveToCatalog(ctx, uuid.New(), scopedID, req)
	require.NoError(t, err)
	assert.Equal(t, OutcomeAlreadyMoved, again.Outcome)

	_, err = f.inst.PlanMoveToCatalog(ctx, scopedID, "0.4.0", "pinned")
	require.Error(t, err)
}

// Acceptance 4: the default install makes no receipt request.
func TestInstall_DefaultFlowMakesNoReceiptRequest(t *testing.T) {
	f, s := newScopedFixture(t)
	listInDefault(t, f, s, "0.9.0")
	row, err := f.inst.Install(context.Background(), uuid.New(), scopedID)
	require.NoError(t, err)
	assert.Equal(t, ScopeDefault, row.CatalogScope)
	assert.Equal(t, "0.9.0", row.Version)
	assert.Equal(t, 1, s.hit("detail"))
	assert.Equal(t, 1, s.hit("artifact"))
	assert.Equal(t, 0, s.hit("receipt"), "no receipt request per default install")
}

// A signed exact receipt replayed where the default catalogue was asked for
// is refused and the previous copy kept: it would narrow the blocklist to one id.
func TestRefresh_RefusesExactReceiptAsDefaultCatalogue(t *testing.T) {
	pub, priv := keypair(t)
	body := `{"blocklist":[],"extensions":[],"generated_at":"2026-11-12T15:00:00Z","schema":1,"selection":{"scope":"scoped","slug":"x","version":"1.0.0"}}`
	fs := &fakeSpark{body: bodyV1, sig: sign(priv, bodyV1)}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	c := newTestClient(srv, pub)
	require.NoError(t, c.Refresh(context.Background(), 1))
	fs.body, fs.sig = body, sign(priv, body)
	err := c.Refresh(context.Background(), 1)
	require.ErrorIs(t, err, ErrUnexpectedSelection)
	cur, _ := c.Current()
	assert.Len(t, cur.Blocklist, 1, "previous copy kept")
}

// Snapshots sealed before the scope existed stay intact; a scoped snapshot's
// digest covers its scope.
func TestCatalogSnapshot_ScopeInDigest(t *testing.T) {
	s := domain.CatalogSnapshot{Version: "1.0.0", Manifest: []byte(`{}`), CommitSHA: "c", ArtifactSHA256: "d", TrustLevel: "community", Source: "catalog"}
	legacy := s.ComputeDigest()
	s.CatalogScope = ScopeDefault
	assert.Equal(t, legacy, s.ComputeDigest())
	s.CatalogScope = ScopeScoped
	s.Seal()
	assert.NotEqual(t, legacy, s.Digest)
	s.CatalogScope = ScopeDefault
	assert.False(t, s.Intact(), "dropping the scope breaks the seal")
}
