package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// IntegrationConsentRepo implements repository.ExtensionConsentRepository on
// the consent columns of integration_configs.
type IntegrationConsentRepo struct {
	db *sqlx.DB
}

// NewIntegrationConsentRepo creates a new IntegrationConsentRepo.
func NewIntegrationConsentRepo(db *sqlx.DB) *IntegrationConsentRepo {
	return &IntegrationConsentRepo{db: db}
}

type consentRow struct {
	Capabilities []byte        `db:"consented_capabilities"`
	At           sql.NullTime  `db:"consented_at"`
	By           uuid.NullUUID `db:"consented_by"`
	Version      string        `db:"consented_version"`
	Commit       string        `db:"consented_commit"`
}

const consentCols = `consented_capabilities, consented_at, consented_by, consented_version, consented_commit`

func (row consentRow) consent() (*domain.ExtensionConsent, error) {
	if row.Capabilities == nil {
		return nil, nil
	}
	var caps []string
	if err := json.Unmarshal(row.Capabilities, &caps); err != nil {
		return nil, err
	}
	c := &domain.ExtensionConsent{Capabilities: caps, Version: row.Version, Commit: row.Commit}
	if row.At.Valid {
		c.At = row.At.Time
	}
	if row.By.Valid {
		by := row.By.UUID
		c.By = &by
	}
	return c, nil
}

// GetConsent returns the recorded consent, or (nil, nil) when the connection
// does not exist or has no consent recorded.
func (r *IntegrationConsentRepo) GetConsent(ctx context.Context, workspaceID uuid.UUID, extensionID string) (*domain.ExtensionConsent, error) {
	q := `SELECT ` + consentCols + ` FROM integration_configs WHERE workspace_id = $1 AND provider = $2`
	var row consentRow
	if err := r.db.GetContext(ctx, &row, q, workspaceID, extensionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.consent()
}

// SetConsent records consent on a connection.
func (r *IntegrationConsentRepo) SetConsent(ctx context.Context, connectionID uuid.UUID, c domain.ExtensionConsent) error {
	res, err := setConsent(ctx, r.db, connectionID, c)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("Integration")
	}
	return nil
}

func setConsent(ctx context.Context, db sqlx.ExecerContext, connectionID uuid.UUID, c domain.ExtensionConsent) (sql.Result, error) {
	caps := c.Capabilities
	if caps == nil {
		caps = []string{}
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		return nil, err
	}
	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	const q = `UPDATE integration_configs
		SET consented_capabilities = $2, consented_at = $3, consented_by = $4,
		    consented_version = $5, consented_commit = $6
		WHERE id = $1`
	return db.ExecContext(ctx, q, connectionID, raw, at, c.By, c.Version, c.Commit)
}

// Reconsent replaces the consent and the actor snapshot in one transaction
// (see repository.ExtensionConsentRepository).
func (r *IntegrationConsentRepo) Reconsent(ctx context.Context, connectionID uuid.UUID, extensionID string, resolve domain.ExtensionReconsentResolve) (*domain.ExtensionConsent, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// The connection row first: two concurrent consents serialize here.
	var exists bool
	if err = tx.GetContext(ctx, &exists, `SELECT true FROM integration_configs WHERE id = $1 FOR UPDATE`, connectionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apierror.NotFound("Integration")
		}
		return nil, err
	}
	// FOR SHARE conflicts with the UPDATE an instance update, rollback or
	// re-install makes on this row, so the release resolve sees is the one
	// installed when the consent commits.
	var target domain.ExtensionReconsentTarget
	var inst domain.InstalledExtension
	err = tx.GetContext(ctx, &inst, `SELECT `+installedExtensionCols+` FROM installed_extensions WHERE extension_id = $1 FOR SHARE`, extensionID)
	switch {
	case err == nil:
		target.Installed = &inst
	case errors.Is(err, sql.ErrNoRows):
	default:
		return nil, err
	}

	c, actorCaps, err := resolve(target)
	if err != nil {
		return nil, err
	}
	if _, err := setConsent(ctx, tx, connectionID, c); err != nil {
		return nil, err
	}
	if actorCaps != nil {
		// No actor yet (no token issued) is fine: the first token is issued
		// from this consent.
		if _, err := tx.ExecContext(ctx, `UPDATE extension_actors SET capabilities = $2 WHERE connection_id = $1`, connectionID, actorCaps); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

var _ repository.ExtensionConsentRepository = (*IntegrationConsentRepo)(nil)
