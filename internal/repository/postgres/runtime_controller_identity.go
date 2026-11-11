package postgres

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// IsController reports whether actor is a controller of workspace ws: an
// agent key presented for exactly this workspace (not an OAuth connector)
// whose identity holds the active reporter grant of a controller in the
// workspace's runtime catalog — the identity a controller reports with
// (Report, Desired). No runtime connection or catalog means no controller.
func (r *RuntimeRepo) IsController(ctx context.Context, ws uuid.UUID, actor RuntimeActor) (bool, error) {
	if actor.Connector || actor.AgentID == uuid.Nil || actor.AuthWorkspaceID != ws {
		return false, nil
	}
	connection, err := runtimeConnection(ctx, r.db, ws, false)
	if err != nil || connection == nil {
		return false, err
	}
	catalog, err := domain.ParseRuntimeCatalog(connection.Config)
	if err != nil {
		// An unreadable catalog names no controller; it is not this caller's fault.
		return false, nil
	}
	for _, c := range catalog.Controllers {
		if c.ReporterGrantID == uuid.Nil {
			continue
		}
		_, err = runtimeGrant(ctx, r.db, domain.RuntimeIdentity{AgentID: actor.AgentID, WorkspaceID: ws, GrantID: c.ReporterGrantID})
		var ae *apierror.Error
		switch {
		case err == nil:
			return true, nil
		case errors.As(err, &ae) && ae.Code == http.StatusForbidden:
			continue
		default:
			return false, err
		}
	}
	return false, nil
}
