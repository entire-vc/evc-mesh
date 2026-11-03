package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// updatedFixture installs A (0.0.9) and updates it to the catalog's 0.1.0 (B).
// Spark's detail lists both releases as installable.
func updatedFixture(t *testing.T) (*installFixture, domain.InstalledExtension) {
	t.Helper()
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	f.store.rows[f.id()].ArtifactSHA256, f.store.rows[f.id()].TrustLevel = "a-digest", "verified"
	a := *f.store.rows[f.id()]
	f.detail["releases"] = []any{
		map[string]any{"version": "0.1.0", "installable": true, "yanked": false},
		map[string]any{"version": "0.0.9", "installable": true, "yanked": false},
	}
	res, err := f.inst.Update(context.Background(), nil, f.id(), "")
	require.NoError(t, err)
	require.Equal(t, "updated", string(res.Outcome))
	return f, a
}

// withManifest is the fixture's valid manifest after mutate.
func withManifest(t *testing.T, f *installFixture, mutate func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(f.manifest, &m))
	mutate(m)
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	return raw
}

func rollbackCode(t *testing.T, err error) string {
	t.Helper()
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	return ae.ErrorCode
}

func TestRollback_RestoresFullSnapshot(t *testing.T) {
	f, a := updatedFixture(t)
	ctx := context.Background()
	list, err := f.inst.Updates(ctx)
	require.NoError(t, err)
	assert.True(t, list[0].CanRollback, "an intact snapshot makes rollback possible")

	got, err := f.inst.Rollback(ctx, nil, f.id())
	require.NoError(t, err)
	assert.Equal(t, a.Version, got.Version)
	assert.JSONEq(t, string(a.Manifest), string(got.Manifest))
	assert.Equal(t, a.CommitSHA, got.CommitSHA)
	assert.Equal(t, a.ArtifactSHA256, got.ArtifactSHA256)
	assert.Equal(t, a.TrustLevel, got.TrustLevel)
	assert.Equal(t, a.Source, got.Source)
	assert.Equal(t, 2, f.changes, "update and rollback each resync the runtime")

	_, err = f.inst.Rollback(ctx, nil, f.id())
	assert.Equal(t, CodeRollbackUnavailable, rollbackCode(t, err), "repeat is a 409, nothing left to roll back to")
	list, _ = f.inst.Updates(ctx)
	assert.False(t, list[0].CanRollback)

	res, err := f.inst.UpdateAll(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "up_to_date", string(res[0].Outcome), "bulk update never takes back the rolled-back commit")
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version)

	res2, err := f.inst.Update(ctx, nil, f.id(), "")
	require.NoError(t, err)
	assert.Equal(t, "updated", string(res2.Outcome), "an explicit update still can")
}

func TestUpdates_CanRollbackFalseWhenRollbackWouldRefuse(t *testing.T) {
	for name, brk := range map[string]func(f *installFixture){
		"stale":   func(f *installFixture) { f.store.rows[f.id()].CommitSHA = "moved-on" },
		"blocked": func(f *installFixture) { f.store.rows[f.id()].Blocked = true },
		"corrupt": func(f *installFixture) { f.store.snaps[f.id()].CommitSHA = "tampered" },
		"legacy":  func(f *installFixture) { delete(f.store.snaps, f.id()) },
		"yanked in Spark": func(f *installFixture) {
			f.detail["releases"] = []any{map[string]any{"version": "0.0.9", "installable": true, "yanked": true}}
		},
		"blocklisted": func(f *installFixture) {
			f.inst.Catalog.current.Blocklist = []Block{{ID: f.id(), Version: "0.0.9", Reason: "bad"}}
		},
		"catalog unverified": func(f *installFixture) { f.inst.Catalog.current = nil },
		"incompatible": func(f *installFixture) {
			s := f.store.snaps[f.id()]
			s.Manifest = withManifest(t, f, func(m map[string]any) { m["version"], m["mesh_api"] = "0.0.9", ">=9" })
			s.Seal()
		},
		"would add permissions": func(f *installFixture) {
			f.store.rows[f.id()].Manifest = withManifest(t, f, func(m map[string]any) { m["capabilities"] = map[string]any{} })
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := updatedFixture(t)
			brk(f)
			list, err := f.inst.Updates(context.Background())
			require.NoError(t, err)
			assert.False(t, list[0].CanRollback)
		})
	}
}

func TestRollback_RefusalsChangeNothing(t *testing.T) {
	cases := map[string]struct {
		code   string
		status int
		break_ func(f *installFixture)
	}{
		"legacy previous_version without snapshot": {CodeRollbackUnavailable, 409, func(f *installFixture) { delete(f.store.snaps, f.id()) }},
		"corrupt snapshot":                         {CodeSnapshotCorrupt, 409, func(f *installFixture) { f.store.snaps[f.id()].CommitSHA = "tampered" }},
		"stale snapshot":                           {CodeRollbackStale, 409, func(f *installFixture) { f.store.rows[f.id()].CommitSHA = "moved-on" }},
		"row blocked":                              {CodeBlocked, 409, func(f *installFixture) { f.store.rows[f.id()].Blocked = true }},
		"blocklisted release": {CodeBlocked, 409, func(f *installFixture) {
			f.inst.Catalog.current.Blocklist = []Block{{ID: f.id(), Version: "0.0.9", Reason: "bad"}}
		}},
		"yanked in Spark": {CodeReleaseRevoked, 409, func(f *installFixture) {
			f.detail["releases"] = []any{map[string]any{"version": "0.0.9", "installable": true, "yanked": true}}
		}},
		"gone from Spark": {CodeReleaseRevoked, 409, func(f *installFixture) {
			f.detail["releases"] = []any{map[string]any{"version": "0.1.0", "installable": true}}
		}},
		"no verified catalog": {CodeSparkDown, 503, func(f *installFixture) { f.inst.Catalog.current = nil }},
		"incompatible manifest": {"extension_incompatible", 422, func(f *installFixture) {
			s := f.store.snaps[f.id()]
			s.Manifest = withManifest(t, f, func(m map[string]any) { m["version"], m["mesh_api"] = "0.0.9", ">=9" })
			s.Seal()
		}},
		"snapshot would add permissions": {"consent_required", 409, func(f *installFixture) {
			f.store.rows[f.id()].Manifest = withManifest(t, f, func(m map[string]any) { m["capabilities"] = map[string]any{} })
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, _ := updatedFixture(t)
			id := f.id()
			tc.break_(f)
			before := *f.store.rows[id]
			changes := f.changes
			_, err := f.inst.Rollback(context.Background(), nil, id)
			require.Error(t, err)
			var ae *apierror.Error
			require.ErrorAs(t, err, &ae)
			assert.Equal(t, tc.status, ae.Code)
			assert.Equal(t, tc.code, ae.ErrorCode)
			assert.Equal(t, before, *f.store.rows[id], "a refused rollback mutates nothing")
			assert.Equal(t, changes, f.changes)
		})
	}
}

func TestRollback_OnlyCatalogRows(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.store.rows["tg"] = &domain.InstalledExtension{ExtensionID: "tg", Source: domain.ExtensionSourceRepo}
	_, err := f.inst.Rollback(context.Background(), nil, "tg")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusNotFound, ae.Code, "a repo row is the repo service's: handler falls through only on 404")
	_, err = f.inst.Rollback(context.Background(), nil, "ghost")
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusNotFound, ae.Code)
}

// failStore makes one store call fail so the error paths of Rollback and the
// bulk update are exercised.
type failStore struct {
	*installMem
	getErr, snapErr, skipErr, rollbackErr, updateErr error
}

func (s *failStore) Get(ctx context.Context, id string) (*domain.InstalledExtension, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.installMem.Get(ctx, id)
}

func (s *failStore) GetCatalogSnapshot(ctx context.Context, id string) (*domain.CatalogSnapshot, error) {
	if s.snapErr != nil {
		return nil, s.snapErr
	}
	return s.installMem.GetCatalogSnapshot(ctx, id)
}

func (s *failStore) SkippedCommit(ctx context.Context, id string) (string, error) {
	if s.skipErr != nil {
		return "", s.skipErr
	}
	return s.installMem.SkippedCommit(ctx, id)
}

func (s *failStore) RollbackCatalog(ctx context.Context, id string, g domain.CatalogRollbackGuard, sn domain.CatalogSnapshot, j *domain.InstanceExtensionJournalEntry) error {
	if s.rollbackErr != nil {
		return s.rollbackErr
	}
	return s.installMem.RollbackCatalog(ctx, id, g, sn, j)
}

func (s *failStore) UpdateCatalog(ctx context.Context, e *domain.InstalledExtension, g domain.CatalogUpdateGuard) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.installMem.UpdateCatalog(ctx, e, g)
}

func TestRollback_StoreAndSparkErrors(t *testing.T) {
	boom := errors.New("db down")
	cases := map[string]struct {
		prep func(f *installFixture, s *failStore)
		code string
	}{
		"row read fails":      {func(_ *installFixture, s *failStore) { s.getErr = boom }, ""},
		"snapshot read fails": {func(_ *installFixture, s *failStore) { s.snapErr = boom }, CodeSnapshotCorrupt},
		"guarded write loses the race": {func(_ *installFixture, s *failStore) {
			s.rollbackErr = apierror.Conflict("the extension changed or has nothing to roll back to")
		}, ""},
		"snapshot manifest unparseable": {func(f *installFixture, s *failStore) {
			sn := s.snaps[f.id()]
			sn.Manifest = []byte(`{"id":`)
			sn.Seal()
		}, CodeSnapshotCorrupt},
		"snapshot manifest names another version": {func(f *installFixture, s *failStore) {
			sn := s.snaps[f.id()]
			sn.Manifest = withManifest(t, f, func(m map[string]any) { m["version"] = "0.0.1" })
			sn.Seal()
		}, CodeSnapshotCorrupt},
		"Spark unreachable (fails closed)": {func(f *installFixture, _ *failStore) {
			gone := httptest.NewServer(http.NotFoundHandler())
			u := gone.URL
			gone.Close()
			f.inst.SparkURL = func() string { return u }
		}, CodeSparkDown},
		"signed catalog lists the release with another digest": {func(f *installFixture, _ *failStore) {
			other := "another-digest"
			e := &f.inst.Catalog.current.Extensions[0]
			e.Latest.Version, e.Latest.ArtifactSHA256 = "0.0.9", &other
		}, CodeReleaseRevoked},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, _ := updatedFixture(t)
			fs := &failStore{installMem: f.store}
			f.inst.Store = fs
			tc.prep(f, fs)
			before := *f.store.rows[f.id()]
			_, err := f.inst.Rollback(context.Background(), nil, f.id())
			require.Error(t, err)
			if tc.code != "" {
				assert.Equal(t, tc.code, rollbackCode(t, err))
			}
			assert.Equal(t, before, *f.store.rows[f.id()])
		})
	}
}

func TestUpdate_StoreErrorsOnTheUpdatePath(t *testing.T) {
	boom := errors.New("db down")
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	fs := &failStore{installMem: f.store, skipErr: boom}
	f.inst.Store = fs

	res, err := f.inst.UpdateAll(context.Background(), nil)
	require.NoError(t, err)
	assert.NotEmpty(t, res[0].Error, "the bulk pass reports the failed skip lookup and updates nothing")
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version)

	fs.skipErr, fs.updateErr = nil, apierror.Conflict("the extension changed while the update was prepared")
	_, err = f.inst.Update(context.Background(), nil, f.id(), "")
	require.Error(t, err)
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version)
}
