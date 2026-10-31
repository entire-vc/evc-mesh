package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// IdempotencyRepo stores Idempotency-Key records (table idempotency_keys).
type IdempotencyRepo struct {
	db *sqlx.DB
	// ttl is how long a key is remembered; stale is how long an in_flight claim
	// holds before another request with the key may take it over.
	ttl   time.Duration
	stale time.Duration
}

// NewIdempotencyRepo keeps keys for 24h; an in_flight claim older than 5
// minutes belongs to a request that died and is taken over.
func NewIdempotencyRepo(db *sqlx.DB) *IdempotencyRepo {
	return &IdempotencyRepo{db: db, ttl: 24 * time.Hour, stale: 5 * time.Minute}
}

const idemScopeWhere = `actor_type=$1 AND actor_id=$2 AND method=$3 AND path=$4 AND idem_key=$5`

// Claim takes the key for a request with fingerprint hash. It returns a claim
// token (non-nil) when this request owns the key now and must run, or the stored
// record when the key is already held: done (replay or mismatch) or in_flight
// (a concurrent request). An expired record, or an in_flight claim past the
// stale window, is dropped and claimed afresh under a new token.
func (r *IdempotencyRepo) Claim(ctx context.Context, s domain.IdempotencyScope, hash string) (*domain.IdempotencyRecord, uuid.UUID, error) {
	args := []any{string(s.ActorType), s.ActorID, s.Method, s.Path, s.Key}
	r.purgeExpired(ctx)
	for range 2 {
		if _, err := r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE `+idemScopeWhere+`
			AND (expires_at < now() OR (state='in_flight' AND claimed_at < now() - make_interval(secs => $6)))`,
			append(args, r.stale.Seconds())...); err != nil {
			return nil, uuid.Nil, fmt.Errorf("idempotency drop stale: %w", err)
		}
		token := uuid.New()
		res, err := r.db.ExecContext(ctx, `INSERT INTO idempotency_keys
			(actor_type, actor_id, method, path, idem_key, request_hash, claim_token, state, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'in_flight', now() + make_interval(secs => $8))
			ON CONFLICT DO NOTHING`, append(args, hash, token, r.ttl.Seconds())...)
		if err != nil {
			return nil, uuid.Nil, fmt.Errorf("idempotency claim: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return nil, token, nil
		}
		var rec domain.IdempotencyRecord
		err = r.db.GetContext(ctx, &rec, `SELECT request_hash, state, response_status, response_type, response_body, claimed_at
			FROM idempotency_keys WHERE `+idemScopeWhere, args...)
		if err == nil {
			return &rec, uuid.Nil, nil
		}
		// The holder released the key between our insert and select: try again.
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, uuid.Nil, fmt.Errorf("idempotency read: %w", err)
		}
	}
	return nil, uuid.Nil, fmt.Errorf("idempotency claim: key %q kept changing hands", s.Key)
}

// Complete stores the response of the request holding claim token. A request
// whose claim was taken over (token no longer on the row) changes nothing.
func (r *IdempotencyRepo) Complete(ctx context.Context, s domain.IdempotencyScope, token uuid.UUID, status int, contentType string, body []byte) error {
	_, err := r.db.ExecContext(ctx, `UPDATE idempotency_keys
		SET state='done', response_status=$7, response_type=$8, response_body=$9
		WHERE `+idemScopeWhere+` AND claim_token=$6 AND state='in_flight'`,
		string(s.ActorType), s.ActorID, s.Method, s.Path, s.Key, token, status, contentType, body)
	if err != nil {
		return fmt.Errorf("idempotency complete: %w", err)
	}
	return nil
}

// Release drops the in_flight claim held by token, whose request produced no
// answer worth replaying (a server error), so a retry runs again.
func (r *IdempotencyRepo) Release(ctx context.Context, s domain.IdempotencyScope, token uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE `+idemScopeWhere+` AND claim_token=$6 AND state='in_flight'`,
		string(s.ActorType), s.ActorID, s.Method, s.Path, s.Key, token)
	if err != nil {
		return fmt.Errorf("idempotency release: %w", err)
	}
	return nil
}

// purgeExpired removes a bounded batch of expired records; best effort.
func (r *IdempotencyRepo) purgeExpired(ctx context.Context) {
	_, _ = r.db.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE ctid IN
		(SELECT ctid FROM idempotency_keys WHERE expires_at < now() LIMIT 100)`)
}
