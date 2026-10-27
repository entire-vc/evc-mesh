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
	return appendJournal(ctx, r.db, e)
}

// appendJournal writes one journal row through db or an open transaction; a nil
// entry is a no-op, so callers pass their optional entry straight through.
func appendJournal(ctx context.Context, ex sqlx.ExecerContext, e *domain.InstanceExtensionJournalEntry) error {
	if e == nil {
		return nil
	}
	details := e.Details
	if len(details) == 0 {
		details = []byte(`{}`)
	}
	_, err := ex.ExecContext(ctx, `
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

// SearchJournal is ListJournal with filters, newest first.
func (r *InstanceExtensionPolicyRepo) SearchJournal(ctx context.Context, f domain.InstanceJournalFilter) ([]domain.InstanceExtensionJournalEntry, error) {
	var rows []domain.InstanceExtensionJournalEntry
	err := r.db.SelectContext(ctx, &rows, `
		SELECT j.id, j.actor_id, COALESCE(NULLIF(u.display_name, ''), u.email, '') AS actor_name,
		       j.extension_id, j.action, j.details, j.created_at
		FROM instance_extension_journal j LEFT JOIN users u ON u.id = j.actor_id
		WHERE ($2 = '' OR j.extension_id = $2)
		  AND ($3 = '' OR j.action = $3)
		  AND ($4::uuid IS NULL OR j.actor_id = $4::uuid)
		  AND ($5::timestamptz IS NULL OR j.created_at >= $5::timestamptz)
		  AND ($6::timestamptz IS NULL OR j.created_at < $6::timestamptz)
		ORDER BY j.created_at DESC, j.id DESC LIMIT $1`,
		f.Limit, f.ExtensionID, f.Action, f.ActorID, f.Since, f.Until)
	return rows, err
}

// JournalFacets returns the distinct actions, actors and extensions of the whole journal.
func (r *InstanceExtensionPolicyRepo) JournalFacets(ctx context.Context) (*domain.InstanceJournalFacets, error) {
	out := &domain.InstanceJournalFacets{Actions: []string{}, Actors: []domain.InstanceJournalActor{}, Extensions: []string{}}
	if err := r.db.SelectContext(ctx, &out.Actions, `SELECT DISTINCT action FROM instance_extension_journal ORDER BY action`); err != nil {
		return nil, err
	}
	if err := r.db.SelectContext(ctx, &out.Extensions, `
		SELECT DISTINCT extension_id FROM instance_extension_journal WHERE extension_id <> '' ORDER BY extension_id`); err != nil {
		return nil, err
	}
	err := r.db.SelectContext(ctx, &out.Actors, `
		SELECT u.id, COALESCE(NULLIF(u.display_name, ''), u.email, '') AS name
		FROM users u WHERE u.id IN (SELECT DISTINCT actor_id FROM instance_extension_journal WHERE actor_id IS NOT NULL)
		ORDER BY name`)
	return out, err
}
