package domain

import (
	"time"

	"github.com/google/uuid"
)

// CheckoutScope identifies an acquire retry. A different session must wait for
// release/expiry rather than replacing another process with the same agent key.
type CheckoutScope struct {
	SessionID *uuid.UUID `json:"session_id,omitempty" db:"checkout_session_id"`
	RequestID *uuid.UUID `json:"request_id,omitempty" db:"checkout_request_id"`
}

// CheckoutExpectation is supplied by the client or captured at a status commit.
// It must never be filled from a later read of the current lease.
type CheckoutExpectation struct {
	Token      *uuid.UUID
	Holder     *uuid.UUID
	SessionID  *uuid.UUID
	Generation int64
	Legacy     bool
	Reason     string
}

type CheckoutLease struct {
	Holder     *uuid.UUID `db:"checked_out_by"`
	Token      *uuid.UUID `db:"checkout_token"`
	ExpiresAt  *time.Time `db:"checkout_expires"`
	AcquiredAt *time.Time `db:"checkout_acquired_at"`
	SessionID  *uuid.UUID `db:"checkout_session_id"`
	RequestID  *uuid.UUID `db:"checkout_request_id"`
	Generation int64      `db:"checkout_generation"`
	Changed    bool       `db:"changed"`
}
