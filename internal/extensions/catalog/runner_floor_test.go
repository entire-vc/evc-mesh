package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
)

// fakeRunnerVersions is the Installer's own RunnerVersions interface fed from
// the test, standing in for the controller reports the repository reads.
type fakeRunnerVersions struct {
	versions []string
	err      error
}

func (f *fakeRunnerVersions) ReportedRunnerVersions(context.Context) ([]string, error) {
	return f.versions, f.err
}

// newRunnerFixture is the Agent Runner catalog row at 0.1.44 with 0.2.40 as
// the catalog's latest release, both installable in Spark. Spark is the only
// external boundary served over HTTP.
func newRunnerFixture(t *testing.T) *installFixture {
	t.Helper()
	f := newInstallFixtureAs(t, domain.RunnerExtensionID, nil)
	raw := withManifest(t, f, func(m map[string]any) { m["version"] = "0.2.40" })
	art := tarGz(t, map[string][]byte{"mesh-extension.json": raw})
	sum := sha256.Sum256(art)
	digest := hex.EncodeToString(sum[:])
	entry := &f.inst.Catalog.current.Extensions[0]
	entry.Latest.Version, entry.Latest.CommitSHA, entry.Latest.ArtifactSHA256 = "0.2.40", strings.Repeat("b", 40), &digest
	f.detail["releases"] = []any{
		map[string]any{"version": "0.2.40", "installable": true},
		map[string]any{"version": "0.1.44", "installable": true},
	}
	// mock: external boundary — Spark detail and release artifact HTTP API.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/0.2.40/artifact") {
			_, _ = w.Write(art)
			return
		}
		_ = json.NewEncoder(w).Encode(f.detail)
	}))
	t.Cleanup(srv.Close)
	f.inst.SparkURL = func() string { return srv.URL }
	f.installedAt("0.1.44", true)
	return f
}

func runnerRow(t *testing.T, f *installFixture) repoinstall.UpdateInfo {
	t.Helper()
	list, err := f.inst.Updates(context.Background())
	require.NoError(t, err)
	require.Len(t, list, 1)
	return list[0]
}

// AC1: Installed 0.1.44, a controller reports 0.2.55, the catalog's latest is
// 0.2.40: nothing older than the running Runner is offered, and the running
// version is in the answer.
func TestRunnerFloor_ReportedVersionHidesOlderUpdate(t *testing.T) {
	f := newRunnerFixture(t)
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{"0.2.55"}}
	got := runnerRow(t, f)
	assert.Equal(t, "0.1.44", got.CurrentVersion)
	assert.False(t, got.UpdateAvailable)
	assert.Empty(t, got.LatestVersion)
	assert.False(t, got.CanRollback)
	assert.Equal(t, []string{"0.2.55"}, got.ReportedVersions)

	// The hidden button cannot be bypassed: an explicit update is refused,
	// the bulk pass leaves the row alone, nothing is written either way.
	_, err := f.inst.Update(context.Background(), nil, domain.RunnerExtensionID, "")
	assert.Equal(t, CodeRunnerAhead, rollbackCode(t, err))
	_, err = f.inst.UpdateTarget(context.Background(), nil, domain.RunnerExtensionID, "", "0.2.40", strings.Repeat("b", 40))
	assert.Equal(t, CodeRunnerAhead, rollbackCode(t, err))
	res, err := f.inst.UpdateAll(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpToDate, res[0].Outcome)
	assert.Equal(t, "0.1.44", f.store.rows[domain.RunnerExtensionID].Version)
	assert.Empty(t, f.journal.actions)
	assert.Zero(t, f.changes)
}

// AC2 (red control): the same row without a controller report keeps today's
// answer, an update to 0.2.40.
func TestRunnerFloor_NoReportKeepsCatalogUpdate(t *testing.T) {
	for name, src := range map[string]RunnerVersions{
		"no source":  nil,
		"no report":  &fakeRunnerVersions{},
		"not semver": &fakeRunnerVersions{versions: []string{"dev", ""}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRunnerFixture(t)
			f.inst.RunnerVersions = src
			got := runnerRow(t, f)
			assert.True(t, got.UpdateAvailable)
			assert.Equal(t, "0.2.40", got.LatestVersion)
			assert.Empty(t, got.ReportedVersions)
		})
	}
}

// A report older than the release still lets the update through: the floor is
// the newest of row and reports, never a block on a real upgrade.
func TestRunnerFloor_OlderReportStillOffersNewerRelease(t *testing.T) {
	f := newRunnerFixture(t)
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{"0.2.9", "0.1.44", "0.2.9"}}
	got := runnerRow(t, f)
	assert.True(t, got.UpdateAvailable)
	assert.Equal(t, []string{"0.1.44", "0.2.9"}, got.ReportedVersions, "distinct, oldest first by semver")
	res, err := f.inst.Update(context.Background(), nil, domain.RunnerExtensionID, "")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
}

// The prod state of the source card: the row was moved 0.1.44 -> 0.2.40, the
// controllers run 0.2.55. Rolling back to 0.1.44 is not offered and refused;
// without the report the same rollback is offered (control).
func TestRunnerFloor_RollbackBelowRunningRefused(t *testing.T) {
	f := newRunnerFixture(t)
	res, err := f.inst.Update(context.Background(), nil, domain.RunnerExtensionID, "")
	require.NoError(t, err)
	require.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
	assert.True(t, runnerRow(t, f).CanRollback, "control: rollback offered without a report")

	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{"0.2.55"}}
	got := runnerRow(t, f)
	assert.Equal(t, "0.2.40", got.CurrentVersion)
	assert.Equal(t, "0.1.44", got.PreviousVersion)
	assert.False(t, got.CanRollback)
	assert.False(t, got.UpdateAvailable)
	_, err = f.inst.Rollback(context.Background(), nil, domain.RunnerExtensionID)
	assert.Equal(t, CodeRunnerAhead, rollbackCode(t, err))
	assert.Equal(t, "0.2.40", f.store.rows[domain.RunnerExtensionID].Version)

	// A report at the rollback target itself does not block it.
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{"0.1.44"}}
	assert.True(t, runnerRow(t, f).CanRollback)
}

// Only the Runner row is judged by controller reports.
func TestRunnerFloor_OtherExtensionsIgnoreReports(t *testing.T) {
	f := newInstallFixture(t, nil)
	f.installedAt("0.0.9", true)
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{"9.9.9"}}
	got := runnerRow(t, f)
	assert.True(t, got.UpdateAvailable)
	assert.Empty(t, got.ReportedVersions)
}

// An unreadable report source fails a write closed and leaves the tab
// readable with the catalog answer.
func TestRunnerFloor_SourceErrorFailsWritesClosed(t *testing.T) {
	f := newRunnerFixture(t)
	f.inst.RunnerVersions = &fakeRunnerVersions{err: errors.New("db down")}
	got := runnerRow(t, f)
	assert.True(t, got.UpdateAvailable)
	_, err := f.inst.Update(context.Background(), nil, domain.RunnerExtensionID, "")
	require.Error(t, err)
	assert.Equal(t, "0.1.44", f.store.rows[domain.RunnerExtensionID].Version)
}
