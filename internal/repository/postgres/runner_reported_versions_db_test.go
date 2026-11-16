package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// The Runner versions an instance reports are its controllers' fresh reports
// only: a report past the controller's heartbeat window no longer counts, and a
// report without a version adds nothing. Other tests share the database, so
// the checks look for this test's own version, never at the whole list.
func TestReportedRunnerVersions_FreshReportsOnly(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	snapshot, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	version := fmt.Sprintf("0.%d.55", time.Now().UnixNano()%1_000_000)
	report := domain.RuntimeReport{SchemaVersion: 2, Revision: snapshot.Revision, Digest: snapshot.Digest, Status: "applied",
		Capabilities: f.catalog.Controllers["runner-a"].Capabilities, Pools: map[string]domain.RuntimePoolObservation{}, Version: version}
	require.NoError(t, f.repo.Report(ctx, f.owner, "runner-a", f.reporter, report))
	var integ string
	require.NoError(t, f.db.GetContext(ctx, &integ, `SELECT integration_id::text FROM runtime_controller_reports WHERE report->>'version' = $1`, version))

	repo := NewInstalledExtensionRepo(f.db)
	got, err := repo.ReportedRunnerVersions(ctx)
	require.NoError(t, err)
	require.Contains(t, got, version, "a fresh report counts")
	require.NotContains(t, got, "")

	_, err = f.db.ExecContext(ctx, `UPDATE runtime_controller_reports SET received_at = now() - interval '1 day' WHERE integration_id=$1`, integ)
	require.NoError(t, err)
	got, err = repo.ReportedRunnerVersions(ctx)
	require.NoError(t, err)
	require.NotContains(t, got, version, "a report outside the heartbeat window does not count")

	_, err = f.db.ExecContext(ctx, `UPDATE runtime_controller_reports SET received_at = now(), report = report - 'version' WHERE integration_id=$1`, integ)
	require.NoError(t, err)
	got, err = repo.ReportedRunnerVersions(ctx)
	require.NoError(t, err)
	require.NotContains(t, got, "", "a report without a version adds nothing")
}
