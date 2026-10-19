package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/extensions/uictx"
)

// SigningKeyRepo implements uictx.Store.
type SigningKeyRepo struct {
	db *sqlx.DB
}

// NewSigningKeyRepo creates a SigningKeyRepo.
func NewSigningKeyRepo(db *sqlx.DB) *SigningKeyRepo { return &SigningKeyRepo{db: db} }

type signingKeyRow struct {
	KID        string     `db:"kid"`
	PrivateEnc string     `db:"private_enc"`
	PublicJWK  []byte     `db:"public_jwk"`
	CreatedAt  time.Time  `db:"created_at"`
	RetiredAt  *time.Time `db:"retired_at"`
}

func (r *SigningKeyRepo) List(ctx context.Context) ([]uictx.KeyRow, error) {
	var rows []signingKeyRow
	if err := r.db.SelectContext(ctx, &rows,
		`SELECT kid, private_enc, public_jwk, created_at, retired_at FROM instance_signing_keys ORDER BY created_at DESC`); err != nil {
		return nil, err
	}
	out := make([]uictx.KeyRow, len(rows))
	for i, k := range rows {
		out[i] = uictx.KeyRow{KID: k.KID, PrivateEnc: k.PrivateEnc, PublicJWK: k.PublicJWK, CreatedAt: k.CreatedAt, RetiredAt: k.RetiredAt}
	}
	return out, nil
}

func (r *SigningKeyRepo) Activate(ctx context.Context, row uictx.KeyRow, at time.Time) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE instance_signing_keys SET retired_at = $1 WHERE retired_at IS NULL`, at); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO instance_signing_keys (kid, private_enc, public_jwk, created_at) VALUES ($1, $2, $3, $4)`,
		row.KID, row.PrivateEnc, row.PublicJWK, row.CreatedAt)
	var pgErr *pq.Error
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return uictx.ErrConflict
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SigningKeyRepo) PurgeRetiredBefore(ctx context.Context, t time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM instance_signing_keys WHERE retired_at IS NOT NULL AND retired_at < $1`, t)
	return err
}
