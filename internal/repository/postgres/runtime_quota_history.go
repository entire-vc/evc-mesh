package postgres

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// quotaHistoryRetention bounds runtime_quota_hourly: about nine pools by three
// windows by 24 hours by 400 days stays near a quarter million rows.
const quotaHistoryRetention = 400 * 24 * time.Hour

// recordQuotaHour folds one accepted observation into its hour of the
// history, inside the caller's ReportQuota transaction. used_max is the
// largest measured share of the hour (GREATEST skips NULL), the *_last fields
// follow the newest observation of the hour, and an exact resend of the hour's
// newest observation is not counted twice.
func recordQuotaHour(ctx context.Context, tx *sqlx.Tx, integration uuid.UUID, ref string, o domain.RuntimeQuotaObservation) error {
	used := o.HistoryUsed()
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_quota_hourly AS h (integration_id,pool_ref,quota_window,hour,window_raw,used_last,used_max,reset_at_last,status_last,observations,controller_ref_last,observed_at_last)
		VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8,1,$9,$10)
		ON CONFLICT (integration_id,pool_ref,quota_window,hour) DO UPDATE SET
			used_max=GREATEST(h.used_max,EXCLUDED.used_max),
			observations=h.observations+1,
			used_last=CASE WHEN EXCLUDED.observed_at_last>=h.observed_at_last THEN EXCLUDED.used_last ELSE h.used_last END,
			reset_at_last=CASE WHEN EXCLUDED.observed_at_last>=h.observed_at_last THEN EXCLUDED.reset_at_last ELSE h.reset_at_last END,
			status_last=CASE WHEN EXCLUDED.observed_at_last>=h.observed_at_last THEN EXCLUDED.status_last ELSE h.status_last END,
			window_raw=CASE WHEN EXCLUDED.observed_at_last>=h.observed_at_last THEN EXCLUDED.window_raw ELSE h.window_raw END,
			controller_ref_last=CASE WHEN EXCLUDED.observed_at_last>=h.observed_at_last THEN EXCLUDED.controller_ref_last ELSE h.controller_ref_last END,
			observed_at_last=GREATEST(h.observed_at_last,EXCLUDED.observed_at_last)
		WHERE (h.observed_at_last,h.controller_ref_last,h.window_raw,h.used_last,h.reset_at_last,h.status_last) IS DISTINCT FROM (EXCLUDED.observed_at_last,EXCLUDED.controller_ref_last,EXCLUDED.window_raw,EXCLUDED.used_last,EXCLUDED.reset_at_last,EXCLUDED.status_last)`,
		integration, o.Pool, domain.NormalizeQuotaWindow(o.Window), o.ObservedAt.UTC().Truncate(time.Hour), o.Window,
		used, o.ResetAt, o.Status, ref, o.ObservedAt)
	return err
}

// QuotaPoolRef is one pool the analytics can show, with its provider.
type QuotaPoolRef struct {
	Pool     string
	Provider string
}

// QuotaLatest is the newest stored observation of one pool and raw window
// (across controllers), as runtime_quota_observations holds it.
type QuotaLatest struct {
	Pool          string     `db:"pool_ref"`
	Provider      string     `db:"provider"`
	WindowRaw     string     `db:"window_ref"`
	UsedFraction  *float64   `db:"used_fraction"`
	ResetAt       *time.Time `db:"reset_at"`
	ObservedAt    time.Time  `db:"observed_at"`
	MaxAgeSeconds int        `db:"max_age_seconds"`
	Status        string     `db:"status"`
	ControllerRef string     `db:"controller_ref"`
}

// QuotaAccountRef is one catalog account the analytics shows as a row: its
// provider and its canonical pools across all execution modes, sorted.
type QuotaAccountRef struct {
	Account  string
	Provider string
	Pools    []string
}

// QuotaHour is one hour of one pool's history in the asked window.
type QuotaHour struct {
	Pool    string    `db:"pool_ref"`
	Hour    time.Time `db:"hour"`
	UsedMax *float64  `db:"used_max"`
}

// QuotaAnalyticsData is what GET .../analytics/quota is built from.
type QuotaAnalyticsData struct {
	Accounts []QuotaAccountRef
	Pools    []QuotaPoolRef
	Latest   []QuotaLatest
	History  []QuotaHour
}

// CanSeeQuota answers whether actor may read the Runner's quota history of ws:
// exactly who may read the Runner catalog (a workspace owner/admin, or an
// agent key holding its admin operator's live runtime_manager grant).
// A refusal is false, not an error.
func (r *RuntimeRepo) CanSeeQuota(ctx context.Context, ws uuid.UUID, actor RuntimeActor) (bool, error) {
	_, err := runtimeCatalogEditor(ctx, r.db, ws, actor)
	if err == nil {
		return true, nil
	}
	var denied *apierror.Error
	if errors.As(err, &denied) && denied.Code == http.StatusForbidden {
		return false, nil
	}
	return false, err
}

// QuotaAnalytics reads the catalog accounts with their pools, the pools, the latest observation of every pool and
// window, and the hourly history of window in [from, to). Readable only by
// CanSeeQuota; everyone else gets 403.
func (r *RuntimeRepo) QuotaAnalytics(ctx context.Context, ws uuid.UUID, actor RuntimeActor, window string, from, to time.Time) (*QuotaAnalyticsData, error) {
	if _, err := runtimeCatalogEditor(ctx, r.db, ws, actor); err != nil {
		return nil, err
	}
	out := &QuotaAnalyticsData{Accounts: []QuotaAccountRef{}, Pools: []QuotaPoolRef{}, Latest: []QuotaLatest{}, History: []QuotaHour{}}
	connection, err := runtimeConnection(ctx, r.db, ws, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return out, nil
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, err
	}
	for _, ref := range slices.Sorted(maps.Keys(catalog.Pools)) {
		out.Pools = append(out.Pools, QuotaPoolRef{Pool: ref, Provider: catalog.Pools[ref].Provider})
	}
	for _, ref := range slices.Sorted(maps.Keys(catalog.Accounts)) {
		account := catalog.Accounts[ref]
		pools := []string{}
		for _, refs := range account.QuotaPoolsByMode {
			for _, pool := range refs {
				if name, ok := catalog.ResolvePool(pool); ok && !slices.Contains(pools, name) {
					pools = append(pools, name)
				}
			}
		}
		slices.Sort(pools)
		out.Accounts = append(out.Accounts, QuotaAccountRef{Account: ref, Provider: account.Provider, Pools: pools})
	}
	if err := sqlx.SelectContext(ctx, r.db, &out.Latest, `SELECT DISTINCT ON (pool_ref,window_ref) pool_ref,provider,window_ref,used_fraction,reset_at,observed_at,max_age_seconds,status,controller_ref
		FROM runtime_quota_observations WHERE integration_id=$1 ORDER BY pool_ref,window_ref,observed_at DESC,controller_ref`, connection.ID); err != nil {
		return nil, err
	}
	if err := sqlx.SelectContext(ctx, r.db, &out.History, `SELECT pool_ref,hour,used_max FROM runtime_quota_hourly
		WHERE integration_id=$1 AND quota_window=$2 AND hour>=$3 AND hour<$4 ORDER BY pool_ref,hour`, connection.ID, window, from.UTC().Truncate(time.Hour), to); err != nil {
		return nil, err
	}
	return out, nil
}
