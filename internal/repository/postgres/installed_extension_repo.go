package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
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
	_, err := r.db.ExecContext(ctx, q, ext.ExtensionID, ext.Version, []byte(ext.Manifest), string(ext.TrustLevel))
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
