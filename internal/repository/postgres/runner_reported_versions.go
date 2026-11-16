package postgres

import (
	"context"
	"fmt"
)

// reportedRunnerVersionsSQL lists the distinct non-empty versions in this
// instance's fresh controller reports. A report is fresh for the controller's
// heartbeat_max_age_seconds in the latest catalog revision of its integration
// (120 s when missing), the same window effectiveControllerSQL gives a derived
// lease. agent_controllers.version is not read: a lease is renewed without its
// announced version, so it keeps a long-gone release alive.
const reportedRunnerVersionsSQL = `SELECT DISTINCT rep.report->>'version'
	FROM runtime_controller_reports rep
	LEFT JOIN LATERAL (SELECT config->'controllers'->rep.controller_ref AS ctrl FROM runtime_catalog_revisions
		WHERE integration_id = rep.integration_id ORDER BY revision DESC LIMIT 1) cat ON true
	WHERE NULLIF(rep.report->>'version', '') IS NOT NULL
		AND rep.received_at + make_interval(secs => COALESCE(NULLIF((cat.ctrl->>'heartbeat_max_age_seconds')::int, 0), 120)) > now()`

// ReportedRunnerVersions is the Agent Runner versions the controllers of this
// instance report running now, in no particular order.
func (r *InstalledExtensionRepo) ReportedRunnerVersions(ctx context.Context) ([]string, error) {
	var out []string
	if err := r.db.SelectContext(ctx, &out, reportedRunnerVersionsSQL); err != nil {
		return nil, fmt.Errorf("reported runner versions: %w", err)
	}
	return out, nil
}
