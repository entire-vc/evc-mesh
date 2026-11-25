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
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
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
	return newRunnerFixtureAt(t, "0.1.44", "0.2.40", strings.Repeat("b", 40), true)
}

// newRunnerFixtureAt is the Agent Runner catalog row at installed with latest
// (commit) as the catalog's latest release, both installable in Spark. Without
// withCaps the row declares no permissions, so the release adds them.
func newRunnerFixtureAt(t *testing.T, installed, latest, commit string, withCaps bool) *installFixture {
	t.Helper()
	f := newInstallFixtureAs(t, domain.RunnerExtensionID, nil)
	raw := withManifest(t, f, func(m map[string]any) { m["version"] = latest })
	art := tarGz(t, map[string][]byte{"mesh-extension.json": raw})
	sum := sha256.Sum256(art)
	digest := hex.EncodeToString(sum[:])
	entry := &f.inst.Catalog.current.Extensions[0]
	entry.Latest.Version, entry.Latest.CommitSHA, entry.Latest.ArtifactSHA256 = latest, commit, &digest
	f.detail["releases"] = []any{
		map[string]any{"version": latest, "installable": true},
		map[string]any{"version": installed, "installable": true},
	}
	// mock: external boundary — Spark detail and release artifact HTTP API.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/"+latest+"/artifact") {
			_, _ = w.Write(art)
			return
		}
		_ = json.NewEncoder(w).Encode(f.detail)
	}))
	t.Cleanup(srv.Close)
	f.inst.SparkURL = func() string { return srv.URL }
	f.installedAt(installed, withCaps)
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

// The prod state behind adopting: the Runner was installed on its host apart
// from the catalog, so the row lags at 0.2.40 while every controller runs
// 0.2.117, the verified catalog's release. Constants name that state.
const (
	adoptRow    = "0.2.40"
	adoptLive   = "0.2.117"
	adoptCommit = "8cb6219cc35055de7cb7a8c1e75d5282c820c552"
)

func adoptFixture(t *testing.T, reported ...string) *installFixture {
	t.Helper()
	f := newRunnerFixtureAt(t, adoptRow, adoptLive, adoptCommit, true)
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: reported}
	return f
}

func pinnedLive(f *installFixture, consent string) (*repoinstall.UpdateResult, error) {
	return f.inst.UpdateTarget(context.Background(), nil, domain.RunnerExtensionID, consent, adoptLive, adoptCommit)
}

// assertRowUntouched: a refusal writes nothing, journals nothing, syncs nothing.
func assertRowUntouched(t *testing.T, f *installFixture) {
	t.Helper()
	row := f.store.rows[domain.RunnerExtensionID]
	assert.Equal(t, adoptRow, row.Version)
	assert.Equal(t, "old", row.CommitSHA)
	assert.Nil(t, f.store.snaps[domain.RunnerExtensionID])
	assert.Empty(t, f.journal.actions)
	assert.Zero(t, f.changes)
}

// AC2/AC7/AC8: row 0.2.40, every controller reports 0.2.117, an admin pins
// 0.2.117 from the verified catalog. Before the fix this was 409
// extension_runner_ahead; now the row adopts the live version.
func TestRunnerAdopt_PinnedLiveVersionIsAdopted(t *testing.T) {
	f := adoptFixture(t, adoptLive, adoptLive)
	before := runnerRow(t, f)
	assert.False(t, before.UpdateAvailable, "the button stays hidden: nothing newer than live")
	assert.Equal(t, []string{adoptLive}, before.ReportedVersions)

	res, err := pinnedLive(f, "")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeAdopted, res.Outcome)
	assert.Equal(t, adoptLive, res.Version)
	assert.Equal(t, adoptCommit, res.CommitSHA)

	// Re-read the stored row: moved as on an update, snapshot sealed on 0.2.40.
	row := f.store.rows[domain.RunnerExtensionID]
	assert.Equal(t, adoptLive, row.Version)
	assert.Equal(t, adoptCommit, row.CommitSHA)
	assert.Equal(t, adoptRow, row.PreviousVersion)
	m, err := extensions.ParseManifest(row.Manifest)
	require.NoError(t, err)
	assert.Equal(t, adoptLive, m.Version)
	snap := f.store.snaps[domain.RunnerExtensionID]
	require.NotNil(t, snap)
	assert.Equal(t, adoptRow, snap.Version)
	assert.Equal(t, adoptLive, snap.SupersededVersion)
	assert.True(t, snap.Intact())

	// Audit: one updated entry marked adopted, with the reports it relied on.
	require.Equal(t, []string{domain.RunnerExtensionID + ":updated"}, f.journal.actions)
	var d map[string]any
	require.NoError(t, json.Unmarshal(f.journal.details[0], &d))
	assert.Equal(t, "adopted", d["outcome"])
	assert.Equal(t, adoptRow, d["from"])
	assert.Equal(t, adoptLive, d["to"])
	assert.Equal(t, []any{adoptLive}, d["reported_versions"])
	assert.Equal(t, 1, f.changes, "registry sync as on an update; the desired controller version is what already runs")

	// AC8: the Updates tab names the live version. Rolling back below the
	// running Runner is refused by the floor, so can_rollback is false while
	// it runs 0.2.117 ...
	after := runnerRow(t, f)
	assert.Equal(t, adoptLive, after.CurrentVersion)
	assert.Equal(t, adoptCommit, after.CurrentCommit)
	assert.Equal(t, adoptRow, after.PreviousVersion)
	assert.False(t, after.UpdateAvailable)
	assert.False(t, after.CanRollback)
	// ... and true once the controllers run the snapshot's version again
	// (control: the snapshot itself is usable).
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{adoptRow}}
	assert.True(t, runnerRow(t, f).CanRollback)
}

// A normal update journals its outcome too, so the audit tells the two apart.
func TestRunnerAdopt_UpdateJournalsUpdatedOutcome(t *testing.T) {
	f := newRunnerFixture(t)
	res, err := f.inst.Update(context.Background(), nil, domain.RunnerExtensionID, "")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeUpdated, res.Outcome)
	var d map[string]any
	require.NoError(t, json.Unmarshal(f.journal.details[0], &d))
	assert.Equal(t, "updated", d["outcome"])
	assert.NotContains(t, d, "reported_versions")
}

// AC3/AC7: a pinned target older than the running Runner is still refused.
func TestRunnerAdopt_TargetOlderThanRunningRefused(t *testing.T) {
	f := adoptFixture(t, "0.2.118")
	_, err := pinnedLive(f, "")
	assert.Equal(t, CodeRunnerAhead, rollbackCode(t, err))
	assertRowUntouched(t, f)
}

// AC3/AC7: mixed reports are refused as before, whichever controller runs
// the target.
func TestRunnerAdopt_MixedReportsRefused(t *testing.T) {
	for name, reported := range map[string][]string{
		"older and target": {"0.2.116", adoptLive},
		"target and newer": {adoptLive, "0.2.118"},
	} {
		t.Run(name, func(t *testing.T) {
			f := adoptFixture(t, reported...)
			_, err := pinnedLive(f, "")
			assert.Equal(t, CodeRunnerAhead, rollbackCode(t, err))
			assertRowUntouched(t, f)
		})
	}
	// Non-semver reports are ignored by the floor, as on the Updates tab: the
	// distinct semver set is still exactly [target].
	f := adoptFixture(t, adoptLive, "dev", "")
	res, err := pinnedLive(f, "")
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeAdopted, res.Outcome)
}

// AC4/AC7: the live release is not in the verified catalog yet (its hourly
// refresh lags): the existing refusal, never an adopt.
func TestRunnerAdopt_ReleaseNotInVerifiedCatalogRefused(t *testing.T) {
	f := newRunnerFixtureAt(t, adoptRow, "0.2.116", strings.Repeat("c", 40), true)
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{adoptLive}}
	_, err := pinnedLive(f, "")
	assert.Equal(t, CodeReleaseRevoked, rollbackCode(t, err))
	assertRowUntouched(t, f)

	// A commit that is not the verified release's is refused as well.
	f = adoptFixture(t, adoptLive)
	_, err = f.inst.UpdateTarget(context.Background(), nil, domain.RunnerExtensionID, "", adoptLive, strings.Repeat("d", 40))
	require.Error(t, err)
	assertRowUntouched(t, f)
}

// AC2: the target must be newer than the row; a row already at the live
// version keeps the existing "not an eligible upgrade" answer.
func TestRunnerAdopt_RowAlreadyAtTargetRefused(t *testing.T) {
	f := newRunnerFixtureAt(t, adoptLive, adoptLive, adoptCommit, true)
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{adoptLive}}
	_, err := pinnedLive(f, "")
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusConflict, ae.Code)
	assert.NotEqual(t, CodeRunnerAhead, ae.ErrorCode)
	assert.Equal(t, adoptLive, f.store.rows[domain.RunnerExtensionID].Version)
	assert.Empty(t, f.journal.actions)
}

// AC1/AC5: only the explicit pinned request adopts. A plain update at the
// same state is still refused, and the bulk pass leaves the row alone.
func TestRunnerAdopt_OnlyPinnedRequestAdopts(t *testing.T) {
	f := adoptFixture(t, adoptLive)
	_, err := f.inst.Update(context.Background(), nil, domain.RunnerExtensionID, "")
	assert.Equal(t, CodeRunnerAhead, rollbackCode(t, err))
	all, err := f.inst.UpdateAll(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, repoinstall.OutcomeUpToDate, all[0].Outcome)
	assertRowUntouched(t, f)
}

// AC6: an adopt that adds permissions waits for consent like an update; with
// the matching commit it proceeds.
func TestRunnerAdopt_ConsentStillRequired(t *testing.T) {
	f := newRunnerFixtureAt(t, adoptRow, adoptLive, adoptCommit, false)
	f.inst.RunnerVersions = &fakeRunnerVersions{versions: []string{adoptLive}}

	for _, consent := range []string{"", strings.Repeat("e", 40)} {
		res, err := pinnedLive(f, consent)
		require.NoError(t, err)
		assert.Equal(t, repoinstall.OutcomePendingConsent, res.Outcome)
		assert.NotEmpty(t, res.NewCapabilities)
		assertRowUntouched(t, f)
	}

	res, err := pinnedLive(f, adoptCommit)
	require.NoError(t, err)
	assert.Equal(t, repoinstall.OutcomeAdopted, res.Outcome)
	assert.Equal(t, adoptLive, f.store.rows[domain.RunnerExtensionID].Version)
}
