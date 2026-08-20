package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// secretRow is the masked shape returned by every read path except
// ResolveCurrentValues — it has no column for encrypted_value at all, so a
// query that forgot to select it cannot accidentally decrypt one either.
type secretRow struct {
	ID                uuid.UUID        `db:"id"`
	WorkspaceID       uuid.UUID        `db:"workspace_id"`
	ProjectID         *uuid.UUID       `db:"project_id"`
	AgentID           *uuid.UUID       `db:"agent_id"`
	Scope             string           `db:"scope"`
	Name              string           `db:"name"`
	ValueSHA256Prefix string           `db:"value_sha256_prefix"`
	ValueLength       int              `db:"value_length"`
	ValueCharClass    string           `db:"value_char_class"`
	ExpiresAt         *time.Time       `db:"expires_at"`
	CreatedBy         uuid.UUID        `db:"created_by"`
	CreatedByType     domain.ActorType `db:"created_by_type"`
	CreatedAt         time.Time        `db:"created_at"`
	RotatedAt         *time.Time       `db:"rotated_at"`
}

func (r *secretRow) toDomain() domain.Secret {
	return domain.Secret{
		ID:                r.ID,
		WorkspaceID:       r.WorkspaceID,
		ProjectID:         r.ProjectID,
		AgentID:           r.AgentID,
		Scope:             domain.SecretScope(r.Scope),
		Name:              r.Name,
		ValueSHA256Prefix: r.ValueSHA256Prefix,
		ValueLength:       r.ValueLength,
		ValueCharClass:    r.ValueCharClass,
		ExpiresAt:         r.ExpiresAt,
		CreatedBy:         r.CreatedBy,
		CreatedByType:     r.CreatedByType,
		CreatedAt:         r.CreatedAt,
		RotatedAt:         r.RotatedAt,
	}
}

const secretMaskedColumns = `id, workspace_id, project_id, agent_id, scope, name,
	value_sha256_prefix, value_length, value_char_class, expires_at,
	created_by, created_by_type, created_at, rotated_at`

// SecretRepo implements repository.SecretRepository and
// repository.SecretMaterializer using PostgreSQL. The two interfaces are
// separate; nothing about this struct forces a caller who only has a
// SecretRepository to reach ResolveCurrentValues.
type SecretRepo struct {
	db *sqlx.DB
}

// NewSecretRepo creates a new SecretRepo.
func NewSecretRepo(db *sqlx.DB) *SecretRepo {
	return &SecretRepo{db: db}
}

var valueClassRE = struct {
	lower, upper, digit, sym *regexp.Regexp
}{
	lower: regexp.MustCompile(`[a-z]`),
	upper: regexp.MustCompile(`[A-Z]`),
	digit: regexp.MustCompile(`\d`),
	sym:   regexp.MustCompile(`[^A-Za-z0-9]`),
}

// classify mirrors scripts/env-inventory.py's classify() exactly (same
// four buckets, same '+'-joined order) so the masked list view renders
// identically regardless of which side of the fleet is looking at a name.
func classify(v string) string {
	var parts []string
	if valueClassRE.lower.MatchString(v) {
		parts = append(parts, "a-z")
	}
	if valueClassRE.upper.MatchString(v) {
		parts = append(parts, "A-Z")
	}
	if valueClassRE.digit.MatchString(v) {
		parts = append(parts, "0-9")
	}
	if valueClassRE.sym.MatchString(v) {
		parts = append(parts, "sym")
	}
	if len(parts) == 0 {
		return "?"
	}
	return strings.Join(parts, "+")
}

// fingerprint mirrors scripts/env-inventory.py's fp(): sha256 hex, first 8
// chars. Computed from plaintext once at write time — the whole reason
// value_sha256_prefix is a stored column and not a query-time expression is
// that there is no plaintext left to compute it from afterward.
func fingerprint(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:8]
}

func scopeRefColumns(scope domain.SecretScope, projectID, agentID *uuid.UUID) (proj, agent *uuid.UUID, err error) {
	switch scope {
	case domain.SecretScopeWorkspace:
		if projectID != nil || agentID != nil {
			return nil, nil, apierror.ValidationError(map[string]string{"scope": "workspace scope takes no project_id or agent_id"})
		}
		return nil, nil, nil
	case domain.SecretScopeProject:
		if projectID == nil || agentID != nil {
			return nil, nil, apierror.ValidationError(map[string]string{"scope": "project scope requires project_id and no agent_id"})
		}
		return projectID, nil, nil
	case domain.SecretScopeAgent:
		if agentID == nil || projectID != nil {
			return nil, nil, apierror.ValidationError(map[string]string{"scope": "agent scope requires agent_id and no project_id"})
		}
		return nil, agentID, nil
	default:
		return nil, nil, apierror.ValidationError(map[string]string{"scope": "must be workspace, project, or agent"})
	}
}

// Create encrypts input.Value and inserts a new current row. The DB trigger
// (migration 20260820110) is the backstop that makes "plaintext never
// reaches the table" true even if this method's own Encrypt call were ever
// bypassed or removed by a future edit.
func (r *SecretRepo) Create(ctx context.Context, input domain.CreateSecretInput) (domain.Secret, error) {
	projectID, agentID, err := scopeRefColumns(input.Scope, input.ProjectID, input.AgentID)
	if err != nil {
		return domain.Secret{}, err
	}
	enc, err := encryption.Encrypt(input.Value)
	if err != nil {
		return domain.Secret{}, err
	}

	var row secretRow
	q := fmt.Sprintf(`
		INSERT INTO secrets (
			workspace_id, project_id, agent_id, scope, name, encrypted_value,
			value_sha256_prefix, value_length, value_char_class, expires_at,
			created_by, created_by_type
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING %s`, secretMaskedColumns)
	err = r.db.GetContext(ctx, &row, q,
		input.WorkspaceID, projectID, agentID, string(input.Scope), input.Name, enc,
		fingerprint(input.Value), len(input.Value), classify(input.Value), input.ExpiresAt,
		input.CreatedBy, input.CreatedByType,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.Secret{}, apierror.Conflict(fmt.Sprintf(
				"a current secret named %s already exists in this scope — use rotate to replace its value", input.Name))
		}
		return domain.Secret{}, err
	}
	return row.toDomain(), nil
}

// Rotate stamps the current row's rotated_at and inserts the replacement in
// one transaction, so a reader never observes a window with zero current
// rows for this name (either the old one is still current, or the new one
// already is).
func (r *SecretRepo) Rotate(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, input domain.CreateSecretInput) (domain.Secret, error) {
	pID, aID, err := scopeRefColumns(scope, projectID, agentID)
	if err != nil {
		return domain.Secret{}, err
	}
	enc, err := encryption.Encrypt(input.Value)
	if err != nil {
		return domain.Secret{}, err
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return domain.Secret{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, rotateSupersedeQuery(scope), buildScopeArgs(workspaceID, pID, aID, name)...)
	if err != nil {
		return domain.Secret{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.Secret{}, apierror.NotFound("Secret")
	}

	var row secretRow
	insertQ := fmt.Sprintf(`
		INSERT INTO secrets (
			workspace_id, project_id, agent_id, scope, name, encrypted_value,
			value_sha256_prefix, value_length, value_char_class, expires_at,
			created_by, created_by_type
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING %s`, secretMaskedColumns)
	if err := sqlx.GetContext(ctx, tx, &row, insertQ,
		workspaceID, pID, aID, string(scope), name, enc,
		fingerprint(input.Value), len(input.Value), classify(input.Value), input.ExpiresAt,
		input.CreatedBy, input.CreatedByType,
	); err != nil {
		return domain.Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Secret{}, err
	}
	return row.toDomain(), nil
}

// Delete stamps rotated_at on the current row without inserting a
// replacement. Same "append, don't erase" posture as Rotate — a deleted
// secret's history is still queryable, it just stops materializing.
func (r *SecretRepo) Delete(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, deletedBy uuid.UUID, deletedByType domain.ActorType) error {
	pID, aID, err := scopeRefColumns(scope, projectID, agentID)
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, rotateSupersedeQuery(scope), buildScopeArgs(workspaceID, pID, aID, name)...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apierror.NotFound("Secret")
	}
	_ = deletedBy
	_ = deletedByType
	return nil
}

// rotateSupersedeQuery/buildScopeArgs share the "stamp the current row"
// half of Rotate and Delete — the only difference between them is whether a
// replacement row follows. Scope is part of the WHERE clause (not just
// workspace/name) so a project-scope and an agent-scope secret sharing a
// name can never supersede each other's row by accident.
func rotateSupersedeQuery(scope domain.SecretScope) string {
	switch scope {
	case domain.SecretScopeWorkspace:
		return `UPDATE secrets SET rotated_at = now()
			WHERE workspace_id = $1 AND project_id IS NULL AND agent_id IS NULL
			  AND scope = 'workspace' AND name = $2 AND rotated_at IS NULL`
	case domain.SecretScopeProject:
		return `UPDATE secrets SET rotated_at = now()
			WHERE workspace_id = $1 AND project_id = $2 AND agent_id IS NULL
			  AND scope = 'project' AND name = $3 AND rotated_at IS NULL`
	default: // agent
		return `UPDATE secrets SET rotated_at = now()
			WHERE workspace_id = $1 AND agent_id = $2 AND project_id IS NULL
			  AND scope = 'agent' AND name = $3 AND rotated_at IS NULL`
	}
}

func buildScopeArgs(workspaceID uuid.UUID, projectID, agentID *uuid.UUID, name string) []interface{} {
	if projectID != nil {
		return []interface{}{workspaceID, *projectID, name}
	}
	if agentID != nil {
		return []interface{}{workspaceID, *agentID, name}
	}
	return []interface{}{workspaceID, name}
}

// ListCurrent returns masked metadata for every current secret reachable
// from the given resolution. It never selects encrypted_value.
func (r *SecretRepo) ListCurrent(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Secret, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM secrets
		WHERE rotated_at IS NULL AND (
			(scope = 'workspace' AND workspace_id = $1) OR
			(scope = 'project' AND workspace_id = $1 AND project_id = $2) OR
			(scope = 'agent' AND workspace_id = $1 AND agent_id = $3)
		)
		ORDER BY scope, name`, secretMaskedColumns)
	var rows []secretRow
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, projectID, agentID); err != nil {
		return nil, err
	}
	result := make([]domain.Secret, 0, len(rows))
	for i := range rows {
		result = append(result, rows[i].toDomain())
	}
	return result, nil
}

// ResolveCurrentValues is the ONLY method in this package that decrypts a
// secret's value. It exists for exactly one caller: the spawn-time
// materialization endpoint (S4). Every current secret in scope is decrypted
// unconditionally; expiry does not filter the row out, it flags it, so the
// caller can name the secret in a loud error instead of silently omitting
// the variable.
func (r *SecretRepo) ResolveCurrentValues(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.MaterializedSecret, error) {
	const q = `
		SELECT name, encrypted_value, expires_at FROM secrets
		WHERE rotated_at IS NULL AND (
			(scope = 'workspace' AND workspace_id = $1) OR
			(scope = 'project' AND workspace_id = $1 AND project_id = $2) OR
			(scope = 'agent' AND workspace_id = $1 AND agent_id = $3)
		)`
	type row struct {
		Name           string     `db:"name"`
		EncryptedValue string     `db:"encrypted_value"`
		ExpiresAt      *time.Time `db:"expires_at"`
	}
	var rows []row
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, projectID, agentID); err != nil {
		return nil, err
	}
	out := make([]domain.MaterializedSecret, 0, len(rows))
	for _, rr := range rows {
		expired := rr.ExpiresAt != nil && rr.ExpiresAt.Before(time.Now())
		m := domain.MaterializedSecret{Name: rr.Name, Expired: expired}
		if !expired {
			plain, err := encryption.Decrypt(rr.EncryptedValue)
			if err != nil {
				return nil, fmt.Errorf("secret %s: %w", rr.Name, err)
			}
			m.Value = plain
		}
		out = append(out, m)
	}
	return out, nil
}

// isUniqueViolation matches the convention already used in
// document_repo.go: *pq.Error with SQLSTATE 23505.
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

var _ repository.SecretRepository = (*SecretRepo)(nil)
var _ repository.SecretMaterializer = (*SecretRepo)(nil)
