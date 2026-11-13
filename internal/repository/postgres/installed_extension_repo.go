package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

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
	artifact_sha256, trust_level, catalog_scope, installed_by, previous_version, blocked, blocked_reason, created_at, updated_at`

// catalogScope is the stored scope of a catalog row: "scoped" or "default".
func catalogScope(s string) string {
	if s == "scoped" {
		return s
	}
	return "default"
}

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
		INSERT INTO installed_extensions (extension_id, version, manifest, source, commit_sha, artifact_sha256, trust_level, installed_by, catalog_scope)
		VALUES ($1, $2, $3, 'catalog', $4, $5, $6, $7, $8)`,
		ext.ExtensionID, ext.Version, []byte(ext.Manifest), ext.CommitSHA, ext.ArtifactSHA256, string(ext.TrustLevel), ext.InstalledBy, catalogScope(ext.CatalogScope))
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apierror.Conflict("an extension with this id is already installed")
	}
	return err
}

// UpdateCatalog moves a source=catalog row to a new release while it is still
// what g names. In the same
// transaction the release it leaves is sealed into previous_snapshot, so a
// rollback can restore all of it. apierror.NotFound when no such row.
func (r *InstalledExtensionRepo) UpdateCatalog(ctx context.Context, ext *domain.InstalledExtension, g domain.CatalogUpdateGuard) error {
	// The unattended pass never moves the Runner: its installed version is the
	// controller version the runtime is pinned to. Refused before any read.
	if g.Auto && ext.ExtensionID == domain.RunnerExtensionID {
		return apierror.Conflict("the runner is updated only explicitly, not by the automatic pass")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var cur domain.InstalledExtension
	err = tx.GetContext(ctx, &cur, `SELECT `+installedExtensionCols+` FROM installed_extensions WHERE extension_id = $1 AND source = 'catalog' FOR UPDATE`, ext.ExtensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return apierror.NotFound("catalog extension")
	}
	if err != nil {
		return err
	}
	// The updater read the row some time ago (and fetched an artifact since):
	// a rollback or another update in between makes this one stale.
	if cur.Version != g.FromVersion || cur.CommitSHA != g.FromCommitSHA {
		return apierror.Conflict("the extension changed while the update was prepared")
	}
	if g.Auto {
		var skipped string
		err = tx.GetContext(ctx, &skipped, `SELECT skipped_commit_sha FROM installed_extensions WHERE extension_id = $1`, ext.ExtensionID)
		if err != nil {
			return err
		}
		if skipped != "" && skipped == ext.CommitSHA {
			return apierror.Conflict("that release was rolled back; it is not applied automatically")
		}
	}
	snap := domain.CatalogSnapshot{
		Version: cur.Version, Manifest: cur.Manifest, CommitSHA: cur.CommitSHA, ArtifactSHA256: cur.ArtifactSHA256,
		TrustLevel: string(cur.TrustLevel), Source: string(cur.Source), CatalogScope: catalogScope(cur.CatalogScope),
		SupersededVersion: ext.Version, SupersededCommit: ext.CommitSHA, CapturedAt: time.Now().UTC(),
	}
	snap.Seal()
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	// skipped_commit_sha is cleared: an explicit update is the admin's choice
	// to take that release again.
	if _, err := tx.ExecContext(ctx, `
		UPDATE installed_extensions SET
			previous_version = version, previous_snapshot = $7, skipped_commit_sha = '',
			version = $2, manifest = $3, commit_sha = $4,
			artifact_sha256 = $5, trust_level = $6, catalog_scope = $8, updated_at = now()
		WHERE extension_id = $1 AND source = 'catalog'`,
		ext.ExtensionID, ext.Version, []byte(ext.Manifest), ext.CommitSHA, ext.ArtifactSHA256, string(ext.TrustLevel), raw, catalogScope(ext.CatalogScope)); err != nil {
		return err
	}
	return tx.Commit()
}

// GetCatalogSnapshot returns the sealed snapshot of a source=catalog row, nil
// when there is none (never updated, rolled back already, or a legacy row that
// only has previous_version). An unreadable snapshot is returned as an error.
func (r *InstalledExtensionRepo) GetCatalogSnapshot(ctx context.Context, extensionID string) (*domain.CatalogSnapshot, error) {
	var raw []byte
	err := r.db.GetContext(ctx, &raw, `SELECT previous_snapshot FROM installed_extensions WHERE extension_id = $1 AND source = 'catalog'`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("catalog extension")
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var s domain.CatalogSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SkippedCommit is the commit an admin rolled back from; "" when none.
func (r *InstalledExtensionRepo) SkippedCommit(ctx context.Context, extensionID string) (string, error) {
	var sha string
	err := r.db.GetContext(ctx, &sha, `SELECT skipped_commit_sha FROM installed_extensions WHERE extension_id = $1`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sha, err
}

// RollbackCatalog restores the snapshot over the row and writes journal, all
// in one transaction. The UPDATE only matches while the row is still the
// release the guard names, holds exactly that snapshot, and is not blocked, so
// a concurrent update, rollback or block makes it a 409 and changes nothing.
// The commit rolled back from is remembered in skipped_commit_sha.
func (r *InstalledExtensionRepo) RollbackCatalog(ctx context.Context, extensionID string, g domain.CatalogRollbackGuard, snap domain.CatalogSnapshot, journal *domain.InstanceExtensionJournalEntry) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE installed_extensions SET
			skipped_commit_sha = commit_sha,
			version = $5, manifest = $6, commit_sha = $7, artifact_sha256 = $8, trust_level = $9, catalog_scope = $10,
			previous_version = '', previous_snapshot = NULL,
			updated_at = now()
		WHERE extension_id = $1 AND source = 'catalog' AND NOT blocked
		  AND version = $2 AND commit_sha = $3 AND previous_snapshot->>'digest' = $4`,
		extensionID, g.Version, g.CommitSHA, g.SnapshotDigest,
		snap.Version, []byte(snap.Manifest), snap.CommitSHA, snap.ArtifactSHA256, snap.TrustLevel, catalogScope(snap.CatalogScope))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return apierror.Conflict("the extension changed or has nothing to roll back to")
	}
	if err := appendJournal(ctx, tx, journal); err != nil {
		return err
	}
	return tx.Commit()
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
