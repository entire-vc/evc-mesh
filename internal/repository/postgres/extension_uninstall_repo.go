package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// ExtensionUninstallRepo removes an installed catalog or repository extension
// from the instance. The extension's own data is kept: its ext_<id>_* tables,
// the workspace connections with their settings, the instance settings, the
// allow-list entry and the event subscriptions stay, so a reinstall of the
// same version sees them again. Purging the data is a separate action (the
// retention job, off by default).
type ExtensionUninstallRepo struct {
	db *sqlx.DB
}

// NewExtensionUninstallRepo creates a new ExtensionUninstallRepo.
func NewExtensionUninstallRepo(db *sqlx.DB) *ExtensionUninstallRepo {
	return &ExtensionUninstallRepo{db: db}
}

// Uninstall removes the installed row of extensionID in one transaction:
// every workspace connection of it is switched off (no event is delivered to
// an inactive connection and its token stops authenticating), every actor
// token is revoked, the stored access token of a private repository is
// deleted and journal (when not nil) is written. apierror.NotFound when the
// extension is not installed; apierror.Conflict for a built-in or system
// extension (they are switched off, never removed) and for a theme (removed
// with DELETE /instance/themes/{id}, which also resets its selections).
func (r *ExtensionUninstallRepo) Uninstall(ctx context.Context, extensionID string, journal *domain.InstanceExtensionJournalEntry) (*domain.ExtensionUninstallReport, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var row domain.InstalledExtension
	switch err = tx.GetContext(ctx, &row, `SELECT `+installedExtensionCols+` FROM installed_extensions WHERE extension_id = $1 FOR UPDATE`, extensionID); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, apierror.NotFound("extension")
	case err != nil:
		return nil, err
	}
	if row.Source == domain.ExtensionSourceBuiltin || extensions.IsSystemPart(row.ExtensionID) {
		return nil, apierror.Conflict("a built-in extension cannot be uninstalled; turn it off instead")
	}
	if m, perr := extensions.ParseManifest(row.Manifest); perr == nil && m.Kind == "theme" {
		return nil, apierror.Conflict("a theme is removed on the themes page (DELETE /instance/themes/{id})")
	}

	rep := &domain.ExtensionUninstallReport{ExtensionID: row.ExtensionID, Version: row.Version, Source: string(row.Source)}
	res, err := tx.ExecContext(ctx, `UPDATE integration_configs SET is_active = false, updated_at = now() WHERE provider = $1 AND is_active`, extensionID)
	if err != nil {
		return nil, err
	}
	if rep.Connections, err = res.RowsAffected(); err != nil {
		return nil, err
	}
	res, err = tx.ExecContext(ctx, `
		UPDATE extension_actors SET revoked_at = now(), prev_token_hash = NULL, prev_token_expires_at = NULL
		WHERE revoked_at IS NULL
		  AND (extension_id = $1 OR connection_id IN (SELECT id FROM integration_configs WHERE provider = $1))`, extensionID)
	if err != nil {
		return nil, err
	}
	if rep.TokensRevoked, err = res.RowsAffected(); err != nil {
		return nil, err
	}
	// The access token of a private repository has no foreign key to the row:
	// left behind it keeps a secret around and blocks a reinstall of the id.
	if _, err = tx.ExecContext(ctx, `DELETE FROM extension_repo_credentials WHERE extension_id = $1`, extensionID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM installed_extensions WHERE extension_id = $1`, extensionID); err != nil {
		return nil, err
	}
	if journal != nil {
		journal.ExtensionID = extensionID
		if journal.Details, err = json.Marshal(map[string]any{
			"version": row.Version, "source": row.Source, "data_kept": true,
			"connections_disabled": rep.Connections, "tokens_revoked": rep.TokensRevoked,
		}); err != nil {
			return nil, err
		}
		if err := appendJournal(ctx, tx, journal); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return rep, nil
}
