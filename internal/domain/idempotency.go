package domain

import (
	"encoding/json"
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

// Routes whose writes record an idempotency receipt.
const (
	IdemRouteCommentCreate  = "comment.create"
	IdemRouteArtifactCreate = "artifact.create"
	IdemRouteTaskFinish     = "task.finish"
	IdemRouteSessionReport  = "session.report"
)

// IdempotencyReceiptScope names one receipt: one write per actor, route, task
// and key. TaskID is uuid.Nil for a write that belongs to no task.
type IdempotencyReceiptScope struct {
	ActorType ActorType
	ActorID   uuid.UUID
	Route     string
	TaskID    uuid.UUID
	Key       string
}

// IdempotencyReceipt is carried on a keyed write down to its transaction,
// which records it next to the written row. A write without a key has none.
type IdempotencyReceipt struct {
	IdempotencyReceiptScope
	RequestHash string
}

// IdempotentWrite is a write already recorded under a receipt's scope. As an
// error it is returned by a keyed write that finds its key taken inside its
// transaction (a concurrent repeat committed first); nothing was written.
type IdempotentWrite struct {
	ResourceID uuid.UUID       `json:"resource_id" db:"resource_id"`
	Response   json.RawMessage `json:"response" db:"response"`
	CreatedAt  time.Time       `json:"created_at" db:"created_at"`
	// SameRequest is false when the key was used with another request body.
	SameRequest bool `json:"-" db:"-"`
}

func (e *IdempotentWrite) Error() string {
	return "a write with this Idempotency-Key already exists: " + e.ResourceID.String()
}
