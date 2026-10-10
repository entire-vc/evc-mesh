package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func quotaObservation(pool, window, status string, observed time.Time) domain.RuntimeQuotaObservation {
	return domain.RuntimeQuotaObservation{Pool: pool, Provider: "provider-a", Window: window, Unit: "fraction", ObservedAt: observed, Source: "provider_headers", Confidence: "high", MaxAgeSeconds: 600, Status: status}
}

func TestRuntimeRepoQuotaObservations(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	now := time.Now()
	report := func(obs ...domain.RuntimeQuotaObservation) domain.RuntimeQuotaReport {
		return domain.RuntimeQuotaReport{SchemaVersion: domain.RuntimeQuotaSchemaVersion, Observations: obs}
	}

	t.Run("identity", func(t *testing.T) {
		r := report(quotaObservation("shared-window", "5h", "unknown", now.Add(-time.Minute)))
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.ownerActor, r), "a human cannot report")
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.worker, r), "another workspace's agent cannot report")
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", RuntimeActor{AgentID: f.reporter.AgentID, AuthWorkspaceID: f.owner, Connector: true}, r))
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "nope", f.reporter, r))
		other := RuntimeActor{AgentID: uuid.New(), AuthWorkspaceID: f.owner}
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", other, r), "an agent that is not the controller's reporter")

		_, err := f.repo.QuotaObservations(ctx, f.owner, "runner-a", f.worker)
		require.Error(t, err)
		_, err = f.repo.QuotaObservations(ctx, f.owner, "runner-a", RuntimeActor{Connector: true})
		require.Error(t, err)
		_, err = f.repo.QuotaObservations(ctx, f.owner, "runner-a", f.receiverActor)
		require.Error(t, err, "a human of another workspace cannot read")
		_, err = f.repo.QuotaObservations(ctx, f.owner, "nope", f.ownerActor)
		require.Error(t, err)
		_, err = f.repo.QuotaObservations(ctx, f.owner, "runner-a", other)
		require.Error(t, err)
	})

	t.Run("rejected batch stores nothing", func(t *testing.T) {
		good := quotaObservation("shared-window", "5h", "unknown", now.Add(-time.Minute))
		mismatch := quotaObservation("shared-window", "7d", "unknown", now.Add(-time.Minute))
		mismatch.Provider = "provider-z"
		dup := quotaObservation("another-credential-view", "5h", "unknown", now.Add(-time.Minute))
		future := quotaObservation("shared-window", "7d", "unknown", now.Add(time.Hour))
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, report(good, mismatch)))
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, report(good, dup)))
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, report(good, future)))
		require.Error(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, report(good, quotaObservation("not-a-pool", "5h", "unknown", now.Add(-time.Minute)))))
		list, err := f.repo.QuotaObservations(ctx, f.owner, "runner-a", f.ownerActor)
		require.NoError(t, err)
		require.Empty(t, list.Observations)
	})

	t.Run("null is not zero, stale, older never overwrites newer", func(t *testing.T) {
		fraction, remaining, reset := 0.42, 58.0, now.Add(24*time.Hour)
		unknown := quotaObservation("another-credential-view", "5h", "unknown", now.Add(-time.Minute))
		measured := quotaObservation("long-window", "7d", "available", now.Add(-time.Hour))
		measured.UsedFraction, measured.Remaining, measured.ResetAt = &fraction, &remaining, &reset
		require.NoError(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, report(unknown, measured)))

		for _, actor := range []RuntimeActor{f.reporter, f.ownerActor} {
			list, err := f.repo.QuotaObservations(ctx, f.owner, "runner-a", actor)
			require.NoError(t, err)
			require.Len(t, list.Observations, 2)
			long, shared := list.Observations[0], list.Observations[1]
			require.Equal(t, "long-window", long.Pool)
			require.NotNil(t, long.UsedFraction)
			require.InDelta(t, 0.42, *long.UsedFraction, 1e-9)
			require.True(t, long.Stale, "observed 1h ago with max age 10m")
			require.Equal(t, "shared-window", shared.Pool, "alias stored under the canonical pool")
			require.Nil(t, shared.UsedFraction, "unknown stays null")
			require.Nil(t, shared.Remaining)
			require.Nil(t, shared.ResetAt)
			require.False(t, shared.Stale)
		}

		older := quotaObservation("shared-window", "5h", "exhausted", now.Add(-time.Hour))
		require.NoError(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, report(older)))
		list, err := f.repo.QuotaObservations(ctx, f.owner, "runner-a", f.ownerActor)
		require.NoError(t, err)
		require.Equal(t, "unknown", list.Observations[1].Status, "older observation must not replace a newer one")

		newer := quotaObservation("shared-window", "5h", "exhausted", now.Add(-time.Second))
		require.NoError(t, f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, report(newer)))
		list, err = f.repo.QuotaObservations(ctx, f.owner, "runner-a", f.ownerActor)
		require.NoError(t, err)
		require.Equal(t, "exhausted", list.Observations[1].Status)
	})

	t.Run("revoked reporter grant serves nothing", func(t *testing.T) {
		_, err := f.db.ExecContext(ctx, `UPDATE agent_workspace_grants SET revoked_at=now() WHERE agent_id=$1`, f.reporter.AgentID)
		require.NoError(t, err)
		_, err = f.repo.QuotaObservations(ctx, f.owner, "runner-a", f.reporter)
		require.Error(t, err)
		list, err := f.repo.QuotaObservations(ctx, f.owner, "runner-a", f.ownerActor)
		require.NoError(t, err)
		require.Empty(t, list.Observations)
	})
}
