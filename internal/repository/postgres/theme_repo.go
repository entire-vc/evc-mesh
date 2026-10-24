package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// ThemeRepo implements repository.ThemeRepository.
type ThemeRepo struct {
	db *sqlx.DB
}

func NewThemeRepo(db *sqlx.DB) *ThemeRepo { return &ThemeRepo{db: db} }

const themeCols = `theme_id, settings, version, updated_by, updated_at`

func settingsOrEmpty(s json.RawMessage) []byte {
	if len(s) == 0 {
		return []byte(`{}`)
	}
	return s
}

func (r *ThemeRepo) GetInstance(ctx context.Context) (*domain.ThemeSelection, error) {
	var t domain.ThemeSelection
	err := r.db.GetContext(ctx, &t, `SELECT `+themeCols+` FROM instance_theme WHERE id`)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *ThemeRepo) SetInstance(ctx context.Context, themeID *string, settings json.RawMessage, by uuid.UUID, journal *domain.InstanceExtensionJournalEntry) (*domain.ThemeSelection, error) {
	var t domain.ThemeSelection
	err := r.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &t, `
		INSERT INTO instance_theme (id, theme_id, settings, updated_by, updated_at)
		VALUES (true, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET theme_id = EXCLUDED.theme_id, settings = EXCLUDED.settings,
			version = instance_theme.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+themeCols, themeID, settingsOrEmpty(settings), by); err != nil {
			return err
		}
		return appendJournal(ctx, tx, withVersion(journal, t.Version))
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *ThemeRepo) GetWorkspace(ctx context.Context, workspaceID uuid.UUID) (*domain.ThemeSelection, error) {
	var t domain.ThemeSelection
	err := r.db.GetContext(ctx, &t, `SELECT `+themeCols+` FROM workspace_theme WHERE workspace_id = $1`, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *ThemeRepo) SetWorkspace(ctx context.Context, workspaceID uuid.UUID, themeID *string, settings json.RawMessage, by uuid.UUID, journal *domain.InstanceExtensionJournalEntry) (*domain.ThemeSelection, error) {
	var t domain.ThemeSelection
	err := r.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &t, `
		INSERT INTO workspace_theme (workspace_id, theme_id, settings, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (workspace_id) DO UPDATE SET theme_id = EXCLUDED.theme_id, settings = EXCLUDED.settings,
			version = workspace_theme.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+themeCols, workspaceID, themeID, settingsOrEmpty(settings), by); err != nil {
			return err
		}
		return appendJournal(ctx, tx, withVersion(journal, t.Version))
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// inTx runs fn in a transaction: a failure of fn, the journal write included,
// leaves nothing behind.
func (r *ThemeRepo) inTx(ctx context.Context, fn func(tx *sqlx.Tx) error) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// withVersion is the journal entry with the stored version added to its
// details; the version is only known once the row is written.
func withVersion(e *domain.InstanceExtensionJournalEntry, version int) *domain.InstanceExtensionJournalEntry {
	if e == nil {
		return nil
	}
	d := map[string]any{}
	_ = json.Unmarshal(e.Details, &d)
	d["version"] = version
	c := *e
	c.Details, _ = json.Marshal(d)
	return &c
}

// InstallBuiltin upserts the builtin row and its journal entry in one transaction.
func (r *ThemeRepo) InstallBuiltin(ctx context.Context, ext *domain.InstalledExtension, journal *domain.InstanceExtensionJournalEntry) error {
	return r.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := upsertBuiltin(ctx, tx, ext); err != nil {
			return err
		}
		return appendJournal(ctx, tx, journal)
	})
}

func (r *ThemeRepo) DeleteTheme(ctx context.Context, extensionID string, journal *domain.InstanceExtensionJournalEntry) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// The service checked that the id is a theme before the transaction; check it
	// again under a row lock, so a different extension installed under the same id
	// in between is neither deleted nor stripped of its token.
	var one int
	switch err = tx.GetContext(ctx, &one, `SELECT 1 FROM installed_extensions WHERE extension_id = $1 AND manifest->>'kind' = 'theme' FOR UPDATE`, extensionID); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	// The foreign key would null theme_id on its own; the settings belong to the
	// theme's schema and must go with it.
	for _, q := range []string{
		`UPDATE instance_theme SET theme_id = NULL, settings = '{}'::jsonb, version = version + 1, updated_at = now() WHERE theme_id = $1`,
		`UPDATE workspace_theme SET theme_id = NULL, settings = '{}'::jsonb, version = version + 1, updated_at = now() WHERE theme_id = $1`,
	} {
		if _, err = tx.ExecContext(ctx, q, extensionID); err != nil {
			return false, err
		}
	}
	// The access token of a private repository has no foreign key to the row: left
	// behind, it keeps a secret around and blocks a reinstall of the same id.
	if _, err = tx.ExecContext(ctx, `DELETE FROM extension_repo_credentials WHERE extension_id = $1`, extensionID); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM installed_extensions WHERE extension_id = $1`, extensionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	// A selection that committed between the resets above and the delete had its
	// theme_id nulled by the foreign key but kept its settings. A selection with no
	// theme never carries settings (Select refuses them), so clear those too.
	for _, t := range []string{"instance_theme", "workspace_theme"} {
		if _, err = tx.ExecContext(ctx, `UPDATE `+t+` SET settings = '{}'::jsonb, version = version + 1, updated_at = now() WHERE theme_id IS NULL AND settings <> '{}'::jsonb`); err != nil {
			return false, err
		}
	}
	// Nothing was deleted: there is nothing to record, and a retry must see the same 404.
	if n > 0 {
		if err := appendJournal(ctx, tx, journal); err != nil {
			return false, err
		}
	}
	if cerr := tx.Commit(); cerr != nil {
		return false, cerr
	}
	return n > 0, nil
}
