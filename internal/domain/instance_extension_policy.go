package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Journal actions written by the instance-level extension admin paths.
const (
	InstanceJournalSettingsChanged   = "settings_changed"
	InstanceJournalAllowChanged      = "allow_changed"
	InstanceJournalAutoUpdateChanged = "auto_update_changed"
	InstanceJournalInstalled         = "installed"
	InstanceJournalUpdated           = "updated"
	InstanceJournalRolledBack        = "rolled_back"
	InstanceJournalUpdatePending     = "update_pending_consent"
)

// InstanceExtensionPolicy is an explicit allow/deny of one extension.
type InstanceExtensionPolicy struct {
	ExtensionID string     `db:"extension_id"`
	Allowed     bool       `db:"allowed"`
	UpdatedBy   *uuid.UUID `db:"updated_by"`
	UpdatedAt   time.Time  `db:"updated_at"`
}

// InstanceExtensionConfig holds the instance-wide extension flags.
type InstanceExtensionConfig struct {
	AutoUpdate bool       `db:"auto_update"`
	UpdatedBy  *uuid.UUID `db:"updated_by"`
	UpdatedAt  time.Time  `db:"updated_at"`
}

// InstanceExtensionJournalEntry is one admin action: who, what, when.
type InstanceExtensionJournalEntry struct {
	ID          uuid.UUID       `db:"id"`
	ActorID     *uuid.UUID      `db:"actor_id"`
	ActorName   string          `db:"actor_name"` // filled by ListJournal only
	ExtensionID string          `db:"extension_id"`
	Action      string          `db:"action"`
	Details     json.RawMessage `db:"details"`
	CreatedAt   time.Time       `db:"created_at"`
}

// DefaultExtensionAllowed is the allow-list default for a trust level:
// official and verified (and the builtin class) are on, community and custom
// need an explicit allow.
func DefaultExtensionAllowed(trust string) bool {
	switch trust {
	case "", string(ExtensionTrustBuiltin), "official", "verified":
		return true
	}
	return false
}
