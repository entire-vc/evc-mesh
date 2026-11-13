package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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
	ExtensionID    string              `db:"extension_id"`
	Version        string              `db:"version"`
	Manifest       json.RawMessage     `db:"manifest"`
	Source         ExtensionSource     `db:"source"`
	RepoURL        string              `db:"repo_url"`
	RepoRef        string              `db:"repo_ref"`
	CommitSHA      string              `db:"commit_sha"`
	ArtifactSHA256 string              `db:"artifact_sha256"`
	TrustLevel     ExtensionTrustLevel `db:"trust_level"`
	// CatalogScope is "scoped" for a catalog install taken from an exact
	// receipt: it is pinned to that version (autoupdate and the default
	// catalogue's latest never move it). "default" otherwise.
	CatalogScope    string     `db:"catalog_scope"`
	InstalledBy     *uuid.UUID `db:"installed_by"`
	PreviousVersion string     `db:"previous_version"`
	Blocked         bool       `db:"blocked"`
	BlockedReason   string     `db:"blocked_reason"`
	CreatedAt       time.Time  `db:"created_at"`
	UpdatedAt       time.Time  `db:"updated_at"`
}

// CatalogSnapshot is everything an update of a source=catalog row replaced,
// kept so a rollback restores the whole release and not just its version. The
// Superseded* fields name the release the snapshot was taken against: a row
// that has moved on since is stale and must not be rolled back from it.
type CatalogSnapshot struct {
	Version        string          `json:"version"`
	Manifest       json.RawMessage `json:"manifest"`
	CommitSHA      string          `json:"commit_sha"`
	ArtifactSHA256 string          `json:"artifact_sha256"`
	TrustLevel     string          `json:"trust_level"`
	Source         string          `json:"source"`
	// CatalogScope is the scope the snapshot's release was installed with;
	// "" (legacy snapshot) means default.
	CatalogScope      string    `json:"catalog_scope,omitempty"`
	SupersededVersion string    `json:"superseded_version"`
	SupersededCommit  string    `json:"superseded_commit"`
	CapturedAt        time.Time `json:"captured_at"`
	Digest            string    `json:"digest"`
}

// canonicalJSON re-encodes raw with sorted keys and no insignificant spacing,
// so the digest survives the JSONB round trip that reorders object keys.
func canonicalJSON(raw []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// ComputeDigest is the sha256 over every field but Digest and CapturedAt.
func (s CatalogSnapshot) ComputeDigest() string {
	h := sha256.New()
	for _, f := range []string{s.Version, string(canonicalJSON(s.Manifest)), s.CommitSHA, s.ArtifactSHA256, s.TrustLevel, s.Source, s.SupersededVersion, s.SupersededCommit} {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	// Only a non-default scope enters the digest, so the snapshots sealed
	// before the scope existed stay intact.
	if s.CatalogScope != "" && s.CatalogScope != "default" {
		h.Write([]byte("catalog_scope=" + s.CatalogScope))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Seal fills Digest.
func (s *CatalogSnapshot) Seal() { s.Digest = s.ComputeDigest() }

// Intact reports whether the stored digest matches the content.
func (s CatalogSnapshot) Intact() bool { return s.Digest != "" && s.Digest == s.ComputeDigest() }

// CatalogRollbackGuard is what RollbackCatalog requires the row to still be:
// the release being left and the snapshot being restored.
type CatalogRollbackGuard struct {
	Version        string
	CommitSHA      string
	SnapshotDigest string
}

// CatalogUpdateGuard is what UpdateCatalog requires the row to still be when it
// writes: the release the updater read, and, for the unattended pass, not the
// commit an admin has since rolled back from.
type CatalogUpdateGuard struct {
	FromVersion   string
	FromCommitSHA string
	// Auto marks the bulk pass: the row's skipped commit is re-read under the
	// row lock and the update refused when it equals the target.
	Auto bool
}

// ExtensionStateGuard is what a move or a re-pin requires the installed row to
// still be when it writes: the source, version and commit the admin's plan
// was made against. Anything else is a concurrent change and nothing is written.
type ExtensionStateGuard struct {
	Source    ExtensionSource
	Version   string
	CommitSHA string
}

// RepoTokenChange says what a re-pin does with the stored repository token.
type RepoTokenChange string

const (
	RepoTokenKeep    RepoTokenChange = "keep"
	RepoTokenReplace RepoTokenChange = "replace"
	RepoTokenRemove  RepoTokenChange = "remove"
)
