package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

const checkoutLeaseCols = `checked_out_by,checkout_token,checkout_expires,checkout_acquired_at,
 checkout_session_id,checkout_request_id,checkout_generation`

// The locked row supplies both the comparison and RETURNING result. A retry
// returns the original lease, including its deadline, rather than rotating it.
func (r *TaskRepo) AcquireCheckout(ctx context.Context, id, holder, token uuid.UUID, expires time.Time, scope domain.CheckoutScope) (*domain.CheckoutLease, error) {
	const q = `WITH previous AS MATERIALIZED (
 SELECT id,` + checkoutLeaseCols + `,
   checked_out_by=$2 AND checkout_session_id IS NOT DISTINCT FROM $5::uuid
   AND checkout_request_id IS NOT DISTINCT FROM $6::uuid
   AND (checkout_expires IS NULL OR checkout_expires>now()) AS retry
 FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE
), acquired AS (
 UPDATE tasks t SET checked_out_by=$2,
 checkout_token=CASE WHEN p.retry THEN p.checkout_token ELSE $3 END,
 checkout_expires=CASE WHEN p.retry AND $6::uuid IS NOT NULL THEN p.checkout_expires ELSE $4 END,
 checkout_acquired_at=CASE WHEN p.retry THEN p.checkout_acquired_at ELSE now() END,
 checkout_session_id=$5,checkout_request_id=$6,
 checkout_generation=CASE WHEN p.retry THEN p.checkout_generation ELSE p.checkout_generation+1 END,
 updated_at=CASE WHEN p.retry AND $6::uuid IS NOT NULL THEN t.updated_at ELSE now() END
 FROM previous p WHERE t.id=p.id
 AND (p.retry OR ((p.checked_out_by IS NULL OR p.checkout_expires<=now())
   AND NOT coalesce(($6::uuid IS NOT NULL AND p.checkout_request_id=$6 AND p.checkout_session_id=$5),false)))
 AND EXISTS (SELECT 1 FROM task_statuses s WHERE s.id=t.status_id AND s.category NOT IN ('done','cancelled'))
 RETURNING t.checked_out_by,t.checkout_token,t.checkout_expires,t.checkout_acquired_at,
 t.checkout_session_id,t.checkout_request_id,t.checkout_generation,NOT coalesce(p.retry,false) AS changed
) SELECT ` + checkoutLeaseCols + `,changed FROM acquired`
	var lease domain.CheckoutLease
	err := r.db.GetContext(ctx, &lease, q, id, holder, token, expires, scope.SessionID, scope.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCheckoutConflict
	}
	if err != nil {
		return nil, err
	}
	return &lease, nil
}

// A single conditional UPDATE releases only the expected lease. No later
// read supplies a missing generation. The previous row is returned for audit.
func (r *TaskRepo) CompareReleaseCheckout(ctx context.Context, id uuid.UUID, expected domain.CheckoutExpectation) (*domain.CheckoutLease, error) {
	const q = `WITH previous AS MATERIALIZED (
 SELECT id,` + checkoutLeaseCols + ` FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE
), released AS (
 UPDATE tasks t SET checked_out_by=NULL,checkout_token=NULL,checkout_expires=NULL,
 checkout_acquired_at=NULL,updated_at=now()
 FROM previous p WHERE t.id=p.id AND p.checked_out_by IS NOT NULL AND (
   ($2::uuid IS NOT NULL AND p.checkout_token=$2)
   OR ($2::uuid IS NULL AND p.checked_out_by=$3 AND (
     ($4::bigint>0 AND p.checkout_generation=$4 AND p.checkout_session_id IS NOT DISTINCT FROM $5::uuid)
     OR ($6::boolean AND $4=0 AND $5::uuid IS NULL AND p.checkout_session_id IS NULL))))
 RETURNING p.checked_out_by,p.checkout_token,p.checkout_expires,p.checkout_acquired_at,
 p.checkout_session_id,p.checkout_request_id,p.checkout_generation,true AS changed
) SELECT ` + checkoutLeaseCols + `,changed FROM released UNION ALL
 SELECT p.checked_out_by,p.checkout_token,p.checkout_expires,p.checkout_acquired_at,
 p.checkout_session_id,p.checkout_request_id,p.checkout_generation,false AS changed
 FROM previous p WHERE NOT EXISTS(SELECT 1 FROM released)`
	var lease domain.CheckoutLease
	err := r.db.GetContext(ctx, &lease, q, id, expected.Token, expected.Holder, expected.Generation, expected.SessionID, expected.Legacy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !lease.Changed {
		if lease.Holder != nil {
			return nil, ErrInvalidCheckoutToken
		}
		return nil, nil
	}
	return &lease, nil
}

func (r *TaskRepo) ExtendScopedCheckout(ctx context.Context, id uuid.UUID, expected domain.CheckoutExpectation, expires time.Time) (*domain.CheckoutLease, error) {
	const q = `UPDATE tasks SET checkout_expires=$2,updated_at=now()
 WHERE id=$1 AND deleted_at IS NULL AND checked_out_by IS NOT NULL AND checkout_expires>now()
 AND (($3::uuid IS NOT NULL AND checkout_token=$3) OR
  ($3::uuid IS NULL AND checkout_generation=$4 AND $4>0 AND checked_out_by=$5
   AND checkout_session_id IS NOT DISTINCT FROM $6::uuid))
 RETURNING ` + checkoutLeaseCols + `,true AS changed`
	var lease domain.CheckoutLease
	err := r.db.GetContext(ctx, &lease, q, id, expires, expected.Token, expected.Generation, expected.Holder, expected.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCheckoutToken
	}
	if err != nil {
		return nil, err
	}
	return &lease, nil
}
