package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Rollback returns a catalog install to the full snapshot its last update
// replaced: version, manifest, commit, digest, source and trust together.
//
// Every refusal happens before anything is written, and the write itself is
// guarded (RollbackCatalog), so a refused or raced rollback changes nothing.
// A legacy row that only has previous_version has no snapshot and is refused.
func (in *Installer) Rollback(ctx context.Context, actor *uuid.UUID, id string) (*domain.InstalledExtension, error) {
	row, err := in.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Source != domain.ExtensionSourceCatalog {
		return nil, apierror.NotFound("catalog extension")
	}
	snap, err := in.rollbackPlan(ctx, row)
	if err != nil {
		return nil, err
	}

	guard := domain.CatalogRollbackGuard{Version: row.Version, CommitSHA: row.CommitSHA, SnapshotDigest: snap.Digest}
	details, _ := json.Marshal(map[string]any{
		"from": row.Version, "to": snap.Version, "from_commit_sha": row.CommitSHA, "commit_sha": snap.CommitSHA,
		"from_artifact_sha256": row.ArtifactSHA256, "artifact_sha256": snap.ArtifactSHA256,
		"from_trust": row.TrustLevel, "trust": snap.TrustLevel, "snapshot_digest": snap.Digest, "source": "catalog",
		"from_catalog_scope": scopeName(row.CatalogScope), "catalog_scope": scopeName(snap.CatalogScope),
	})
	journal := &domain.InstanceExtensionJournalEntry{ActorID: actor, ExtensionID: id, Action: domain.InstanceJournalRolledBack, Details: details}
	if err := in.Store.RollbackCatalog(ctx, id, guard, *snap, journal); err != nil {
		return nil, err
	}
	if in.OnChange != nil {
		in.OnChange(ctx)
	}
	return in.Store.Get(ctx, id)
}

// rollbackPlan runs every check a rollback of row needs and returns the snapshot
// it would restore. It writes nothing; Rollback acts on it and Updates reports
// can_rollback from the same answer, so the flag cannot promise a rollback the
// endpoint then refuses.
func (in *Installer) rollbackPlan(ctx context.Context, row *domain.InstalledExtension) (*domain.CatalogSnapshot, error) {
	id := row.ExtensionID
	snap, err := in.Store.GetCatalogSnapshot(ctx, id)
	if err != nil {
		return nil, apiErr(http.StatusConflict, CodeSnapshotCorrupt, "the rollback snapshot is unreadable")
	}
	if snap == nil {
		return nil, apiErr(http.StatusConflict, CodeRollbackUnavailable, "nothing to roll back to")
	}
	if !snap.Intact() || snap.Source != string(domain.ExtensionSourceCatalog) {
		return nil, apiErr(http.StatusConflict, CodeSnapshotCorrupt, "the rollback snapshot failed its integrity check")
	}
	if snap.SupersededVersion != row.Version || snap.SupersededCommit != row.CommitSHA {
		return nil, apiErr(http.StatusConflict, CodeRollbackStale, "the extension changed since the snapshot was taken")
	}
	if row.Blocked {
		return nil, apiErr(http.StatusConflict, CodeBlocked, "blocked by the catalog: "+row.BlockedReason)
	}

	old, err := repoinstall.ValidateManifest(snap.Manifest)
	if err != nil {
		var ae *apierror.Error
		if errors.As(err, &ae) && ae.ErrorCode == repoinstall.CodeIncompatible {
			return nil, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeIncompatible, ae.Message)
		}
		return nil, apiErr(http.StatusConflict, CodeSnapshotCorrupt, "the snapshot manifest is invalid")
	}
	if old.ID != id || old.Version != snap.Version {
		return nil, apiErr(http.StatusConflict, CodeSnapshotCorrupt, fmt.Sprintf("snapshot manifest says %s@%s, not %s@%s", old.ID, old.Version, id, snap.Version))
	}
	if !repoinstall.MeshAPICompatible(old.MeshAPI, extensions.ExtensionsAPIVersion) {
		return nil, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeIncompatible, fmt.Sprintf("needs Mesh API %s", old.MeshAPI))
	}

	// Revocation is judged by the signed catalog; without a verified copy it
	// cannot be judged and the rollback is refused, never assumed safe.
	cat, err := in.Catalog.Current()
	if err != nil {
		return nil, apiErr(http.StatusServiceUnavailable, CodeSparkDown, "the catalog has not been verified yet")
	}
	if b, hit := cat.Blocks(id, snap.Version); hit {
		return nil, apiErr(http.StatusConflict, CodeBlocked, "blocked by the catalog: "+b.Reason)
	}
	if snap.CatalogScope == ScopeScoped {
		if err := in.exactReleaseUsable(ctx, id, snap); err != nil {
			return nil, err
		}
	} else if err := in.releaseUsable(ctx, cat, id, snap); err != nil {
		return nil, err
	}

	// Rollback never widens permissions: what the snapshot declares beyond the
	// current release would need the consent an update asks for, and a
	// rollback carries none.
	cur, _ := extensions.ParseManifest(row.Manifest)
	if added := repoinstall.NewCapabilities(repoinstall.Capabilities(cur), repoinstall.Capabilities(old)); len(added) > 0 {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeConsentRequired, fmt.Sprintf("rolling back would add permissions %v; install that release again with consent", added))
	}
	return snap, nil
}

// releaseUsable reports whether the snapshot's release may be installed again.
// The signed catalog only carries the latest release, so an older one is judged
// twice: where the catalog does list it, commit and digest must match and it
// must not be yanked; and Spark's release list (the one an install reads) must
// still show it installable and not yanked. Spark unreachable fails closed.
func (in *Installer) releaseUsable(ctx context.Context, cat *Catalog, id string, snap *domain.CatalogSnapshot) error {
	revoked := apiErr(http.StatusConflict, CodeReleaseRevoked, "the catalog no longer offers that release")
	if e, ok := cat.Entry(id); ok {
		for _, r := range append([]Release{e.Latest}, e.Releases...) {
			if r.Version != snap.Version {
				continue
			}
			if r.Yanked || r.ArtifactSHA256 == nil || r.CommitSHA != snap.CommitSHA || *r.ArtifactSHA256 != snap.ArtifactSHA256 {
				return revoked
			}
		}
	}
	d, err := in.Detail(ctx, id)
	if err != nil {
		return err
	}
	for _, r := range d.Releases {
		if r.Version == snap.Version {
			if !r.Installable || r.Yanked {
				return revoked
			}
			return nil
		}
	}
	return revoked
}
