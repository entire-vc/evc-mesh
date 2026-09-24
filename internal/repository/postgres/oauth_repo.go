package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Ensure OAuthRepo implements repository.OAuthRepository at compile time.
var _ repository.OAuthRepository = (*OAuthRepo)(nil)

// OAuthRepo implements repository.OAuthRepository with PostgreSQL, across
// the four tables migration 20260924004 created: oauth_clients,
// oauth_authorization_codes, oauth_grants, oauth_tokens.
type OAuthRepo struct {
	db *sqlx.DB
}

// NewOAuthRepo creates a new OAuthRepo.
func NewOAuthRepo(db *sqlx.DB) *OAuthRepo {
	return &OAuthRepo{db: db}
}

// --- Clients ---

const oauthClientCols = `id, client_id, registration_type, client_name, redirect_uris, token_endpoint_auth_method, grant_types, metadata, metadata_fetched_at, created_at, updated_at`

func (r *OAuthRepo) CreateClient(ctx context.Context, c *domain.OAuthClient) error {
	const q = `
		INSERT INTO oauth_clients (id, client_id, registration_type, client_name, redirect_uris, token_endpoint_auth_method, grant_types, metadata, metadata_fetched_at, created_at, updated_at)
		VALUES (:id, :client_id, :registration_type, :client_name, :redirect_uris, :token_endpoint_auth_method, :grant_types, :metadata, :metadata_fetched_at, :created_at, :updated_at)
	`
	_, err := r.db.NamedExecContext(ctx, q, c)
	return err
}

func (r *OAuthRepo) GetClientByClientID(ctx context.Context, clientID string) (*domain.OAuthClient, error) {
	q := `SELECT ` + oauthClientCols + ` FROM oauth_clients WHERE client_id = $1`
	var c domain.OAuthClient
	if err := r.db.GetContext(ctx, &c, q, clientID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (r *OAuthRepo) UpdateClientMetadata(ctx context.Context, c *domain.OAuthClient) error {
	const q = `
		UPDATE oauth_clients
		SET client_name = :client_name, redirect_uris = :redirect_uris, grant_types = :grant_types,
		    metadata = :metadata, metadata_fetched_at = :metadata_fetched_at, updated_at = :updated_at
		WHERE id = :id
	`
	_, err := r.db.NamedExecContext(ctx, q, c)
	return err
}

// --- Authorization codes ---

const oauthCodeCols = `id, code_hash, client_id, redirect_uri, code_challenge, code_challenge_method, grant_id, expires_at, used_at, issued_family_id, created_at`

func (r *OAuthRepo) CreateCode(ctx context.Context, code *domain.OAuthAuthorizationCode) error {
	const q = `
		INSERT INTO oauth_authorization_codes (id, code_hash, client_id, redirect_uri, code_challenge, code_challenge_method, grant_id, expires_at, used_at, issued_family_id, created_at)
		VALUES (:id, :code_hash, :client_id, :redirect_uri, :code_challenge, :code_challenge_method, :grant_id, :expires_at, :used_at, :issued_family_id, :created_at)
	`
	_, err := r.db.NamedExecContext(ctx, q, code)
	return err
}

func (r *OAuthRepo) GetCodeByHash(ctx context.Context, codeHash string) (*domain.OAuthAuthorizationCode, error) {
	q := `SELECT ` + oauthCodeCols + ` FROM oauth_authorization_codes WHERE code_hash = $1`
	var c domain.OAuthAuthorizationCode
	if err := r.db.GetContext(ctx, &c, q, codeHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func (r *OAuthRepo) MarkCodeUsed(ctx context.Context, id uuid.UUID, now time.Time, familyID uuid.UUID) (bool, error) {
	const q = `UPDATE oauth_authorization_codes SET used_at = $2, issued_family_id = $3 WHERE id = $1 AND used_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id, now, familyID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *OAuthRepo) DeleteExpiredCodes(ctx context.Context, before time.Time) (int64, error) {
	const q = `DELETE FROM oauth_authorization_codes WHERE expires_at < $1`
	res, err := r.db.ExecContext(ctx, q, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- Grants ---

const oauthGrantCols = `id, user_id, client_id, workspace_id, agent_id, scope, created_at, revoked_at`

func (r *OAuthRepo) CreateGrant(ctx context.Context, g *domain.OAuthGrant) error {
	const q = `
		INSERT INTO oauth_grants (id, user_id, client_id, workspace_id, agent_id, scope, created_at, revoked_at)
		VALUES (:id, :user_id, :client_id, :workspace_id, :agent_id, :scope, :created_at, :revoked_at)
	`
	_, err := r.db.NamedExecContext(ctx, q, g)
	return err
}

func (r *OAuthRepo) GetGrantByID(ctx context.Context, id uuid.UUID) (*domain.OAuthGrant, error) {
	q := `SELECT ` + oauthGrantCols + ` FROM oauth_grants WHERE id = $1`
	var g domain.OAuthGrant
	if err := r.db.GetContext(ctx, &g, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

func (r *OAuthRepo) GetGrantByUserClientWorkspace(ctx context.Context, userID uuid.UUID, clientID string, workspaceID uuid.UUID) (*domain.OAuthGrant, error) {
	q := `SELECT ` + oauthGrantCols + ` FROM oauth_grants WHERE user_id = $1 AND client_id = $2 AND workspace_id = $3`
	var g domain.OAuthGrant
	if err := r.db.GetContext(ctx, &g, q, userID, clientID, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

func (r *OAuthRepo) RetargetGrant(ctx context.Context, id, agentID uuid.UUID) error {
	const q = `UPDATE oauth_grants SET agent_id = $2, revoked_at = NULL WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id, agentID)
	return err
}

func (r *OAuthRepo) ReactivateGrant(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE oauth_grants SET revoked_at = NULL WHERE id = $1`
	_, err := r.db.ExecContext(ctx, q, id)
	return err
}

// oauthGrantListRow is the flat scan target for ListGrantsByUser's JOIN —
// sqlx has no nested-struct dotted-alias mapping, same reason
// agentWorkspaceGrantWorkspaceRow exists in agent_workspace_grant_repo.go.
type oauthGrantListRow struct {
	ID            uuid.UUID  `db:"id"`
	UserID        uuid.UUID  `db:"user_id"`
	ClientID      string     `db:"client_id"`
	WorkspaceID   uuid.UUID  `db:"workspace_id"`
	AgentID       uuid.UUID  `db:"agent_id"`
	Scope         string     `db:"scope"`
	CreatedAt     time.Time  `db:"created_at"`
	RevokedAt     *time.Time `db:"revoked_at"`
	ClientName    string     `db:"client_name"`
	AgentName     string     `db:"agent_name"`
	WorkspaceName string     `db:"workspace_name"`
	WorkspaceSlug string     `db:"workspace_slug"`
}

func (r *OAuthRepo) ListGrantsByUser(ctx context.Context, userID uuid.UUID) ([]domain.OAuthGrantWithDetails, error) {
	const q = `
		SELECT
			g.id, g.user_id, g.client_id, g.workspace_id, g.agent_id, g.scope, g.created_at, g.revoked_at,
			c.client_name AS client_name, a.name AS agent_name,
			w.name AS workspace_name, w.slug AS workspace_slug
		FROM oauth_grants g
		JOIN oauth_clients c ON c.client_id = g.client_id
		JOIN agents a ON a.id = g.agent_id
		JOIN workspaces w ON w.id = g.workspace_id
		WHERE g.user_id = $1
		ORDER BY g.created_at DESC
	`
	var rows []oauthGrantListRow
	if err := r.db.SelectContext(ctx, &rows, q, userID); err != nil {
		return nil, err
	}
	result := make([]domain.OAuthGrantWithDetails, len(rows))
	for i, row := range rows {
		result[i] = domain.OAuthGrantWithDetails{
			OAuthGrant: domain.OAuthGrant{
				ID: row.ID, UserID: row.UserID, ClientID: row.ClientID, WorkspaceID: row.WorkspaceID,
				AgentID: row.AgentID, Scope: row.Scope, CreatedAt: row.CreatedAt, RevokedAt: row.RevokedAt,
			},
			ClientName: row.ClientName,
			AgentName:  row.AgentName,
			Workspace: domain.WorkspaceBrief{
				ID: row.WorkspaceID, Name: row.WorkspaceName, Slug: row.WorkspaceSlug,
			},
		}
	}
	return result, nil
}

func (r *OAuthRepo) RevokeGrant(ctx context.Context, id uuid.UUID, now time.Time) error {
	const q = `UPDATE oauth_grants SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, id, now)
	return err
}

// --- Tokens ---

const oauthTokenCols = `id, grant_id, token_type, token_hash, family_id, parent_token_id, expires_at, revoked_at, created_at`

func (r *OAuthRepo) CreateToken(ctx context.Context, t *domain.OAuthToken) error {
	const q = `
		INSERT INTO oauth_tokens (id, grant_id, token_type, token_hash, family_id, parent_token_id, expires_at, revoked_at, created_at)
		VALUES (:id, :grant_id, :token_type, :token_hash, :family_id, :parent_token_id, :expires_at, :revoked_at, :created_at)
	`
	_, err := r.db.NamedExecContext(ctx, q, t)
	return err
}

func (r *OAuthRepo) GetTokenByHash(ctx context.Context, tokenHash string) (*domain.OAuthToken, error) {
	q := `SELECT ` + oauthTokenCols + ` FROM oauth_tokens WHERE token_hash = $1`
	var t domain.OAuthToken
	if err := r.db.GetContext(ctx, &t, q, tokenHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (r *OAuthRepo) RevokeToken(ctx context.Context, id uuid.UUID, now time.Time) (bool, error) {
	const q = `UPDATE oauth_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`
	res, err := r.db.ExecContext(ctx, q, id, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *OAuthRepo) RevokeFamily(ctx context.Context, familyID uuid.UUID, now time.Time) error {
	const q = `UPDATE oauth_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, familyID, now)
	return err
}

func (r *OAuthRepo) RevokeTokensByGrant(ctx context.Context, grantID uuid.UUID, now time.Time) error {
	const q = `UPDATE oauth_tokens SET revoked_at = $2 WHERE grant_id = $1 AND revoked_at IS NULL`
	_, err := r.db.ExecContext(ctx, q, grantID, now)
	return err
}

// insertTokensTx inserts every token row on tx, in order.
func insertTokensTx(ctx context.Context, tx *sqlx.Tx, tokens []*domain.OAuthToken) error {
	const q = `
		INSERT INTO oauth_tokens (id, grant_id, token_type, token_hash, family_id, parent_token_id, expires_at, revoked_at, created_at)
		VALUES (:id, :grant_id, :token_type, :token_hash, :family_id, :parent_token_id, :expires_at, :revoked_at, :created_at)
	`
	for _, t := range tokens {
		if _, err := tx.NamedExecContext(ctx, q, t); err != nil {
			return err
		}
	}
	return nil
}

func (r *OAuthRepo) RedeemCode(ctx context.Context, codeID uuid.UUID, now time.Time, familyID uuid.UUID, tokens []*domain.OAuthToken) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	const q = `UPDATE oauth_authorization_codes SET used_at = $2, issued_family_id = $3 WHERE id = $1 AND used_at IS NULL`
	res, err := tx.ExecContext(ctx, q, codeID, now, familyID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := insertTokensTx(ctx, tx, tokens); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *OAuthRepo) RotateRefreshToken(ctx context.Context, oldTokenID uuid.UUID, now time.Time, tokens []*domain.OAuthToken) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	const q = `UPDATE oauth_tokens SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`
	res, err := tx.ExecContext(ctx, q, oldTokenID, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := insertTokensTx(ctx, tx, tokens); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
