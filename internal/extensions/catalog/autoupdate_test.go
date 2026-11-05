package catalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type autoPolicy struct {
	repository.InstanceExtensionPolicyRepository
	on bool
}

func (p autoPolicy) GetConfig(context.Context) (*domain.InstanceExtensionConfig, error) {
	return &domain.InstanceExtensionConfig{AutoUpdate: p.on}, nil
}

// emptyRepoRows lists the catalog fixture's rows, so the repo pass sees none
// of its own (source=catalog) and the timer entry point is the real one.
type emptyRepoRows struct {
	repository.InstalledExtensionRepository
	store *installMem
}

func (e emptyRepoRows) List(ctx context.Context) ([]domain.InstalledExtension, error) {
	return e.store.List(ctx)
}

// timerService is the production wiring: AutoUpdateOnce with the catalog hook.
func timerService(f *installFixture, on bool) *repoinstall.Service {
	return &repoinstall.Service{
		Installed:     emptyRepoRows{store: f.store},
		Policy:        autoPolicy{on: on},
		CatalogUpdate: func(ctx context.Context) ([]repoinstall.UpdateResult, error) { return f.inst.UpdateAll(ctx, nil) },
	}
}

func TestAutoUpdateTimer_UpdatesCatalogRowOnlyWhenEnabled(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	ctx := context.Background()

	timerService(f, false).AutoUpdateOnce(ctx)
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version, "auto_update=false updates nothing")
	assert.Equal(t, 0, f.changes)
}

func TestAutoUpdateTimer_AppliesAndKeepsGuards(t *testing.T) {
	// Same capabilities installed and offered: no consent needed.
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	ctx := context.Background()
	timerService(f, true).AutoUpdateOnce(ctx)
	row := f.store.rows[f.id()]
	assert.Equal(t, "0.1.0", row.Version)
	assert.Equal(t, "0.0.9", row.PreviousVersion)
	assert.Equal(t, "abc123", row.CommitSHA)
	assert.Equal(t, 1, f.changes)

	// Second cycle: nothing newer, no extra change callback.
	timerService(f, true).AutoUpdateOnce(ctx)
	assert.Equal(t, 1, f.changes)
}

func TestAutoUpdateTimer_NewCapabilitiesWaitForConsent(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", false)
	timerService(f, true).AutoUpdateOnce(context.Background())
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version)
	assert.Equal(t, 0, f.changes)
}

func TestAutoUpdateTimer_RollbackSkippedReleaseNotReapplied(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	f.store.skipped = "abc123"
	timerService(f, true).AutoUpdateOnce(context.Background())
	assert.Equal(t, "0.0.9", f.store.rows[f.id()].Version)
	assert.Equal(t, 0, f.changes)
}

func TestAutoUpdateTimer_UnsafeSourcesFailSafely(t *testing.T) {
	// Unavailable catalog (never verified).
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	id := f.id()
	f.inst.Catalog.current = nil
	require.NotPanics(t, func() { timerService(f, true).AutoUpdateOnce(context.Background()) })
	assert.Equal(t, "0.0.9", f.store.rows[id].Version)

	// Incompatible mesh API, blocked-by-digest (missing signature digest), yanked.
	for name, mut := range map[string]func(e *Entry, c *Catalog){
		"incompatible": func(e *Entry, _ *Catalog) { e.Latest.MeshAPI = ">=99" },
		"no digest":    func(e *Entry, _ *Catalog) { e.Latest.ArtifactSHA256 = nil },
		"yanked":       func(e *Entry, _ *Catalog) { e.Latest.Yanked = true },
	} {
		g := newInstallFixture(t, mut)
		g.installedAt("0.0.9", true)
		timerService(g, true).AutoUpdateOnce(context.Background())
		assert.Equal(t, "0.0.9", g.store.rows[g.id()].Version, name)
		assert.Equal(t, 0, g.changes, name)
	}

	// Artifact digest mismatch: nothing written.
	h := newInstallFixture(t, nil)
	h.installedAt("0.0.9", true)
	bad := "deadbeef"
	h.inst.Catalog.current.Extensions[0].Latest.ArtifactSHA256 = &bad
	timerService(h, true).AutoUpdateOnce(context.Background())
	assert.Equal(t, "0.0.9", h.store.rows[h.id()].Version)
	assert.Equal(t, 0, h.changes)
}
