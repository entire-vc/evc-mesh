package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// lockGuardedRow reads the installed row FOR UPDATE and checks it is still what
// g names. apierror.NotFound when there is no row, apierror.Conflict when it
// changed since the caller's plan.
func lockGuardedRow(ctx context.Context, tx *sqlx.Tx, extensionID string, g domain.ExtensionStateGuard) (*domain.InstalledExtension, error) {
	var cur domain.InstalledExtension
	err := tx.GetContext(ctx, &cur, `SELECT `+installedExtensionCols+` FROM installed_extensions WHERE extension_id = $1 FOR UPDATE`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apierror.NotFound("extension")
	}
	if err != nil {
		return nil, err
	}
	if cur.Source != g.Source || cur.Version != g.Version || cur.CommitSHA != g.CommitSHA {
		return nil, apierror.Conflict(fmt.Sprintf("the extension is now %s %s@%s, not the %s %s@%s the plan was made for; nothing was changed",
			cur.Source, cur.Version, short(cur.CommitSHA), g.Source, g.Version, short(g.CommitSHA)))
	}
	return &cur, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// withDetail returns e with key=value added to its JSON details.
func withDetail(e *domain.InstanceExtensionJournalEntry, key string, value any) (*domain.InstanceExtensionJournalEntry, error) {
	if e == nil {
		return nil, nil
	}
	m := map[string]any{}
	if len(e.Details) > 0 {
		if err := json.Unmarshal(e.Details, &m); err != nil {
			return nil, err
		}
	}
	m[key] = value
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	out := *e
	out.Details = raw
	return &out, nil
}

// MoveRepoToCatalog turns the source=repo row named by g into a source=catalog
// row of release ext, in one transaction: the row is locked and re-checked,
// source / trust / digest / commit / manifest are replaced, the repository
// URL, ref and every previous / pending / skipped column are cleared, the
// stored repository token is deleted and journal is written. The block state
// of the repository release is cleared: the caller checked the target release
// is not blocked, and the next blocklist pass judges the new row. Everything that
// hangs off the extension id (connections, consent, scope, settings, actors,
// subscriptions, data tables) is not touched. The repository state is not
// kept: there is no way back to it.
func (r *InstalledExtensionRepo) MoveRepoToCatalog(ctx context.Context, ext *domain.InstalledExtension, g domain.ExtensionStateGuard, journal *domain.InstanceExtensionJournalEntry) error {
	if g.Source != domain.ExtensionSourceRepo {
		return apierror.Conflict("only an install from a repository moves to the catalog")
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = lockGuardedRow(ctx, tx, ext.ExtensionID, g); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE installed_extensions SET
			source = 'catalog', version = $2, manifest = $3, commit_sha = $4, artifact_sha256 = $5, trust_level = $6,
			catalog_scope = $7, repo_url = '', repo_ref = '',
			previous_version = '', previous_snapshot = NULL,
			previous_manifest = NULL, previous_commit_sha = '', previous_ref = '',
			pending_version = '', pending_ref = '', pending_commit_sha = '', pending_manifest = NULL,
			skipped_commit_sha = '', blocked = false, blocked_reason = '', updated_at = now()
		WHERE extension_id = $1 AND source = 'repo'`,
		ext.ExtensionID, ext.Version, []byte(ext.Manifest), ext.CommitSHA, ext.ArtifactSHA256, string(ext.TrustLevel), catalogScope(ext.CatalogScope)); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM extension_repo_credentials WHERE extension_id = $1`, ext.ExtensionID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if journal, err = withDetail(journal, "repository_token_deleted", n > 0); err != nil {
		return err
	}
	if err := appendJournal(ctx, tx, journal); err != nil {
		return err
	}
	return tx.Commit()
}

// Repin points the source=repo row named by g at another URL, ref or commit
// (next carries RepoURL, RepoRef, CommitSHA, Version, Manifest) and applies
// the token change, in one transaction with journal. On the same URL the
// release it leaves goes to previous_* so a repository rollback can return to
// it; on another URL previous_* is cleared (the old ref means nothing there).
// A pending update and a skipped commit are cleared: the admin chose the pin.
func (r *RepoExtensionRepo) Repin(ctx context.Context, next *domain.InstalledExtension, g domain.ExtensionStateGuard, tok domain.RepoTokenChange, token string, journal *domain.InstanceExtensionJournalEntry) error {
	if g.Source != domain.ExtensionSourceRepo {
		return apierror.Conflict("only an install from a repository can be re-pinned")
	}
	var enc string
	switch tok {
	case domain.RepoTokenKeep, domain.RepoTokenRemove:
	case domain.RepoTokenReplace:
		if token == "" {
			return apierror.BadRequest("a replacement token is empty")
		}
		var err error
		if enc, err = encryption.Encrypt(token); err != nil {
			return fmt.Errorf("encrypt repo token: %w", err)
		}
		if !encryption.IsEncrypted(enc) {
			return errors.New("repo token not stored: instance encryption key is not configured")
		}
	default:
		return fmt.Errorf("unknown token change %q", tok)
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := lockGuardedRow(ctx, tx, next.ExtensionID, g)
	if err != nil {
		return err
	}
	keepPrevious := cur.RepoURL == next.RepoURL
	if _, err := tx.ExecContext(ctx, `
		UPDATE installed_extensions SET
			previous_version    = CASE WHEN $7 THEN version    ELSE '' END,
			previous_manifest   = CASE WHEN $7 THEN manifest   ELSE NULL END,
			previous_commit_sha = CASE WHEN $7 THEN commit_sha ELSE '' END,
			previous_ref        = CASE WHEN $7 THEN repo_ref   ELSE '' END,
			repo_url = $2, repo_ref = $3, commit_sha = $4, version = $5, manifest = $6,
			pending_version = '', pending_ref = '', pending_commit_sha = '', pending_manifest = NULL, skipped_commit_sha = '',
			updated_at = now()
		WHERE extension_id = $1 AND source = 'repo'`,
		next.ExtensionID, next.RepoURL, next.RepoRef, next.CommitSHA, next.Version, []byte(next.Manifest), keepPrevious); err != nil {
		return err
	}
	switch tok {
	case domain.RepoTokenReplace:
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO extension_repo_credentials (extension_id, token_enc, updated_by) VALUES ($1, $2, $3)
			ON CONFLICT (extension_id) DO UPDATE SET token_enc = EXCLUDED.token_enc, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			next.ExtensionID, enc, next.InstalledBy); err != nil {
			return err
		}
	case domain.RepoTokenRemove:
		if _, err := tx.ExecContext(ctx, `DELETE FROM extension_repo_credentials WHERE extension_id = $1`, next.ExtensionID); err != nil {
			return err
		}
	}
	if err := appendJournal(ctx, tx, journal); err != nil {
		return err
	}
	return tx.Commit()
}
