package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func isCode(err error, code string) bool {
	var ae *apierror.Error
	return errors.As(err, &ae) && ae.ErrorCode == code
}

// CodeReceiptInvalid: Spark answered an exact receipt that does not verify or
// is not the selection asked for. Nothing is installed from it.
const CodeReceiptInvalid = "extension_catalog_receipt_invalid"

// IsScoped reports whether row is pinned to an exact receipt.
func IsScoped(row *domain.InstalledExtension) bool {
	return row != nil && row.CatalogScope == ScopeScoped
}

// exactTarget reads and verifies the exact receipt of id@version and runs the
// checks every catalog install runs on its release: blocklist (the receipt's
// and, when there is one, the cached default catalogue's), runtime, signed
// digest and Mesh API compatibility. The returned entry's Latest is the
// selected release. It writes nothing.
func (in *Installer) exactTarget(ctx context.Context, id, version string) (*Entry, error) {
	if !targetVersionPattern.MatchString(version) {
		return nil, apiErr(http.StatusBadRequest, CodeReceiptInvalid, "version must be an exact x.y.z version")
	}
	rc, err := in.Catalog.FetchExact(ctx, extensions.ExtensionsAPIVersion, id, version)
	switch {
	case errors.Is(err, ErrSelectionUnavailable):
		return nil, apiErr(http.StatusNotFound, CodeNotInCatalog,
			fmt.Sprintf("Spark does not offer %s@%s (missing, incompatible, yanked, blocked or suspended)", id, version))
	case errors.Is(err, ErrBadSelection), errors.Is(err, ErrBadSignature), errors.Is(err, ErrNoSignature), errors.Is(err, ErrBadSchema):
		return nil, apiErr(http.StatusBadGateway, CodeReceiptInvalid, "the catalog receipt does not verify: "+err.Error())
	case err != nil:
		return nil, apiErr(http.StatusBadGateway, CodeSparkDown, "the catalog receipt is unavailable: "+err.Error())
	}
	e := *rc.Entry
	rel := e.Latest
	if b, hit := rc.Catalog.Blocks(id, rel.Version); hit {
		return nil, apiErr(http.StatusConflict, CodeBlocked, "blocked by the catalog: "+b.Reason)
	}
	if cat, cerr := in.Catalog.Current(); cerr == nil {
		if b, hit := cat.Blocks(id, rel.Version); hit {
			return nil, apiErr(http.StatusConflict, CodeBlocked, "blocked by the catalog: "+b.Reason)
		}
	}
	if e.Runtime == "builtin" {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeBuiltinRuntime, "this extension ships inside Mesh; nothing to install")
	}
	if rel.ArtifactSHA256 == nil || *rel.ArtifactSHA256 == "" {
		return nil, apiErr(http.StatusUnprocessableEntity, CodeDigest, "the catalog gives no artifact digest for this version")
	}
	if !repoinstall.MeshAPICompatible(rel.MeshAPI, extensions.ExtensionsAPIVersion) {
		return nil, apiErr(http.StatusUnprocessableEntity, repoinstall.CodeIncompatible, fmt.Sprintf("needs Mesh API %s", rel.MeshAPI))
	}
	return &e, nil
}

// InstallExact installs id at exactly version from Spark's exact receipt. The
// id does not have to be in the default catalogue (a scoped release never is).
// The install is pinned: autoupdate and the default catalogue's latest never
// move it; only an admin choosing a version does.
func (in *Installer) InstallExact(ctx context.Context, actor uuid.UUID, id, version string) (*domain.InstalledExtension, error) {
	if existing, gerr := in.Store.Get(ctx, id); gerr != nil {
		return nil, gerr
	} else if existing != nil {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeIDConflict, fmt.Sprintf("an extension with id %q is already installed", id))
	}
	e, err := in.exactTarget(ctx, id, version)
	if err != nil {
		return nil, err
	}
	m, err := in.fetchVerified(ctx, e, true)
	if err != nil {
		return nil, err
	}
	rel := e.Latest
	row := &domain.InstalledExtension{
		ExtensionID: id, Version: rel.Version, Manifest: m.Raw, Source: domain.ExtensionSourceCatalog, CatalogScope: ScopeScoped,
		CommitSHA: rel.CommitSHA, ArtifactSHA256: *rel.ArtifactSHA256, TrustLevel: domain.ExtensionTrustLevel(e.Trust), InstalledBy: &actor,
	}
	if err := in.Store.InsertCatalog(ctx, row); err != nil {
		return nil, err
	}
	if in.Journal != nil {
		details, _ := json.Marshal(map[string]any{"version": rel.Version, "commit_sha": rel.CommitSHA, "artifact_sha256": *rel.ArtifactSHA256,
			"trust": e.Trust, "source": "catalog", "catalog_scope": ScopeScoped})
		if jerr := in.Journal.AppendJournal(ctx, &domain.InstanceExtensionJournalEntry{ActorID: &actor, ExtensionID: id, Action: domain.InstanceJournalInstalled, Details: details}); jerr != nil {
			log.Printf("[extensions] journal installed %s: %v", id, jerr)
		}
	}
	if in.OnChange != nil {
		in.OnChange(ctx)
	}
	go in.countInstall(id)
	return in.Store.Get(ctx, id)
}

// UpdateExact moves a catalog install to exactly version from Spark's exact
// receipt; the install is (or becomes) pinned there. The release must be newer
// than the installed one (rollback returns to the previous one) and its commit
// must be expectedCommitSHA. Consent, artifact verification and the guarded
// write are those of every catalog update.
func (in *Installer) UpdateExact(ctx context.Context, actor *uuid.UUID, id, consentSHA, version, expectedCommitSHA string) (*repoinstall.UpdateResult, error) {
	if !targetVersionPattern.MatchString(version) || !targetCommitPattern.MatchString(expectedCommitSHA) {
		return nil, apiErr(http.StatusBadRequest, CodeReceiptInvalid, "target_version and expected_target_commit_sha must be a version and full commit SHA")
	}
	return in.update(ctx, actor, id, consentSHA, false, updateSelection{version: version, commit: expectedCommitSHA, scope: ScopeScoped})
}

// exactUpdateTarget is selectedUpdateTarget for an exact receipt.
func (in *Installer) exactUpdateTarget(ctx context.Context, row *domain.InstalledExtension, version, expectedCommitSHA string) (*Entry, error) {
	e, err := in.exactTarget(ctx, row.ExtensionID, version)
	if err != nil {
		return nil, err
	}
	if e.Latest.CommitSHA != expectedCommitSHA {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeStateChanged, "expected_target_commit_sha does not match the receipt")
	}
	if !semverNewer(e.Latest.Version, row.Version) {
		return nil, apiErr(http.StatusConflict, repoinstall.CodeStateChanged,
			fmt.Sprintf("%s is not newer than the installed %s; rollback returns to the previous release", e.Latest.Version, row.Version))
	}
	return e, nil
}

// exactReleaseUsable is releaseUsable for a snapshot taken from an exact
// receipt: Spark's release list hides scoped releases, so the exact receipt of
// the snapshot's version decides, and its commit and digest must match.
func (in *Installer) exactReleaseUsable(ctx context.Context, id string, snap *domain.CatalogSnapshot) error {
	e, err := in.exactTarget(ctx, id, snap.Version)
	if err != nil {
		if isCode(err, CodeNotInCatalog) {
			return apiErr(http.StatusConflict, CodeReleaseRevoked, "the catalog no longer offers that release")
		}
		return err
	}
	if e.Latest.CommitSHA != snap.CommitSHA || *e.Latest.ArtifactSHA256 != snap.ArtifactSHA256 {
		return apiErr(http.StatusConflict, CodeReleaseRevoked, "the catalog no longer offers that release")
	}
	return nil
}
