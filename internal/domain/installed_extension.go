package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ExtensionSource says where an installed extension came from.
type ExtensionSource string

const (
	ExtensionSourceBuiltin ExtensionSource = "builtin"
	ExtensionSourceCatalog ExtensionSource = "catalog"
	ExtensionSourceRepo    ExtensionSource = "repo"
)

// ExtensionTrustLevel is the trust class of an installed extension.
type ExtensionTrustLevel string

const ExtensionTrustBuiltin ExtensionTrustLevel = "builtin"

// InstalledExtension is a row of installed_extensions (instance level).
type InstalledExtension struct {
	ExtensionID     string              `db:"extension_id"`
	Version         string              `db:"version"`
	Manifest        json.RawMessage     `db:"manifest"`
	Source          ExtensionSource     `db:"source"`
	RepoURL         string              `db:"repo_url"`
	RepoRef         string              `db:"repo_ref"`
	CommitSHA       string              `db:"commit_sha"`
	ArtifactSHA256  string              `db:"artifact_sha256"`
	TrustLevel      ExtensionTrustLevel `db:"trust_level"`
	InstalledBy     *uuid.UUID          `db:"installed_by"`
	PreviousVersion string              `db:"previous_version"`
	Blocked         bool                `db:"blocked"`
	BlockedReason   string              `db:"blocked_reason"`
	CreatedAt       time.Time           `db:"created_at"`
	UpdatedAt       time.Time           `db:"updated_at"`
}
