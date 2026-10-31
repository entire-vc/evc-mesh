package domain

import (
	"time"

	"github.com/google/uuid"
)

// IdempotencyScope names one Idempotency-Key record: the same key from another
// actor, or on another method or path, is a different record.
type IdempotencyScope struct {
	ActorType ActorType
	ActorID   uuid.UUID
	Method    string
	Path      string
	Key       string
}

// Idempotency record states.
const (
	IdempotencyInFlight = "in_flight"
	IdempotencyDone     = "done"
)

// IdempotencyRecord is the stored state of a key: the fingerprint of the
// request that claimed it and, once that request answered, its response.
type IdempotencyRecord struct {
	RequestHash    string    `db:"request_hash"`
	State          string    `db:"state"`
	ResponseStatus *int      `db:"response_status"`
	ResponseType   *string   `db:"response_type"`
	ResponseBody   []byte    `db:"response_body"`
	ClaimedAt      time.Time `db:"claimed_at"`
}
