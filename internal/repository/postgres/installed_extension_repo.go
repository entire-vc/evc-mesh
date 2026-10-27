package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// InstalledExtensionRepo implements repository.InstalledExtensionRepository.
type InstalledExtensionRepo struct {
	db *sqlx.DB
}

// NewInstalledExtensionRepo creates a new InstalledExtensionRepo.
func NewInstalledExtensionRepo(db *sqlx.DB) *InstalledExtensionRepo {
	return &InstalledExtensionRepo{db: db}
}

const installedExtensionCols = `extension_id, version, manifest, source, repo_url, repo_ref, commit_sha,
	artifact_sha256, trust_level, installed_by, previous_version, blocked, blocked_reason, created_at, updated_at`

func (r *InstalledExtensionRepo) UpsertBuiltin(ctx context.Context, ext *domain.InstalledExtension) error {
	return upsertBuiltin(ctx, r.db, ext)
}

func upsertBuiltin(ctx context.Context, ex sqlx.ExecerContext, ext *domain.InstalledExtension) error {
	const q = `
		INSERT INTO installed_extensions (extension_id, version, manifest, source, trust_level)
		VALUES ($1, $2, $3, 'builtin', $4)
		ON CONFLICT (extension_id) DO UPDATE SET
			previous_version = CASE WHEN installed_extensions.version <> EXCLUDED.version
				THEN installed_extensions.version ELSE installed_extensions.previous_version END,
			version = EXCLUDED.version,
			manifest = EXCLUDED.manifest,
			updated_at = now()
		WHERE installed_extensions.source = 'builtin'`
	_, err := ex.ExecContext(ctx, q, ext.ExtensionID, ext.Version, []byte(ext.Manifest), string(ext.TrustLevel))
	return err
}

func (r *InstalledExtensionRepo) Get(ctx context.Context, extensionID string) (*domain.InstalledExtension, error) {
	var e domain.InstalledExtension
	err := r.db.GetContext(ctx, &e, `SELECT `+installedExtensionCols+` FROM installed_extensions WHERE extension_id = $1`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *InstalledExtensionRepo) List(ctx context.Context) ([]domain.InstalledExtension, error) {
	var out []domain.InstalledExtension
	err := r.db.SelectContext(ctx, &out, `SELECT `+installedExtensionCols+` FROM installed_extensions ORDER BY extension_id`)
	return out, err
}

// SetBlocked turns the catalog block on or off for one extension. It reports
// whether the row changed, so a repeated refresh stays quiet.
func (r *InstalledExtensionRepo) SetBlocked(ctx context.Context, extensionID string, blocked bool, reason string) (bool, error) {
	if !blocked {
		reason = ""
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE installed_extensions SET blocked = $2, blocked_reason = $3, updated_at = now()
		WHERE extension_id = $1 AND source <> 'builtin' AND (blocked <> $2 OR blocked_reason <> $3)`,
		extensionID, blocked, reason)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// InsertCatalog inserts a source=catalog row; apierror.Conflict when the id is taken.
func (r *InstalledExtensionRepo) InsertCatalog(ctx context.Context, ext *domain.InstalledExtension) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO installed_extensions (extension_id, version, manifest, source, commit_sha, artifact_sha256, trust_level, installed_by)
		VALUES ($1, $2, $3, 'catalog', $4, $5, $6, $7)`,
		ext.ExtensionID, ext.Version, []byte(ext.Manifest), ext.CommitSHA, ext.ArtifactSHA256, string(ext.TrustLevel), ext.InstalledBy)
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apierror.Conflict("an extension with this id is already installed")
	}
	return err
}

// UpdateCatalog moves a source=catalog row to a new release; the version it
// leaves is kept in previous_version. apierror.NotFound when no such row.
func (r *InstalledExtensionRepo) UpdateCatalog(ctx context.Context, ext *domain.InstalledExtension) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE installed_extensions SET
			previous_version = version, version = $2, manifest = $3, commit_sha = $4,
			artifact_sha256 = $5, trust_level = $6, updated_at = now()
		WHERE extension_id = $1 AND source = 'catalog'`,
		ext.ExtensionID, ext.Version, []byte(ext.Manifest), ext.CommitSHA, ext.ArtifactSHA256, string(ext.TrustLevel))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("catalog extension")
	}
	return nil
}

// CountActiveConnections returns, per extension id, how many workspaces use it:
// an active connection (integration_configs.provider is the extension id) or,
// for a theme, the theme in effect: the workspace's own choice, or the instance
// default when the workspace has none. Soft-deleted workspaces are not counted.
func (r *InstalledExtensionRepo) CountActiveConnections(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		Provider string `db:"provider"`
		N        int    `db:"n"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		SELECT u.provider, COUNT(DISTINCT u.workspace_id) AS n
		FROM (
			SELECT c.provider, c.workspace_id FROM integration_configs c WHERE c.is_active
			UNION ALL
			SELECT COALESCE(wt.theme_id, it.theme_id), w.id
			FROM workspaces w
			LEFT JOIN workspace_theme wt ON wt.workspace_id = w.id
			-- instance_theme is a singleton (id BOOLEAN PRIMARY KEY CHECK (id)): every
			-- workspace joins its one row, and a missing row leaves the default NULL.
			LEFT JOIN instance_theme it ON true
		) u JOIN workspaces w ON w.id = u.workspace_id AND w.deleted_at IS NULL
		WHERE u.provider IS NOT NULL GROUP BY u.provider`); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[row.Provider] = row.N
	}
	return out, nil
}
