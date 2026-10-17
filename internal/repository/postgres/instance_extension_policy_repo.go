package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// InstanceExtensionPolicyRepo implements repository.InstanceExtensionPolicyRepository.
type InstanceExtensionPolicyRepo struct {
	db *sqlx.DB
}

func NewInstanceExtensionPolicyRepo(db *sqlx.DB) *InstanceExtensionPolicyRepo {
	return &InstanceExtensionPolicyRepo{db: db}
}

func (r *InstanceExtensionPolicyRepo) Allowed(ctx context.Context, extensionID string) (bool, error) {
	var allowed bool
	err := r.db.GetContext(ctx, &allowed, `SELECT allowed FROM instance_extension_policy WHERE extension_id = $1`, extensionID)
	if err == nil {
		return allowed, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var trust string
	err = r.db.GetContext(ctx, &trust, `SELECT trust_level FROM installed_extensions WHERE extension_id = $1`, extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		// Every registered provider is synced into installed_extensions at start,
		// so no row means an extension the instance does not know: fail closed.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return domain.DefaultExtensionAllowed(trust), nil
}

func (r *InstanceExtensionPolicyRepo) ListPolicies(ctx context.Context) (map[string]domain.InstanceExtensionPolicy, error) {
	var rows []domain.InstanceExtensionPolicy
	if err := r.db.SelectContext(ctx, &rows, `SELECT extension_id, allowed, updated_by, updated_at FROM instance_extension_policy`); err != nil {
		return nil, err
	}
	out := make(map[string]domain.InstanceExtensionPolicy, len(rows))
	for _, p := range rows {
		out[p.ExtensionID] = p
	}
	return out, nil
}

func (r *InstanceExtensionPolicyRepo) SetAllowed(ctx context.Context, extensionID string, allowed bool, by *uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO instance_extension_policy (extension_id, allowed, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (extension_id) DO UPDATE SET allowed = EXCLUDED.allowed, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		extensionID, allowed, by)
	return err
}

func (r *InstanceExtensionPolicyRepo) GetConfig(ctx context.Context) (*domain.InstanceExtensionConfig, error) {
	var c domain.InstanceExtensionConfig
	err := r.db.GetContext(ctx, &c, `SELECT auto_update, updated_by, updated_at FROM instance_extension_config WHERE id`)
	if errors.Is(err, sql.ErrNoRows) {
		return &domain.InstanceExtensionConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *InstanceExtensionPolicyRepo) SetAutoUpdate(ctx context.Context, on bool, by *uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO instance_extension_config (id, auto_update, updated_by, updated_at) VALUES (true, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET auto_update = EXCLUDED.auto_update, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		on, by)
	return err
}

func (r *InstanceExtensionPolicyRepo) AppendJournal(ctx context.Context, e *domain.InstanceExtensionJournalEntry) error {
	details := e.Details
	if len(details) == 0 {
		details = []byte(`{}`)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO instance_extension_journal (actor_id, extension_id, action, details) VALUES ($1, $2, $3, $4)`,
		e.ActorID, e.ExtensionID, e.Action, []byte(details))
	return err
}

func (r *InstanceExtensionPolicyRepo) ListJournal(ctx context.Context, limit int) ([]domain.InstanceExtensionJournalEntry, error) {
	var rows []domain.InstanceExtensionJournalEntry
	err := r.db.SelectContext(ctx, &rows, `
		SELECT j.id, j.actor_id, COALESCE(NULLIF(u.display_name, ''), u.email, '') AS actor_name,
		       j.extension_id, j.action, j.details, j.created_at
		FROM instance_extension_journal j LEFT JOIN users u ON u.id = j.actor_id
		ORDER BY j.created_at DESC, j.id DESC LIMIT $1`, limit)
	return rows, err
}
