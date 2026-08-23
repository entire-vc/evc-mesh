package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ProjectIntegration represents a project-level integration configuration.
type ProjectIntegration struct {
	ID        uuid.UUID       `json:"id" db:"id"`
	ProjectID uuid.UUID       `json:"project_id" db:"project_id"`
	Type      string          `json:"type" db:"type"`
	Enabled   bool            `json:"enabled" db:"enabled"`
	Settings  json.RawMessage `json:"settings" db:"settings"`
	// AgentKey is plaintext in memory and encrypted at rest, but ONLY when it
	// travels through the repository layer: the encryption lives in
	// ProjectIntegrationRepo, so a row provisioned with direct SQL bypasses it
	// entirely. That is how the 2026-06 Team Relay credentials came to sit in
	// the clear while this comment claimed otherwise. The DB-side CHECK
	// constraint (migration 20260820108) is what makes the claim true for
	// every write path rather than just this one.
	AgentKey  string     `json:"agent_key,omitempty" db:"agent_key"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt time.Time  `json:"updated_at" db:"updated_at"`
	CreatedBy *uuid.UUID `json:"created_by,omitempty" db:"created_by"`

	// KeyExpiresAt is nil when the credential's expiry is unknown. KeyExpirySource
	// is load-bearing, not decorative: it is "manual" (a human typed the date into
	// settings — an unverified claim that can silently drift from reality if the
	// credential is reissued) or "source" (fetched from the integration's own
	// introspection endpoint — a fact, not yet wired up for any integration type).
	// A UI showing a date without this label would be showing an indicator that
	// cannot be trusted, per the decision recorded on #218d5847.
	KeyExpiresAt    *time.Time `json:"key_expires_at,omitempty" db:"key_expires_at"`
	KeyExpirySource *string    `json:"key_expiry_source,omitempty" db:"key_expiry_source"`

	// LastSyncCheckedAt/LastSyncStatus/LastSyncError record the outcome of the
	// most recent attempt to reach the integration's source. LastSyncStatus
	// distinguishes "key_expired" from a generic "error" so a stale credential
	// shows up as exactly that in the UI, not as an unexplained empty tree
	// (the failure mode named in spec doc 55cf5d7e §3.9).
	LastSyncCheckedAt *time.Time `json:"last_sync_checked_at,omitempty" db:"last_sync_checked_at"`
	LastSyncStatus    *string    `json:"last_sync_status,omitempty" db:"last_sync_status"`
	LastSyncError     *string    `json:"last_sync_error,omitempty" db:"last_sync_error"`
}

// TeamRelaySettings holds the Team Relay-specific configuration.
type TeamRelaySettings struct {
	// ShareID is the relay share UUID (shares.id = folder GUID). Preferred over ShareSlug
	// because it works for private sync folders that have web_published=false.
	// When non-empty, transport() sends this UUID to the relay upload endpoint.
	ShareID            string `json:"share_id,omitempty"`
	ShareSlug          string `json:"share_slug"`
	Subfolder          string `json:"subfolder"`
	IncludeProjectSlug bool   `json:"include_project_slug"`

	// DocsMountPath is where the share's subtree is grafted into this project's
	// Docs tree, as a slug path ("Team Relay" or "External/Notes") — the SAME
	// syntax GetByPathInProject already walks. Empty means the subtree IS the
	// project's top level (Pavel's second mount mode): there is deliberately no
	// separate flag for that case, because "no path segments to walk" and "walk
	// these segments" are the same operation with a different (possibly empty)
	// segment list, not two behaviours. See
	// service.teamRelayMountService.resolveOrCreateFolderPath — that function is
	// what both modes go through, and it is the one place a second branch would
	// have crept in if this had been a bool instead.
	DocsMountPath string `json:"docs_mount_path,omitempty"`

	// SyncTTLSeconds is how long a mounted copy is served without checking its
	// source on open. Zero (the unset zero value, indistinguishable here from
	// "explicitly zero" — nothing today needs to ask for a zero-second TTL) means
	// "use DefaultTeamRelaySyncTTL". Settings UI to change this per-project is
	// R5's job; this field exists now so R3 has one source of truth for the
	// value to read rather than inventing a second place to store it ahead of
	// that UI landing.
	SyncTTLSeconds int `json:"sync_ttl_seconds,omitempty"`
}

// RelayFileItem represents a file entry returned by the Team Relay share file-list API.
type RelayFileItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// UpsertProjectIntegrationInput holds data for creating/updating a project integration.
type UpsertProjectIntegrationInput struct {
	ProjectID          uuid.UUID  `json:"project_id"`
	Enabled            bool       `json:"enabled"`
	ShareID            string     `json:"share_id,omitempty"`
	ShareSlug          string     `json:"share_slug"`
	AgentKey           string     `json:"agent_key,omitempty"` // omit to keep existing
	Subfolder          string     `json:"subfolder"`
	IncludeProjectSlug bool       `json:"include_project_slug"`
	CreatedBy          *uuid.UUID `json:"-"`
}
