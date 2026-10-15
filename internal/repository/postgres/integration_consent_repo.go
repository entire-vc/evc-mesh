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
}

// GetConsent returns the recorded consent, or (nil, nil) when the connection
// does not exist or has no consent recorded.
func (r *IntegrationConsentRepo) GetConsent(ctx context.Context, workspaceID uuid.UUID, extensionID string) (*domain.ExtensionConsent, error) {
	const q = `SELECT consented_capabilities, consented_at, consented_by
		FROM integration_configs WHERE workspace_id = $1 AND provider = $2`
	var row consentRow
	if err := r.db.GetContext(ctx, &row, q, workspaceID, extensionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if row.Capabilities == nil {
		return nil, nil
	}
	var caps []string
	if err := json.Unmarshal(row.Capabilities, &caps); err != nil {
		return nil, err
	}
	c := &domain.ExtensionConsent{Capabilities: caps}
	if row.At.Valid {
		c.At = row.At.Time
	}
	if row.By.Valid {
		by := row.By.UUID
		c.By = &by
	}
	return c, nil
}

// SetConsent records consent on a connection.
func (r *IntegrationConsentRepo) SetConsent(ctx context.Context, connectionID uuid.UUID, c domain.ExtensionConsent) error {
	caps := c.Capabilities
	if caps == nil {
		caps = []string{}
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		return err
	}
	at := c.At
	if at.IsZero() {
		at = time.Now()
	}
	const q = `UPDATE integration_configs
		SET consented_capabilities = $2, consented_at = $3, consented_by = $4
		WHERE id = $1`
	res, err := r.db.ExecContext(ctx, q, connectionID, raw, at, c.By)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apierror.NotFound("Integration")
	}
	return nil
}

var _ repository.ExtensionConsentRepository = (*IntegrationConsentRepo)(nil)
