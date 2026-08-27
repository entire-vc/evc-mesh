package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// teamRelayIntegrationType is the project_integrations.type value for a Team
// Relay integration — the only integration type with a fixed, checkable
// credential shape today.
const teamRelayIntegrationType = "team_relay"

// teamRelayAgentKeyPattern matches a genuine Team Relay agent key:
// "tr_agent_" + 48 lowercase hex chars, 57 chars total. Any AgentKey bound
// for a team_relay row that doesn't match this shape is rejected at write
// time (see Upsert) rather than stored and left to fail as an opaque 401
// against Team Relay later — the failure mode that shipped 11 double-
// encrypted rows silently on 2026-08-20 (task f824032d): a malformed value
// (there, ciphertext-shaped) sailed through Upsert and only surfaced as an
// authentication error against a different system a day later.
var teamRelayAgentKeyPattern = regexp.MustCompile(`^tr_agent_[0-9a-f]{48}$`)

type projectIntegrationRow struct {
	ID                uuid.UUID  `db:"id"`
	ProjectID         uuid.UUID  `db:"project_id"`
	Type              string     `db:"type"`
	Enabled           bool       `db:"enabled"`
	Settings          []byte     `db:"settings"`
	AgentKey          *string    `db:"agent_key"`
	CreatedAt         time.Time  `db:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at"`
	CreatedBy         *uuid.UUID `db:"created_by"`
	KeyExpiresAt      *time.Time `db:"key_expires_at"`
	KeyExpirySource   *string    `db:"key_expiry_source"`
	LastSyncCheckedAt *time.Time `db:"last_sync_checked_at"`
	LastSyncStatus    *string    `db:"last_sync_status"`
	LastSyncError     *string    `db:"last_sync_error"`
}

func (r *projectIntegrationRow) toDomain() (domain.ProjectIntegration, error) {
	settings := r.Settings
	if settings == nil {
		settings = []byte("{}")
	}
	pi := domain.ProjectIntegration{
		ID:                r.ID,
		ProjectID:         r.ProjectID,
		Type:              r.Type,
		Enabled:           r.Enabled,
		Settings:          settings,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
		CreatedBy:         r.CreatedBy,
		KeyExpiresAt:      r.KeyExpiresAt,
		KeyExpirySource:   r.KeyExpirySource,
		LastSyncCheckedAt: r.LastSyncCheckedAt,
		LastSyncStatus:    r.LastSyncStatus,
		LastSyncError:     r.LastSyncError,
	}
	if r.AgentKey != nil && *r.AgentKey != "" {
		plain, err := encryption.Decrypt(*r.AgentKey)
		if err != nil {
			return pi, err
		}
		pi.AgentKey = plain
	}
	return pi, nil
}

// ProjectIntegrationRepo implements repository.ProjectIntegrationRepository using PostgreSQL.
type ProjectIntegrationRepo struct {
	db *sqlx.DB
}

// NewProjectIntegrationRepo creates a new ProjectIntegrationRepo.
func NewProjectIntegrationRepo(db *sqlx.DB) *ProjectIntegrationRepo {
	return &ProjectIntegrationRepo{db: db}
}

// Get retrieves the integration of the given type for the project, or nil if not found.
func (r *ProjectIntegrationRepo) Get(ctx context.Context, projectID uuid.UUID, intType string) (*domain.ProjectIntegration, error) {
	const q = `SELECT id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by, key_expires_at, key_expiry_source, last_sync_checked_at, last_sync_status, last_sync_error FROM project_integrations WHERE project_id = $1 AND type = $2`
	var row projectIntegrationRow
	if err := r.db.GetContext(ctx, &row, q, projectID, intType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	pi, err := row.toDomain()
	if err != nil {
		return nil, err
	}
	return &pi, nil
}

// GetByShareSlug finds the first enabled team_relay integration whose settings->>'share_slug' matches.
func (r *ProjectIntegrationRepo) GetByShareSlug(ctx context.Context, shareSlug string) (*domain.ProjectIntegration, error) {
	const q = `SELECT id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by, key_expires_at, key_expiry_source, last_sync_checked_at, last_sync_status, last_sync_error
	            FROM project_integrations
	            WHERE type = 'team_relay' AND enabled = true AND settings->>'share_slug' = $1
	            LIMIT 1`
	var row projectIntegrationRow
	if err := r.db.GetContext(ctx, &row, q, shareSlug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	pi, err := row.toDomain()
	if err != nil {
		return nil, err
	}
	return &pi, nil
}

// ListEnabledByType returns every enabled integration of the given type,
// across all projects. Ordered by project_id only for deterministic test
// output — callers that care about priority (e.g. never-checked first) sort
// after fetching.
func (r *ProjectIntegrationRepo) ListEnabledByType(ctx context.Context, intType string) ([]domain.ProjectIntegration, error) {
	const q = `SELECT id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by, key_expires_at, key_expiry_source, last_sync_checked_at, last_sync_status, last_sync_error FROM project_integrations WHERE type = $1 AND enabled = true ORDER BY project_id ASC`
	var rows []projectIntegrationRow
	if err := r.db.SelectContext(ctx, &rows, q, intType); err != nil {
		return nil, err
	}
	result := make([]domain.ProjectIntegration, 0, len(rows))
	for i := range rows {
		pi, err := rows[i].toDomain()
		if err != nil {
			return nil, err
		}
		result = append(result, pi)
	}
	return result, nil
}

// Upsert inserts or updates a project integration. When agent_key is NULL, the existing value is preserved.
//
// The write runs in a transaction because of the plaintext escape hatch below;
// see upsertQuery for why a single statement still needs one.
func (r *ProjectIntegrationRepo) Upsert(ctx context.Context, pi *domain.ProjectIntegration) error {
	settings := pi.Settings
	if settings == nil {
		settings = json.RawMessage("{}")
	}
	var encKey *string
	storingPlaintext := false
	if pi.AgentKey != "" {
		if pi.Type == teamRelayIntegrationType && !teamRelayAgentKeyPattern.MatchString(pi.AgentKey) {
			return apierror.BadRequest("team_relay agent_key must be \"tr_agent_\" followed by 48 lowercase hex characters")
		}
		enc, err := encryption.Encrypt(pi.AgentKey)
		if err != nil {
			return err
		}
		// Encrypt degrades to a passthrough on a deployment with no key
		// configured — a documented self-hosting mode. The DB trigger rejects
		// unencrypted credentials by default, so this one caller, which knows
		// encryption is genuinely unavailable rather than merely skipped, has
		// to say so explicitly.
		storingPlaintext = !encryption.IsEncrypted(enc)
		encKey = &enc
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if storingPlaintext {
		if _, err := tx.ExecContext(ctx, `SET LOCAL mesh.allow_plaintext_agent_key = 'on'`); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, upsertQuery,
		pi.ID, pi.ProjectID, pi.Type, pi.Enabled, []byte(settings), encKey,
		pi.CreatedAt, pi.UpdatedAt, pi.CreatedBy,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertQuery is separated out only so Upsert reads as the transaction it is.
// SET LOCAL is scoped to the surrounding transaction, which is what keeps the
// plaintext exemption from leaking to the next user of a pooled connection.
const upsertQuery = `
		INSERT INTO project_integrations (id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (project_id, type)
		DO UPDATE SET
			enabled    = EXCLUDED.enabled,
			settings   = EXCLUDED.settings,
			agent_key  = CASE WHEN EXCLUDED.agent_key IS NOT NULL THEN EXCLUDED.agent_key ELSE project_integrations.agent_key END,
			updated_at = EXCLUDED.updated_at
	`

// SetKeyExpiry records the credential's expiry and provenance for the given
// integration. expiresAt nil clears both fields. Separate from Upsert — see
// the interface doc comment for why folding this into Upsert would be a
// data-loss trap.
func (r *ProjectIntegrationRepo) SetKeyExpiry(ctx context.Context, projectID uuid.UUID, intType string, expiresAt *time.Time, source string) error {
	var src *string
	if expiresAt != nil {
		src = &source
	}
	const q = `UPDATE project_integrations
	            SET key_expires_at = $1, key_expiry_source = $2, updated_at = NOW()
	            WHERE project_id = $3 AND type = $4`
	res, err := r.db.ExecContext(ctx, q, expiresAt, src, projectID, intType)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("ProjectIntegration")
	}
	return nil
}

// RecordSyncCheck stamps the outcome of the most recent attempt to reach the
// integration's source. errMsg is persisted only for status "error" — an "ok"
// or "key_expired" outcome always clears any previously stored message, so a
// resolved error can't linger and be misread as still-current.
func (r *ProjectIntegrationRepo) RecordSyncCheck(ctx context.Context, projectID uuid.UUID, intType string, checkedAt time.Time, status, errMsg string) error {
	var errPtr *string
	if status == "error" && errMsg != "" {
		errPtr = &errMsg
	}
	const q = `UPDATE project_integrations
	            SET last_sync_checked_at = $1, last_sync_status = $2, last_sync_error = $3, updated_at = NOW()
	            WHERE project_id = $4 AND type = $5`
	res, err := r.db.ExecContext(ctx, q, checkedAt, status, errPtr, projectID, intType)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("ProjectIntegration")
	}
	return nil
}

// Delete removes the integration of the given type for the project.
func (r *ProjectIntegrationRepo) Delete(ctx context.Context, projectID uuid.UUID, intType string) error {
	const q = `DELETE FROM project_integrations WHERE project_id = $1 AND type = $2`
	res, err := r.db.ExecContext(ctx, q, projectID, intType)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("ProjectIntegration")
	}
	return nil
}

// ListByProject returns all integrations for a project, ordered by type.
func (r *ProjectIntegrationRepo) ListByProject(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectIntegration, error) {
	const q = `SELECT id, project_id, type, enabled, settings, agent_key, created_at, updated_at, created_by, key_expires_at, key_expiry_source, last_sync_checked_at, last_sync_status, last_sync_error FROM project_integrations WHERE project_id = $1 ORDER BY type ASC`
	var rows []projectIntegrationRow
	if err := r.db.SelectContext(ctx, &rows, q, projectID); err != nil {
		return nil, err
	}
	result := make([]domain.ProjectIntegration, 0, len(rows))
	for i := range rows {
		pi, err := rows[i].toDomain()
		if err != nil {
			return nil, err
		}
		result = append(result, pi)
	}
	return result, nil
}

// Ensure ProjectIntegrationRepo satisfies the repository.ProjectIntegrationRepository interface.
var _ repository.ProjectIntegrationRepository = (*ProjectIntegrationRepo)(nil)
