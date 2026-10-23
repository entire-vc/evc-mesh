package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jmoiron/sqlx"
)

// QAExtensionIDPattern is the ONLY extension id the cleanup may touch: the
// throwaway extension registered by the acceptance run. It is
// anchored on both ends, so "hello", "qa-e2e-hello-", "qa-e2e-hello-1x"
// and "xqa-e2e-hello-1" are all refused.
var QAExtensionIDPattern = regexp.MustCompile(`^qa-e2e-hello-\d{1,6}$`)

// ErrExtensionCleanupRefused is returned for any id outside QAExtensionIDPattern.
var ErrExtensionCleanupRefused = errors.New("extension id does not match the QA cleanup pattern")

// extensionCleanupTarget is one table the registration/activation of an
// extension writes to, and the column that holds the extension id. Order is
// child-before-parent so the tool never leans on ON DELETE CASCADE alone.
type extensionCleanupTarget struct {
	Table string
	// Where selects rows for the id passed as $1.
	Where string
}

var extensionCleanupTargets = []extensionCleanupTarget{
	// Workspace activation: connection -> actor, event subscriptions
	// (webhook_deliveries hang off the subscriptions).
	{"webhook_deliveries", `subscription_id IN (SELECT s.id FROM extension_event_subscriptions s JOIN integration_configs c ON c.id = s.connection_id WHERE c.provider = $1)`},
	{"extension_event_subscriptions", `connection_id IN (SELECT id FROM integration_configs WHERE provider = $1)`},
	{"extension_actors", `extension_id = $1`},
	{"project_integrations", `extension_id = $1`},
	{"integration_configs", `provider = $1`},
	// Instance level: registry, settings, policy, repo credential, journal.
	{"extension_repo_credentials", `extension_id = $1`},
	{"instance_extension_settings", `extension_id = $1`},
	{"instance_extension_policy", `extension_id = $1`},
	{"instance_extension_journal", `extension_id = $1`},
	{"installed_extensions", `extension_id = $1`},
}

// ExtensionCleanupReport is the per-table row count that was (dry run) or will
// be (apply) removed.
type ExtensionCleanupReport struct {
	ExtensionID string
	Applied     bool
	Counts      []ExtensionCleanupCount
}

type ExtensionCleanupCount struct {
	Table string
	Rows  int64
}

// Total is the number of rows across all tables.
func (r *ExtensionCleanupReport) Total() int64 {
	var n int64
	for _, c := range r.Counts {
		n += c.Rows
	}
	return n
}

// CleanupQAExtension removes every instance- and workspace-level row of a QA
// extension id in one transaction. With apply=false nothing is deleted.
// Any id outside QAExtensionIDPattern is refused before the database is touched.
func CleanupQAExtension(ctx context.Context, db *sqlx.DB, extensionID string, apply bool) (*ExtensionCleanupReport, error) {
	if !QAExtensionIDPattern.MatchString(extensionID) {
		return nil, fmt.Errorf("%w: %q", ErrExtensionCleanupRefused, extensionID)
	}
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// integration_configs and extension_actors carry row-level security keyed
	// on a session setting this tool never sets. Only the table owner itself
	// (or a superuser / BYPASSRLS role) skips it; a role that merely inherits
	// the owner role does not, and would silently see (and delete) nothing.
	// Refuse instead of reporting a success that left rows behind.
	var bypass bool
	if err := tx.GetContext(ctx, &bypass, `
		SELECT (r.rolsuper OR r.rolbypassrls) OR (
		         SELECT count(*) = 2 AND bool_and(t.tableowner = current_user)
		         FROM pg_tables t
		         WHERE t.schemaname = current_schema()
		           AND t.tablename IN ('integration_configs', 'extension_actors'))
		FROM pg_roles r WHERE r.rolname = current_user`); err != nil {
		return nil, err
	}
	if !bypass {
		return nil, errors.New("run as the owner of integration_configs and extension_actors (or a superuser): row-level security would hide rows from this role")
	}

	rep := &ExtensionCleanupReport{ExtensionID: extensionID, Applied: apply}
	for _, t := range extensionCleanupTargets {
		var n int64
		if apply {
			res, err := tx.ExecContext(ctx, `DELETE FROM `+t.Table+` WHERE `+t.Where, extensionID)
			if err != nil {
				return nil, fmt.Errorf("delete from %s: %w", t.Table, err)
			}
			if n, err = res.RowsAffected(); err != nil {
				return nil, err
			}
		} else if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM `+t.Table+` WHERE `+t.Where, extensionID); err != nil {
			return nil, fmt.Errorf("count %s: %w", t.Table, err)
		}
		rep.Counts = append(rep.Counts, ExtensionCleanupCount{Table: t.Table, Rows: n})
	}
	if !apply {
		return rep, nil
	}
	return rep, tx.Commit()
}
