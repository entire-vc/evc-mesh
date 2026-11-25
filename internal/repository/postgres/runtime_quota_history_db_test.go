package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

type quotaHourRow struct {
	Window       string     `db:"quota_window"`
	WindowRaw    string     `db:"window_raw"`
	Hour         time.Time  `db:"hour"`
	UsedLast     *float64   `db:"used_last"`
	UsedMax      *float64   `db:"used_max"`
	ResetAtLast  *time.Time `db:"reset_at_last"`
	StatusLast   string     `db:"status_last"`
	Observations int        `db:"observations"`
	Controller   string     `db:"controller_ref_last"`
}

func quotaHours(t *testing.T, f runtimeFixture, pool string) []quotaHourRow {
	t.Helper()
	var rows []quotaHourRow
	require.NoError(t, f.db.SelectContext(context.Background(), &rows, `SELECT h.quota_window,h.window_raw,h.hour,h.used_last,h.used_max,h.reset_at_last,h.status_last,h.observations,h.controller_ref_last
		FROM runtime_quota_hourly h JOIN integration_configs i ON i.id=h.integration_id WHERE i.workspace_id=$1 AND h.pool_ref=$2 ORDER BY h.quota_window,h.hour`, f.owner, pool))
	return rows
}

func measuredQuota(pool, window string, used float64, observed time.Time) domain.RuntimeQuotaObservation {
	o := quotaObservation(pool, window, "available", observed)
	o.UsedFraction = &used
	return o
}

func TestNormalizeQuotaWindow(t *testing.T) {
	for raw, want := range map[string]string{"5h": "5h", "7d": "week", "1w": "week", "month": "month", "budget": "budget", "primary": "other", "unknown": "other", "30d": "other"} {
		require.Equal(t, want, domain.NormalizeQuotaWindow(raw), raw)
	}
}

func TestRuntimeQuotaHourlyHistory(t *testing.T) {
	f := seedRuntime(t)
	ctx := context.Background()
	_, err := f.repo.Save(ctx, f.owner, f.ownerActor, f.input)
	require.NoError(t, err)
	report := func(obs ...domain.RuntimeQuotaObservation) error {
		return f.repo.ReportQuota(ctx, f.owner, "runner-a", f.reporter, domain.RuntimeQuotaReport{SchemaVersion: domain.RuntimeQuotaSchemaVersion, Observations: obs})
	}
	// A fixed hour well inside the past, so both observations land in it.
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)

	t.Run("observation writes its hour in the same transaction", func(t *testing.T) {
		reset := hour.Add(5 * time.Hour)
		o := measuredQuota("long-window", "7d", 0.4, hour.Add(10*time.Minute))
		o.ResetAt = &reset
		require.NoError(t, report(o))
		rows := quotaHours(t, f, "long-window")
		require.Len(t, rows, 1)
		require.Equal(t, "week", rows[0].Window)
		require.Equal(t, "7d", rows[0].WindowRaw)
		require.True(t, rows[0].Hour.Equal(hour), "hour is the UTC hour of observed_at")
		require.InDelta(t, 0.4, *rows[0].UsedLast, 1e-9)
		require.InDelta(t, 0.4, *rows[0].UsedMax, 1e-9)
		require.True(t, rows[0].ResetAtLast.Equal(reset))
		require.Equal(t, "available", rows[0].StatusLast)
		require.Equal(t, "runner-a", rows[0].Controller)
		require.Equal(t, 1, rows[0].Observations)

		// Same transaction: when the latest-observation write of a later
		// observation in the batch fails, the hour of the first is gone too.
		// The injected failure is scoped to this fixture's integration, so
		// tests running beside it on the same database are not touched.
		var integration uuid.UUID
		require.NoError(t, f.db.GetContext(ctx, &integration, `SELECT integration_id FROM runtime_quota_observations o JOIN integration_configs i ON i.id=o.integration_id WHERE i.workspace_id=$1 LIMIT 1`, f.owner))
		// nosemgrep: go.lang.security.audit.database.string-formatted-query.string-formatted-query -- Test-only DDL cannot bind parameters; pq quotes the fixture-generated UUID literal.
		_, err := f.db.ExecContext(ctx, `CREATE OR REPLACE FUNCTION quota_hourly_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.pool_ref='reserve-window' AND NEW.integration_id=`+pq.QuoteLiteral(integration.String())+`::uuid THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$`)
		require.NoError(t, err)
		_, err = f.db.ExecContext(ctx, `CREATE TRIGGER quota_hourly_test_fail BEFORE INSERT ON runtime_quota_observations FOR EACH ROW EXECUTE FUNCTION quota_hourly_test_fail()`)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = f.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS quota_hourly_test_fail ON runtime_quota_observations`)
			_, _ = f.db.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS quota_hourly_test_fail()`)
		})
		first := measuredQuota("shared-window", "5h", 0.2, hour.Add(time.Minute))
		require.ErrorContains(t, report(first, measuredQuota("reserve-window", "5h", 0.1, hour.Add(time.Minute))), "injected failure")
		require.Empty(t, quotaHours(t, f, "shared-window"), "a failed batch leaves no history row")
		var count int
		require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM runtime_quota_observations WHERE integration_id=$1 AND pool_ref='shared-window'`, integration))
		require.Zero(t, count, "latest observation is rolled back too")
		_, err = f.db.ExecContext(ctx, `DROP TRIGGER quota_hourly_test_fail ON runtime_quota_observations`)
		require.NoError(t, err)
	})

	t.Run("two observations in an hour: larger used_max, newest used_last", func(t *testing.T) {
		require.NoError(t, report(measuredQuota("long-window", "7d", 0.9, hour.Add(20*time.Minute))))
		require.NoError(t, report(measuredQuota("long-window", "7d", 0.6, hour.Add(30*time.Minute))))
		// An older observation of the same hour arriving late raises the max
		// it carries but does not become the "last" value.
		late := measuredQuota("long-window", "7d", 0.95, hour.Add(15*time.Minute))
		require.NoError(t, report(late))
		rows := quotaHours(t, f, "long-window")
		require.Len(t, rows, 1)
		require.InDelta(t, 0.95, *rows[0].UsedMax, 1e-9)
		require.InDelta(t, 0.6, *rows[0].UsedLast, 1e-9, "used_last is the newest observation of the hour")
		require.Equal(t, 4, rows[0].Observations)
		require.Nil(t, rows[0].ResetAtLast, "reset_at follows the newest observation")

		// An exact resend of the newest observation is not counted again.
		require.NoError(t, report(measuredQuota("long-window", "7d", 0.6, hour.Add(30*time.Minute))))
		require.Equal(t, 4, quotaHours(t, f, "long-window")[0].Observations)
	})

	t.Run("status unknown is NULL, never 0, and keeps the max", func(t *testing.T) {
		require.NoError(t, report(measuredQuota("shared-window", "5h", 0.3, hour.Add(40*time.Minute))))
		require.NoError(t, report(quotaObservation("shared-window", "5h", "unknown", hour.Add(50*time.Minute))))
		rows := quotaHours(t, f, "shared-window")
		require.Len(t, rows, 1)
		require.Nil(t, rows[0].UsedLast, "unknown is stored as NULL")
		require.InDelta(t, 0.3, *rows[0].UsedMax, 1e-9, "GREATEST skips NULL")
		require.Equal(t, "unknown", rows[0].StatusLast)

		// An hour that only ever saw unknown has no value at all.
		require.NoError(t, report(quotaObservation("shared-window", "5h", "network_error", hour.Add(time.Hour+time.Minute))))
		rows = quotaHours(t, f, "shared-window")
		require.Len(t, rows, 2)
		require.Nil(t, rows[1].UsedMax)
		require.Nil(t, rows[1].UsedLast)

		exhausted := measuredQuota("shared-window", "5h", 0.99, hour.Add(time.Hour+2*time.Minute))
		exhausted.Status = "exhausted"
		require.NoError(t, report(exhausted))
		rows = quotaHours(t, f, "shared-window")
		require.NotNil(t, rows[1].UsedLast, "exhausted with a share is measured: the account is at its limit")
		require.InDelta(t, 0.99, *rows[1].UsedLast, 1e-9)
		require.NotNil(t, rows[1].UsedMax)
		require.InDelta(t, 0.99, *rows[1].UsedMax, 1e-9)
		require.Equal(t, "exhausted", rows[1].StatusLast)

		// exhausted without a share is still NULL, never 0, and keeps the hour's max.
		require.NoError(t, report(quotaObservation("shared-window", "5h", "exhausted", hour.Add(time.Hour+3*time.Minute))))
		rows = quotaHours(t, f, "shared-window")
		require.Nil(t, rows[1].UsedLast, "exhausted without used_fraction has no value")
		require.InDelta(t, 0.99, *rows[1].UsedMax, 1e-9)
	})

	t.Run("7d and 1w are one week, unknown names are other with the raw name", func(t *testing.T) {
		require.NoError(t, report(measuredQuota("reserve-window", "1w", 0.5, hour.Add(5*time.Minute)), measuredQuota("reserve-window", "primary", 0.7, hour.Add(5*time.Minute))))
		require.NoError(t, report(measuredQuota("reserve-window", "7d", 0.8, hour.Add(6*time.Minute))))
		rows := quotaHours(t, f, "reserve-window")
		require.Len(t, rows, 2, "1w and 7d share the week row")
		require.Equal(t, "other", rows[0].Window)
		require.Equal(t, "primary", rows[0].WindowRaw)
		require.Equal(t, "week", rows[1].Window)
		require.Equal(t, "7d", rows[1].WindowRaw, "window_raw follows the newest observation")
		require.InDelta(t, 0.8, *rows[1].UsedMax, 1e-9)
		require.Equal(t, 2, rows[1].Observations)
	})

	t.Run("rows past 400 days are deleted by the writer", func(t *testing.T) {
		var integration uuid.UUID
		require.NoError(t, f.db.GetContext(ctx, &integration, `SELECT integration_id FROM runtime_quota_observations o JOIN integration_configs i ON i.id=o.integration_id WHERE i.workspace_id=$1 LIMIT 1`, f.owner))
		old := time.Now().UTC().Truncate(time.Hour).Add(-401 * 24 * time.Hour)
		kept := time.Now().UTC().Truncate(time.Hour).Add(-399 * 24 * time.Hour)
		for _, h := range []time.Time{old, kept} {
			_, err := f.db.ExecContext(ctx, `INSERT INTO runtime_quota_hourly(integration_id,pool_ref,quota_window,hour,window_raw,status_last,controller_ref_last,observed_at_last) VALUES($1,'long-window','5h',$2,'5h','unknown','runner-a',$2)`, integration, h)
			require.NoError(t, err)
		}
		require.NoError(t, report(quotaObservation("long-window", "5h", "unknown", time.Now().Add(-time.Minute))))
		var hours []time.Time
		require.NoError(t, f.db.SelectContext(ctx, &hours, `SELECT hour FROM runtime_quota_hourly WHERE integration_id=$1 AND quota_window='5h' AND pool_ref='long-window' ORDER BY hour`, integration))
		require.Len(t, hours, 2)
		require.True(t, hours[0].Equal(kept), "399 days stays, 401 days is gone")
	})

	t.Run("analytics readers: owner/admin and runtime managers only", func(t *testing.T) {
		from, to := hour.Add(-time.Hour), time.Now()
		data, err := f.repo.QuotaAnalytics(ctx, f.owner, f.ownerActor, "week", from, to)
		require.NoError(t, err)
		require.Equal(t, []QuotaPoolRef{{"long-window", "provider-a"}, {"reserve-window", "provider-a"}, {"shared-window", "provider-a"}}, data.Pools)
		require.NotEmpty(t, data.History)
		for _, h := range data.History {
			require.NotEqual(t, "shared-window", h.Pool, "only the asked window")
		}

		member := insertWorkspaceUser(t, f, "member")
		admin := insertWorkspaceUser(t, f, "admin")
		ok, err := f.repo.CanSeeQuota(ctx, f.owner, admin)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = f.repo.QuotaAnalytics(ctx, f.owner, admin, "week", from, to)
		require.NoError(t, err)

		for name, actor := range map[string]RuntimeActor{"member": member, "reporter key without runtime_manager": f.reporter, "connector": {UserID: f.ownerActor.UserID, Connector: true}, "other workspace owner": f.receiverActor} {
			ok, err = f.repo.CanSeeQuota(ctx, f.owner, actor)
			require.NoError(t, err, name)
			require.False(t, ok, name)
			_, err = f.repo.QuotaAnalytics(ctx, f.owner, actor, "week", from, to)
			require.Error(t, err, name)
			require.Contains(t, err.Error(), "403", name)
		}

		// An agent key of an admin operator with a live runtime_manager grant reads.
		_, err = f.db.ExecContext(ctx, `UPDATE agents SET operator_user_id=$1 WHERE id=$2`, f.ownerActor.UserID, f.reporter.AgentID)
		require.NoError(t, err)
		_, err = f.db.ExecContext(ctx, `INSERT INTO user_runtime_managers(operator_user_id,agent_id) VALUES($1,$2)`, f.ownerActor.UserID, f.reporter.AgentID)
		require.NoError(t, err)
		ok, err = f.repo.CanSeeQuota(ctx, f.owner, f.reporter)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = f.repo.QuotaAnalytics(ctx, f.owner, f.reporter, "week", from, to)
		require.NoError(t, err)
	})
}

func insertWorkspaceUser(t *testing.T, f runtimeFixture, role string) RuntimeActor {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	suffix := id.String()[:8]
	_, err := f.db.ExecContext(ctx, `INSERT INTO users (id,email,password_hash,display_name,username,is_active) VALUES ($1,$2,'x','Quota '||$3,$4,true)`, id, "quota-"+suffix+"@example.com", role, "quota-"+suffix)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `INSERT INTO workspace_members (id,workspace_id,user_id,role) VALUES ($1,$2,$3,$4)`, uuid.New(), f.owner, id, role)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = f.db.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
	return RuntimeActor{UserID: id}
}
