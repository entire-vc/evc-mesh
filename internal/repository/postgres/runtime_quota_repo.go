package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type runtimeQuotaRow struct {
	PoolRef      string     `db:"pool_ref"`
	Window       string     `db:"window_ref"`
	Provider     string     `db:"provider"`
	Unit         string     `db:"unit"`
	UsedFraction *float64   `db:"used_fraction"`
	Remaining    *float64   `db:"remaining"`
	ResetAt      *time.Time `db:"reset_at"`
	ObservedAt   time.Time  `db:"observed_at"`
	Source       string     `db:"source"`
	Confidence   string     `db:"confidence"`
	MaxAge       int        `db:"max_age_seconds"`
	Status       string     `db:"status"`
	ErrorKind    *string    `db:"error_kind"`
	ReceivedAt   time.Time  `db:"received_at"`
}

// controllerPoolSet lists the canonical pools a controller may speak for: the
// pools of the accounts behind its own profiles.
func controllerPoolSet(catalog *domain.RuntimeCatalog, ref string) map[string]bool {
	pools := map[string]bool{}
	for _, p := range catalog.Profiles {
		if p.ControllerRef != ref {
			continue
		}
		for _, pool := range catalog.Accounts[p.AccountRef].QuotaPoolsByMode[p.ExecutionMode] {
			if name, ok := catalog.ResolvePool(pool); ok {
				pools[name] = true
			}
		}
	}
	return pools
}

// ReportQuota stores the latest detailed quota observation per pool and window.
// Additive to Report: same exact-reporter-grant identity, no revision pairing,
// never an admission decision. An older observation never overwrites a newer one.
func (r *RuntimeRepo) ReportQuota(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor, report domain.RuntimeQuotaReport) error {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return apierror.Forbidden("quota observations require the controller's exact owner-workspace agent key")
	}
	if err := report.Validate(time.Now()); err != nil {
		return err
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	connection, err := runtimeConnection(ctx, tx, ws, true)
	if err != nil {
		return err
	}
	if connection == nil {
		return apierror.NotFound("Runtime connection")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return err
	}
	controller, ok := catalog.Controllers[ref]
	if !ok {
		return apierror.NotFound("Runtime controller")
	}
	fingerprint, err := runtimeGrant(ctx, tx, domain.RuntimeIdentity{AgentID: actor.AgentID, WorkspaceID: ws, GrantID: controller.ReporterGrantID})
	if err != nil {
		return err
	}
	allowed := controllerPoolSet(catalog, ref)
	type key struct{ pool, window string }
	seen := map[key]bool{}
	canonical := make([]domain.RuntimeQuotaObservation, 0, len(report.Observations))
	for _, o := range report.Observations {
		pool, ok := catalog.ResolvePool(o.Pool)
		if !ok || !allowed[pool] {
			return apierror.Forbidden("controller cannot report an unrelated pool")
		}
		if catalog.Pools[pool].Provider != o.Provider {
			return apierror.BadRequest("observation provider does not match the pool")
		}
		k := key{pool, o.Window}
		if seen[k] {
			return apierror.BadRequest("duplicate pool window observation")
		}
		seen[k] = true
		o.Pool = pool
		canonical = append(canonical, o)
	}
	for _, o := range canonical {
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_quota_observations(integration_id,controller_ref,pool_ref,window_ref,provider,unit,used_fraction,remaining,reset_at,observed_at,source,confidence,max_age_seconds,status,error_kind,reporter_grant_id,grant_fingerprint)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			ON CONFLICT(integration_id,controller_ref,pool_ref,window_ref) DO UPDATE SET provider=EXCLUDED.provider,unit=EXCLUDED.unit,used_fraction=EXCLUDED.used_fraction,remaining=EXCLUDED.remaining,reset_at=EXCLUDED.reset_at,observed_at=EXCLUDED.observed_at,source=EXCLUDED.source,confidence=EXCLUDED.confidence,max_age_seconds=EXCLUDED.max_age_seconds,status=EXCLUDED.status,error_kind=EXCLUDED.error_kind,reporter_grant_id=EXCLUDED.reporter_grant_id,grant_fingerprint=EXCLUDED.grant_fingerprint,received_at=now()
			WHERE runtime_quota_observations.observed_at <= EXCLUDED.observed_at`,
			connection.ID, ref, o.Pool, o.Window, o.Provider, o.Unit, o.UsedFraction, o.Remaining, o.ResetAt, o.ObservedAt, o.Source, o.Confidence, o.MaxAgeSeconds, o.Status, o.ErrorKind, controller.ReporterGrantID, fingerprint)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QuotaObservations returns the latest observation per pool and window with
// stale computed from max_age. Readable by a workspace owner/admin (the UI) and
// by the controller's own reporter key. Rows written under a grant that has
// since been rotated or revoked are not served.
func (r *RuntimeRepo) QuotaObservations(ctx context.Context, ws uuid.UUID, ref string, actor RuntimeActor) (*domain.RuntimeQuotaList, error) {
	if actor.Connector {
		return nil, apierror.Forbidden("quota observations are not available to connectors")
	}
	if actor.AgentID == uuid.Nil {
		if err := runtimeAdmin(ctx, r.db, ws, actor); err != nil {
			return nil, err
		}
	} else if actor.AuthWorkspaceID != ws {
		return nil, apierror.Forbidden("quota observations require an owner-workspace key")
	}
	connection, err := runtimeConnection(ctx, r.db, ws, false)
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, apierror.NotFound("Runtime controller")
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		return nil, err
	}
	controller, ok := catalog.Controllers[ref]
	if !ok {
		return nil, apierror.NotFound("Runtime controller")
	}
	if actor.AgentID != uuid.Nil {
		if _, err = runtimeGrant(ctx, r.db, domain.RuntimeIdentity{AgentID: actor.AgentID, WorkspaceID: ws, GrantID: controller.ReporterGrantID}); err != nil {
			return nil, err
		}
	}
	// Observations written under a grant that was since rotated or revoked
	// no longer describe a trusted reporter: serve none of them.
	var reporter uuid.UUID
	if err = sqlx.GetContext(ctx, r.db, &reporter, `SELECT agent_id FROM agent_workspace_grants WHERE id=$1`, controller.ReporterGrantID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &domain.RuntimeQuotaList{ControllerRef: ref, Observations: []domain.RuntimeQuotaView{}}, nil
		}
		return nil, err
	}
	fingerprint, err := runtimeGrant(ctx, r.db, domain.RuntimeIdentity{AgentID: reporter, WorkspaceID: ws, GrantID: controller.ReporterGrantID})
	if err != nil {
		var denied *apierror.Error
		if errors.As(err, &denied) {
			return &domain.RuntimeQuotaList{ControllerRef: ref, Observations: []domain.RuntimeQuotaView{}}, nil
		}
		return nil, err
	}
	return r.quotaList(ctx, connection.ID, ref, controller.ReporterGrantID, fingerprint, time.Now())
}

func (r *RuntimeRepo) quotaList(ctx context.Context, integration uuid.UUID, ref string, grant uuid.UUID, fingerprint string, now time.Time) (*domain.RuntimeQuotaList, error) {
	var rows []runtimeQuotaRow
	err := sqlx.SelectContext(ctx, r.db, &rows, `SELECT pool_ref,window_ref,provider,unit,used_fraction,remaining,reset_at,observed_at,source,confidence,max_age_seconds,status,error_kind,received_at FROM runtime_quota_observations WHERE integration_id=$1 AND controller_ref=$2 AND reporter_grant_id=$3 AND grant_fingerprint=$4 ORDER BY pool_ref,window_ref`, integration, ref, grant, fingerprint)
	if err != nil {
		return nil, err
	}
	out := &domain.RuntimeQuotaList{ControllerRef: ref, Observations: make([]domain.RuntimeQuotaView, 0, len(rows))}
	for _, row := range rows {
		obs := domain.RuntimeQuotaObservation{Pool: row.PoolRef, Provider: row.Provider, Window: row.Window, Unit: row.Unit, UsedFraction: row.UsedFraction, Remaining: row.Remaining, ResetAt: row.ResetAt, ObservedAt: row.ObservedAt, Source: row.Source, Confidence: row.Confidence, MaxAgeSeconds: row.MaxAge, Status: row.Status, ErrorKind: row.ErrorKind}
		out.Observations = append(out.Observations, domain.RuntimeQuotaView{RuntimeQuotaObservation: obs, ReceivedAt: row.ReceivedAt, Stale: obs.Stale(now)})
	}
	return out, nil
}
