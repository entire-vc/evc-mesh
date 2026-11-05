package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type pinnedFixture struct {
	*installFixture
	target, latest Release
	beforeArtifact func()
}

// Reuse the catalog's in-memory installation mode and serve only Spark's
// external HTTP boundary. Each version has its own real hash-bound artifact.
func newPinnedFixture(t *testing.T) *pinnedFixture {
	t.Helper()
	f := &pinnedFixture{installFixture: newInstallFixture(t, nil)}
	f.installedAt("0.2.3", true)
	entry := &f.inst.Catalog.current.Extensions[0]
	artifacts := map[string][]byte{}
	var releases []any
	for i, v := range []string{"0.3.0", "0.3.9"} {
		raw := withManifest(t, f.installFixture, func(m map[string]any) { m["version"] = v })
		art := tarGz(t, map[string][]byte{"mesh-extension.json": raw})
		sum := sha256.Sum256(art)
		digest := hex.EncodeToString(sum[:])
		r := entry.Latest
		r.Version, r.CommitSHA, r.ArtifactSHA256 = v, strings.Repeat(string(rune('a'+i)), 40), &digest
		if i == 0 {
			f.target = r
		} else {
			f.latest = r
		}
		artifacts[v] = art
		releases = append(releases, map[string]any{"version": v, "installable": true})
	}
	entry.Latest, entry.Releases = f.latest, []Release{f.target, f.latest}
	f.detail["releases"] = releases
	// mock: external boundary — Spark detail and release artifact HTTP API.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if art, ok := artifacts[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/mesh-extensions/"+f.id()+"/releases/"), "/artifact")]; ok {
			if f.beforeArtifact != nil {
				f.beforeArtifact()
			}
			_, _ = w.Write(art)
			return
		}
		_ = json.NewEncoder(w).Encode(f.detail)
	}))
	t.Cleanup(srv.Close)
	f.inst.SparkURL = func() string { return srv.URL }
	return f
}

func (f *pinnedFixture) update(consent string) (*repoinstall.UpdateResult, error) {
	return f.inst.UpdateTarget(context.Background(), nil, f.id(), consent, f.target.Version, f.target.CommitSHA)
}

func TestUpdateTarget_SelectsHistoricalReleaseWithoutChangingCache(t *testing.T) {
	f := newPinnedFixture(t)
	before, err := json.Marshal(f.inst.Catalog.current)
	require.NoError(t, err)
	res, err := f.update("")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
	assert.Equal(t, "0.3.0", res.Version)
	row := f.store.rows[f.id()]
	assert.Equal(t, f.target.CommitSHA, row.CommitSHA)
	assert.Equal(t, *f.target.ArtifactSHA256, row.ArtifactSHA256)
	assert.Equal(t, "0.2.3", row.PreviousVersion)
	after, err := json.Marshal(f.inst.Catalog.current)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "choosing a target must not replace cached Latest")
	assert.Equal(t, 1, f.changes)
	assert.Equal(t, []string{f.id() + ":updated"}, f.journal.actions)
}

func TestUpdateTarget_LatestRefreshCannotSubstituteSelectedRelease(t *testing.T) {
	f := newPinnedFixture(t)
	f.beforeArtifact = func() {
		cat := *f.inst.Catalog.current
		cat.Extensions = append([]Entry(nil), cat.Extensions...)
		cat.Extensions[0].Latest.Version = "0.4.0"
		f.inst.Catalog.mu.Lock()
		f.inst.Catalog.current = &cat
		f.inst.Catalog.mu.Unlock()
	}
	res, err := f.update("")
	require.NoError(t, err)
	assert.Equal(t, "0.3.0", res.Version)
	assert.Equal(t, f.target.CommitSHA, f.store.rows[f.id()].CommitSHA)
}

func TestUpdateTarget_SelectedCommitNeedsItsOwnConsent(t *testing.T) {
	f := newPinnedFixture(t)
	f.installedAt("0.2.3", false)
	for _, consent := range []string{"", f.latest.CommitSHA} {
		res, err := f.update(consent)
		require.NoError(t, err)
		assert.Equal(t, repoinstall.OutcomePendingConsent, res.Outcome)
		assert.Equal(t, f.target.CommitSHA, res.CommitSHA)
		assert.NotEmpty(t, res.NewCapabilities)
		assert.Empty(t, f.store.snaps)
		assert.Empty(t, f.journal.actions)
		assert.Zero(t, f.changes)
	}
	res, err := f.update(f.target.CommitSHA)
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
}

func TestUpdateTarget_RefusalsHaveNoEffects(t *testing.T) {
	cases := map[string]func(*pinnedFixture){
		"wrong pin":         func(f *pinnedFixture) { f.target.CommitSHA = strings.Repeat("f", 40) },
		"short pin":         func(f *pinnedFixture) { f.target.CommitSHA = "abc" },
		"malformed version": func(f *pinnedFixture) { f.target.Version = "v0.3.0" },
		"unknown version":   func(f *pinnedFixture) { f.target.Version = "0.3.1" },
		"unknown extension": func(f *pinnedFixture) { f.inst.Catalog.current.Extensions = nil },
		"not verified":      func(f *pinnedFixture) { f.inst.Catalog.current = nil },
		"repo row":          func(f *pinnedFixture) { f.store.rows[f.id()].Source = domain.ExtensionSourceRepo },
		"non upgrade":       func(f *pinnedFixture) { f.store.rows[f.id()].Version = "0.3.0" },
		"downgrade":         func(f *pinnedFixture) { f.store.rows[f.id()].Version = "0.4.0" },
		"blocked row":       func(f *pinnedFixture) { f.store.rows[f.id()].Blocked = true },
		"blocklisted": func(f *pinnedFixture) {
			f.inst.Catalog.current.Blocklist = []Block{{ID: f.id(), Version: f.target.Version}}
		},
		"builtin":          func(f *pinnedFixture) { f.inst.Catalog.current.Extensions[0].Runtime = "builtin" },
		"yanked":           func(f *pinnedFixture) { f.inst.Catalog.current.Extensions[0].Releases[0].Yanked = true },
		"no digest":        func(f *pinnedFixture) { f.inst.Catalog.current.Extensions[0].Releases[0].ArtifactSHA256 = nil },
		"incompatible":     func(f *pinnedFixture) { f.inst.Catalog.current.Extensions[0].Releases[0].MeshAPI = ">=9" },
		"revoked in Spark": func(f *pinnedFixture) { f.detail["releases"] = []any{} },
		"yanked in Spark": func(f *pinnedFixture) {
			f.detail["releases"] = []any{map[string]any{"version": "0.3.0", "installable": true, "yanked": true}}
		},
		"artifact digest mismatch": func(f *pinnedFixture) {
			bad := strings.Repeat("0", 64)
			f.inst.Catalog.current.Extensions[0].Releases[0].ArtifactSHA256 = &bad
		},
	}
	for name, caps := range map[string]string{"null": "null", "array": "[]", "group object": `{"api":{}}`, "null item": `{"api":[null]}`, "number item": `{"api":[1]}`, "empty item": `{"api":[""]}`} {
		cases["malformed capabilities "+name] = func(f *pinnedFixture) { f.inst.Catalog.current.Extensions[0].Releases[0].Capabilities = []byte(caps) }
	}
	for name, mutate := range map[string]func(*Release){
		"commit":       func(r *Release) { r.CommitSHA = strings.Repeat("c", 40) },
		"digest":       func(r *Release) { d := strings.Repeat("0", 64); r.ArtifactSHA256 = &d },
		"capabilities": func(r *Release) { r.Capabilities = []byte(`{"api":[]}`) },
		"api":          func(r *Release) { r.MeshAPI = ">=2" },
		"yank":         func(r *Release) { r.Yanked = true },
	} {
		cases["conflicting records "+name] = func(f *pinnedFixture) {
			r := f.target
			mutate(&r)
			f.inst.Catalog.current.Extensions[0].Releases = append(f.inst.Catalog.current.Extensions[0].Releases, r)
		}
	}
	for name, prep := range cases {
		t.Run(name, func(t *testing.T) {
			f := newPinnedFixture(t)
			id := f.id()
			prep(f)
			before := *f.store.rows[id]
			// The id stays stable even when the verified entry is removed.
			_, err := f.inst.UpdateTarget(context.Background(), nil, id, "", f.target.Version, f.target.CommitSHA)
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			assert.GreaterOrEqual(t, ae.Code, 400)
			assert.Less(t, ae.Code, 600)
			assert.Equal(t, before, *f.store.rows[id])
			assert.Empty(t, f.store.snaps)
			assert.Empty(t, f.journal.actions)
			assert.Zero(t, f.changes)
		})
	}
}

func TestUpdateTarget_IdenticalLatestAndReleasesAreNotAmbiguous(t *testing.T) {
	f := newPinnedFixture(t)
	f.target = f.latest
	res, err := f.update("")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
	assert.Equal(t, "0.3.9", res.Version)
}
