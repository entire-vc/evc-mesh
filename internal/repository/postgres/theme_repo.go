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

func (r *ThemeRepo) SetInstance(ctx context.Context, themeID *string, settings json.RawMessage, by uuid.UUID) (*domain.ThemeSelection, error) {
	var t domain.ThemeSelection
	err := r.db.GetContext(ctx, &t, `
		INSERT INTO instance_theme (id, theme_id, settings, updated_by, updated_at)
		VALUES (true, $1, $2, $3, now())
		ON CONFLICT (id) DO UPDATE SET theme_id = EXCLUDED.theme_id, settings = EXCLUDED.settings,
			version = instance_theme.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+themeCols, themeID, settingsOrEmpty(settings), by)
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

func (r *ThemeRepo) SetWorkspace(ctx context.Context, workspaceID uuid.UUID, themeID *string, settings json.RawMessage, by uuid.UUID) (*domain.ThemeSelection, error) {
	var t domain.ThemeSelection
	err := r.db.GetContext(ctx, &t, `
		INSERT INTO workspace_theme (workspace_id, theme_id, settings, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (workspace_id) DO UPDATE SET theme_id = EXCLUDED.theme_id, settings = EXCLUDED.settings,
			version = workspace_theme.version + 1, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING `+themeCols, workspaceID, themeID, settingsOrEmpty(settings), by)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
