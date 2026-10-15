package domain

import (
	"time"

	"github.com/google/uuid"
)

// Limits on the externally supplied strings: they travel in event payloads.
const (
	MaxExternalRefLen  = 512
	MaxExternalHashLen = 128
)

// TaskExternalRef binds a card to an object in an external system. Hash is
// write-once: rewriting it with a different value is a conflict.
type TaskExternalRef struct {
	TaskID uuid.UUID  `json:"task_id" db:"task_id"`
	Ref    string     `json:"external_ref" db:"ref"`
	Hash   string     `json:"external_hash" db:"hash"`
	SetBy  *uuid.UUID `json:"set_by,omitempty" db:"set_by"`
	SetAt  time.Time  `json:"set_at" db:"set_at"`
}
