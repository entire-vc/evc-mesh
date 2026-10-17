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

const (
	ExtensionTrustBuiltin ExtensionTrustLevel = "builtin"
	// ExtensionTrustCustom marks an extension installed from a repository: it
	// never went through the catalog review.
	ExtensionTrustCustom ExtensionTrustLevel = "custom"
)

// ExtensionRepoState is the update/rollback bookkeeping of a repo-installed
// extension, kept apart from InstalledExtension so the catalog listing paths
// stay as they are.
type ExtensionRepoState struct {
	PreviousManifest  json.RawMessage `db:"previous_manifest"`
	PreviousCommitSHA string          `db:"previous_commit_sha"`
	PreviousRef       string          `db:"previous_ref"`
	PendingVersion    string          `db:"pending_version"`
	PendingRef        string          `db:"pending_ref"`
	PendingCommitSHA  string          `db:"pending_commit_sha"`
	PendingManifest   json.RawMessage `db:"pending_manifest"`
	// SkippedCommitSHA is the commit the admin rolled back from; auto-update
	// never applies it again (a manual update still can).
	SkippedCommitSHA string `db:"skipped_commit_sha"`
}

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
