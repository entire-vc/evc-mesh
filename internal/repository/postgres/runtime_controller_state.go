package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// runtimeControllerLiveness classifies a controller's latest report against the
// catalog revision it must be running. Paused wins over stale so the operator
// sees the emergency stop; a missing or unreadable report is unreported, never
// current. The same rule feeds the inventory "current" flag, admission and the
// capacity projection, so they cannot disagree.
func runtimeControllerLiveness(ctx context.Context, q sqlx.QueryerContext, connection *domain.IntegrationConfig, catalog *domain.RuntimeCatalog, ref string, revision int64, digest string) (string, error) {
	var row struct {
		GrantID     uuid.UUID `db:"reporter_grant_id"`
		Fingerprint string    `db:"grant_fingerprint"`
		Data        []byte    `db:"report"`
		At          time.Time `db:"received_at"`
	}
	err := sqlx.GetContext(ctx, q, &row, `SELECT reporter_grant_id,grant_fingerprint,report,received_at FROM runtime_controller_reports WHERE integration_id=$1 AND controller_ref=$2`, connection.ID, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RuntimeControllerUnreported, nil
	}
	if err != nil {
		return "", err
	}
	return runtimeReportLiveness(ctx, q, connection, catalog, ref, revision, digest, row.GrantID, row.Fingerprint, row.Data, row.At)
}

func runtimeReportLiveness(ctx context.Context, q sqlx.QueryerContext, connection *domain.IntegrationConfig, catalog *domain.RuntimeCatalog, ref string, revision int64, digest string, grantID uuid.UUID, fingerprint string, data []byte, at time.Time) (string, error) {
	controller, ok := catalog.Controllers[ref]
	if !ok {
		return domain.RuntimeControllerUnreported, nil
	}
	var report domain.RuntimeReport
	if json.Unmarshal(data, &report) != nil {
		return domain.RuntimeControllerStale, nil
	}
	var agent uuid.UUID
	err := sqlx.GetContext(ctx, q, &agent, `SELECT agent_id FROM agent_workspace_grants WHERE id=$1`, grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RuntimeControllerStale, nil
	}
	if err != nil {
		return "", err
	}
	current, grantErr := runtimeGrant(ctx, q, domain.RuntimeIdentity{AgentID: agent, WorkspaceID: connection.WorkspaceID, GrantID: grantID})
	if grantErr != nil {
		var denied *apierror.Error
		if !errors.As(grantErr, &denied) {
			return "", grantErr
		}
	}
	switch {
	case report.EmergencyPaused:
		return domain.RuntimeControllerPaused, nil
	case grantErr != nil || controller.ReporterGrantID != grantID || current != fingerprint ||
		report.Revision != revision || report.Digest != digest || report.Status != "applied" || !controller.Enabled ||
		time.Since(at) > time.Duration(controller.HeartbeatMaxAgeSeconds)*time.Second:
		return domain.RuntimeControllerStale, nil
	}
	return domain.RuntimeControllerCurrent, nil
}
