package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// RepoExtensionRepo implements repository.RepoExtensionRepository.
type RepoExtensionRepo struct {
	db *sqlx.DB
}

// NewRepoExtensionRepo creates a new RepoExtensionRepo.
func NewRepoExtensionRepo(db *sqlx.DB) *RepoExtensionRepo { return &RepoExtensionRepo{db: db} }

func (r *RepoExtensionRepo) InsertRepo(ctx context.Context, ext *domain.InstalledExtension, token string, journal *domain.InstanceExtensionJournalEntry) error {
	// Encrypt first and fail closed: Encrypt hands plaintext back when no key is
	// configured, and a repository token must never reach the table that way.
	var enc string
	if token != "" {
		var err error
		if enc, err = encryption.Encrypt(token); err != nil {
			return fmt.Errorf("encrypt repo token: %w", err)
		}
		if !encryption.IsEncrypted(enc) {
			return errors.New("repo token not stored: instance encryption key is not configured")
		}
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO installed_extensions (extension_id, version, manifest, source, repo_url, repo_ref, commit_sha, trust_level, installed_by)
		VALUES ($1, $2, $3, 'repo', $4, $5, $6, $7, $8)`,
		ext.ExtensionID, ext.Version, []byte(ext.Manifest), ext.RepoURL, ext.RepoRef, ext.CommitSHA, string(ext.TrustLevel), ext.InstalledBy)
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return apierror.Conflict("an extension with this id is already installed")
	}
	if err != nil {
		return err
	}
	if enc != "" {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO extension_repo_credentials (extension_id, token_enc, updated_by) VALUES ($1, $2, $3)`,
			ext.ExtensionID, enc, ext.InstalledBy); err != nil {
			return err
		}
	}
	if err := appendJournal(ctx, tx, journal); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *RepoExtensionRepo) GetState(ctx context.Context, extensionID string) (*domain.ExtensionRepoState, error) {
	var s domain.ExtensionRepoState
	err := r.db.GetContext(ctx, &s, `
		SELECT previous_manifest, previous_commit_sha, previous_ref, pending_version, pending_ref, pending_commit_sha, pending_manifest, skipped_commit_sha
		FROM installed_extensions WHERE extension_id = $1`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *RepoExtensionRepo) ApplyUpdate(ctx context.Context, extensionID, version, ref, commitSHA string, manifest []byte) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE installed_extensions SET
			previous_version = version, previous_manifest = manifest, previous_commit_sha = commit_sha, previous_ref = repo_ref,
			version = $2, repo_ref = $3, commit_sha = $4, manifest = $5,
			pending_version = '', pending_ref = '', pending_commit_sha = '', pending_manifest = NULL, skipped_commit_sha = '',
			updated_at = now()
		WHERE extension_id = $1 AND source = 'repo'`, extensionID, version, ref, commitSHA, manifest)
	return requireOneRow(res, err)
}

func (r *RepoExtensionRepo) SetPending(ctx context.Context, extensionID, version, ref, commitSHA string, manifest []byte) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE installed_extensions SET pending_version = $2, pending_ref = $3, pending_commit_sha = $4, pending_manifest = $5, updated_at = now()
		WHERE extension_id = $1 AND source = 'repo'`, extensionID, version, ref, commitSHA, manifest)
	return requireOneRow(res, err)
}

func (r *RepoExtensionRepo) Rollback(ctx context.Context, extensionID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE installed_extensions SET
			skipped_commit_sha = commit_sha,
			version = previous_version, manifest = previous_manifest, commit_sha = previous_commit_sha, repo_ref = previous_ref,
			previous_version = '', previous_manifest = NULL, previous_commit_sha = '', previous_ref = '',
			pending_version = '', pending_ref = '', pending_commit_sha = '', pending_manifest = NULL,
			updated_at = now()
		WHERE extension_id = $1 AND source = 'repo' AND previous_version <> '' AND previous_manifest IS NOT NULL`, extensionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.Conflict("nothing to roll back to")
	}
	return nil
}

func (r *RepoExtensionRepo) GetToken(ctx context.Context, extensionID string) (string, error) {
	var enc string
	err := r.db.GetContext(ctx, &enc, `SELECT token_enc FROM extension_repo_credentials WHERE extension_id = $1`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	plain, err := encryption.Decrypt(enc)
	if err != nil {
		return "", fmt.Errorf("decrypt repo token %q: %w", extensionID, err)
	}
	return plain, nil
}

func requireOneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("extension")
	}
	return nil
}
