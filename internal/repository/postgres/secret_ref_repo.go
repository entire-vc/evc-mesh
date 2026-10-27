package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// secretRow is the masked shape returned by every read path. secret_refs has
// no value column at all: the value is in Keep and only
// ResolveCurrentValues fetches it from there.
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

// SecretRefRepo implements repository.SecretRepository and
// repository.SecretMaterializer over the secret_refs index, with the values
// in Keep. The two interfaces are separate; nothing about this struct forces a
// caller who only has a SecretRepository to reach ResolveCurrentValues.
type SecretRefRepo struct {
	db   *sqlx.DB
	keep secretstore.Store
}

// NewSecretRefRepo creates a SecretRefRepo. keep is the Keep provider of the
// process secret store; nil (Keep not configured) refuses every write with
// 503 and answers every value read as degraded. There is no builtin fallback:
// a workspace secret has no other place to live.
func NewSecretRefRepo(db *sqlx.DB, keep secretstore.Store) *SecretRefRepo {
	return &SecretRefRepo{db: db, keep: keep}
}

// keepRefExtension and keepRef name a secret inside Keep the way
// cmd/migrate-secrets-to-keep did for the rows it moved, so a rotation of a
// moved secret writes a new version of the same Keep secret.
const keepRefExtension = "mesh-secret"

func keepRef(workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string) secretstore.Ref {
	scopeRef := "workspace"
	switch {
	case projectID != nil:
		scopeRef = projectID.String()
	case agentID != nil:
		scopeRef = agentID.String()
	}
	return secretstore.Ref{WorkspaceID: workspaceID, ExtensionID: keepRefExtension, Field: string(scope) + "." + scopeRef + "." + name}
}

// putKeep stores value in Keep under ref and checks it landed where the row
// already points. A Keep failure is a 503 naming the cause, never the value.
func (r *SecretRefRepo) putKeep(ctx context.Context, ref secretstore.Ref, stored, value string) error {
	if r.keep == nil {
		return apierror.ServiceUnavailable("workspace secrets are held by Keep, and Keep is not configured on this server")
	}
	got, err := r.keep.Put(ctx, ref, value)
	if errors.Is(err, secretstore.ErrKeepBadID) {
		return apierror.ValidationError(map[string]string{"name": "too long for a Keep reference in this scope"})
	}
	if err != nil {
		return apierror.ServiceUnavailable("Keep did not store the value: " + err.Error())
	}
	if got != stored {
		return apierror.ServiceUnavailable("Keep stored the value under an unexpected reference")
	}
	return nil
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

const insertSecretRefQuery = `
		INSERT INTO secret_refs (
			workspace_id, project_id, agent_id, scope, name, keep_ref,
			value_sha256_prefix, value_length, value_char_class, expires_at,
			created_by, created_by_type
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING ` + secretMaskedColumns

// Create claims the name with a new current row, stores the value in Keep and
// commits, in that order. The row goes first so a duplicate create fails on
// the unique index BEFORE anything is written to Keep: the Keep ref of a name
// is fixed, and a PUT for a name that already exists would replace the value
// of the current secret. A concurrent creator waits on the index until this
// transaction ends. A failed PUT rolls the row back.
func (r *SecretRefRepo) Create(ctx context.Context, input domain.CreateSecretInput) (domain.Secret, error) {
	projectID, agentID, err := scopeRefColumns(input.Scope, input.ProjectID, input.AgentID)
	if err != nil {
		return domain.Secret{}, err
	}
	ref := keepRef(input.WorkspaceID, input.Scope, projectID, agentID, input.Name)
	stored := secretstore.KeepStoredRef(ref)

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return domain.Secret{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var row secretRow
	err = sqlx.GetContext(ctx, tx, &row, insertSecretRefQuery,
		input.WorkspaceID, projectID, agentID, string(input.Scope), input.Name, stored,
		fingerprint(input.Value), len(input.Value), classify(input.Value), input.ExpiresAt,
		input.CreatedBy, input.CreatedByType,
	)
	if err != nil {
		if isSecretsUniqueViolation(err) {
			return domain.Secret{}, apierror.Conflict(fmt.Sprintf(
				"a current secret named %s already exists in this scope — use rotate to replace its value", input.Name))
		}
		return domain.Secret{}, err
	}
	if err := r.putKeep(ctx, ref, stored, input.Value); err != nil {
		return domain.Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Secret{}, err
	}
	return row.toDomain(), nil
}

// Rotate stamps the current row's rotated_at, inserts the replacement and
// writes the new value to Keep as a new version of the same Keep secret, all
// before the commit: a reader never sees a window with zero current rows, and
// a failed PUT leaves the old row current with Keep untouched. Keep has no
// transaction to join, so the PUT is compensated instead: the current value
// is read first, and if the commit fails after the PUT it is written back,
// leaving Keep and the current row describing the same value. The superseded
// row stays locked until then, so a concurrent rotate waits and then gets 409.
func (r *SecretRefRepo) Rotate(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, input domain.CreateSecretInput) (domain.Secret, error) {
	pID, aID, err := scopeRefColumns(scope, projectID, agentID)
	if err != nil {
		return domain.Secret{}, err
	}
	ref := keepRef(workspaceID, scope, pID, aID, name)
	stored := secretstore.KeepStoredRef(ref)

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return domain.Secret{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var prevRef string
	err = tx.QueryRowxContext(ctx, rotateSupersedeQuery(scope), buildScopeArgs(workspaceID, pID, aID, name)...).Scan(&prevRef)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Secret{}, apierror.NotFound("Secret")
	}
	if err != nil {
		return domain.Secret{}, err
	}

	var row secretRow
	if err := sqlx.GetContext(ctx, tx, &row, insertSecretRefQuery,
		workspaceID, pID, aID, string(scope), name, stored,
		fingerprint(input.Value), len(input.Value), classify(input.Value), input.ExpiresAt,
		input.CreatedBy, input.CreatedByType,
	); err != nil {
		return domain.Secret{}, err
	}
	// The current value must be readable before anything is overwritten. The
	// PUT replaces what the old row points at only when both name the same
	// Keep secret; otherwise a failed commit leaves an unused version, and
	// the value read here is not needed to undo it.
	var prev *secretstore.Result
	if r.keep != nil {
		res := r.keep.Resolve(ctx, prevRef)
		if res.Degraded {
			return domain.Secret{}, apierror.ServiceUnavailable("Keep could not read the current value, so a failed rotation could not be undone; nothing was changed: " + res.Reason)
		}
		if prevRef == stored {
			prev = &res
		}
	}
	if err := r.putKeep(ctx, ref, stored, input.Value); err != nil {
		return domain.Secret{}, err
	}
	if err := tx.Commit(); err != nil {
		if prev != nil {
			if _, perr := r.keep.Put(ctx, ref, prev.Value); perr != nil {
				return domain.Secret{}, fmt.Errorf("rotate %s: commit failed (%w) and Keep could not be given the previous value back (%v); Keep serves the new value, rotate it again", name, err, perr)
			}
		}
		return domain.Secret{}, err
	}
	return row.toDomain(), nil
}

// Delete stamps rotated_at on the current row without inserting a
// replacement. Same "append, don't erase" posture as Rotate — a deleted
// secret's history is still queryable, it just stops materializing. The Keep
// secret stays (Keep's Mesh API has no delete); a later create of the same
// name writes a new version of it.
func (r *SecretRefRepo) Delete(ctx context.Context, workspaceID uuid.UUID, scope domain.SecretScope, projectID, agentID *uuid.UUID, name string, deletedBy uuid.UUID, deletedByType domain.ActorType) error {
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
		return `UPDATE secret_refs SET rotated_at = now()
			WHERE workspace_id = $1 AND project_id IS NULL AND agent_id IS NULL
			  AND scope = 'workspace' AND name = $2 AND rotated_at IS NULL
			RETURNING keep_ref`
	case domain.SecretScopeProject:
		return `UPDATE secret_refs SET rotated_at = now()
			WHERE workspace_id = $1 AND project_id = $2 AND agent_id IS NULL
			  AND scope = 'project' AND name = $3 AND rotated_at IS NULL
			RETURNING keep_ref`
	default: // agent
		return `UPDATE secret_refs SET rotated_at = now()
			WHERE workspace_id = $1 AND agent_id = $2 AND project_id IS NULL
			  AND scope = 'agent' AND name = $3 AND rotated_at IS NULL
			RETURNING keep_ref`
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
// from the given resolution.
func (r *SecretRefRepo) ListCurrent(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.Secret, error) {
	q := fmt.Sprintf(`
		SELECT %s FROM secret_refs
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

// GetByID returns one row's masked metadata, scoped to workspaceID. It
// selects secretMaskedColumns like every other read path here, so the value
// has no column to arrive in. The workspace_id predicate is part of the
// WHERE clause rather than a post-fetch check: a row belonging to another
// tenant produces the same NotFound as an id that names nothing, so the API
// cannot be used to probe for the existence of another workspace's secrets.
func (r *SecretRefRepo) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (domain.Secret, error) {
	q := fmt.Sprintf(`SELECT %s FROM secret_refs WHERE id = $1 AND workspace_id = $2`, secretMaskedColumns)
	var row secretRow
	if err := r.db.GetContext(ctx, &row, q, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Secret{}, apierror.NotFound("Secret")
		}
		return domain.Secret{}, err
	}
	return row.toDomain(), nil
}

// AssertScopeRefInWorkspace verifies in one round-trip that whichever of
// project_id / agent_id the caller supplied belongs to workspaceID.
//
// This is not redundant with the table's foreign keys. secret_refs.project_id
// references projects(id) with no workspace predicate, so a create naming
// another tenant's project id satisfies the FK, the scope CHECK and the
// partial unique index alike — the row lands, referencing a project the
// caller cannot see. Nothing downstream would catch it either: the
// materializer matches on workspace_id AND project_id together, so the row
// would simply never resolve, which reads as a silently broken secret rather
// than as the tenancy error it is.
func (r *SecretRefRepo) AssertScopeRefInWorkspace(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) error {
	if projectID == nil && agentID == nil {
		return nil
	}
	const q = `SELECT
		($2::uuid IS NULL OR EXISTS (
			SELECT 1 FROM projects WHERE id = $2 AND workspace_id = $1 AND deleted_at IS NULL)) AS project_ok,
		($3::uuid IS NULL OR EXISTS (
			SELECT 1 FROM agents WHERE id = $3 AND workspace_id = $1 AND deleted_at IS NULL)) AS agent_ok`
	var res struct {
		ProjectOK bool `db:"project_ok"`
		AgentOK   bool `db:"agent_ok"`
	}
	if err := r.db.GetContext(ctx, &res, q, workspaceID, projectID, agentID); err != nil {
		return err
	}
	fields := map[string]string{}
	if !res.ProjectOK {
		fields["project_id"] = "must name a project in this workspace"
	}
	if !res.AgentOK {
		fields["agent_id"] = "must name an agent in this workspace"
	}
	if len(fields) > 0 {
		return apierror.ValidationError(fields)
	}
	return nil
}

// ResolveCurrentValues is the ONLY method in this package that produces a
// secret's value, and it gets every one from Keep. It exists for the
// spawn-time materialization endpoint and the extension signing paths. Expiry
// does not filter a row out, it flags it, so the caller can name the secret in
// a loud error instead of silently omitting the variable. ValueSHA256Prefix is
// the fingerprint of the value Keep returned, not the one stored at write
// time, so the issuance journal names the version actually handed out.
func (r *SecretRefRepo) ResolveCurrentValues(ctx context.Context, workspaceID uuid.UUID, projectID, agentID *uuid.UUID) ([]domain.MaterializedSecret, error) {
	const q = `
		SELECT id, name, keep_ref, value_sha256_prefix, expires_at FROM secret_refs
		WHERE rotated_at IS NULL AND (
			(scope = 'workspace' AND workspace_id = $1) OR
			(scope = 'project' AND workspace_id = $1 AND project_id = $2) OR
			(scope = 'agent' AND workspace_id = $1 AND agent_id = $3)
		)`
	type row struct {
		ID                uuid.UUID  `db:"id"`
		Name              string     `db:"name"`
		KeepRef           string     `db:"keep_ref"`
		ValueSHA256Prefix string     `db:"value_sha256_prefix"`
		ExpiresAt         *time.Time `db:"expires_at"`
	}
	var rows []row
	if err := r.db.SelectContext(ctx, &rows, q, workspaceID, projectID, agentID); err != nil {
		return nil, err
	}
	out := make([]domain.MaterializedSecret, 0, len(rows))
	for _, rr := range rows {
		expired := rr.ExpiresAt != nil && rr.ExpiresAt.Before(time.Now())
		m := domain.MaterializedSecret{ID: rr.ID, Name: rr.Name, ValueSHA256Prefix: rr.ValueSHA256Prefix, Expired: expired}
		if !expired {
			// Degraded is an error, never an empty Value — an empty variable
			// would read as "no secret".
			if r.keep == nil {
				return nil, &domain.SecretDegradedError{Name: rr.Name, Reason: secretstore.ErrKeepNotConfigured.Error()}
			}
			res := r.keep.Resolve(ctx, rr.KeepRef)
			if res.Degraded {
				return nil, &domain.SecretDegradedError{Name: rr.Name, Reason: res.Reason}
			}
			m.Value = res.Value
			m.ValueSHA256Prefix = fingerprint(res.Value)
		}
		out = append(out, m)
	}
	return out, nil
}

// isSecretsUniqueViolation matches the convention already used in
// document_repo.go: *pq.Error with SQLSTATE 23505. Named for this package
// rather than plain isUniqueViolation — document_external_source_db_test.go
// (merged same day, unrelated PR) declares its own same-package helper of
// that name for a different signature; two files in one package can't share
// an unexported symbol name.
func isSecretsUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

var _ repository.SecretRepository = (*SecretRefRepo)(nil)
var _ repository.SecretMaterializer = (*SecretRefRepo)(nil)
