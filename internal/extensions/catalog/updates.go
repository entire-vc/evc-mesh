package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"regexp"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/repoinstall"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func semverNewer(candidate, current string) bool {
	return repoinstall.IsNewerTag("v"+candidate, "v"+current)
}

// catalogRows are the installed rows that came from the catalog.
func (in *Installer) catalogRows(ctx context.Context) ([]domain.InstalledExtension, error) {
	rows, err := in.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.InstalledExtension
	for i := range rows {
		if rows[i].Source == domain.ExtensionSourceCatalog {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// updateTarget is the catalog release a row could move to, or nil. A blocked,
// incompatible, yanked-away (no longer listed) or not newer release is never a
// target: yanked versions only vanish from the Updates tab, nothing is switched off.
// A scoped (pinned) install has no target: the default catalogue's latest is
// never offered to it, even when its id is listed there.
func (in *Installer) updateTarget(cat *Catalog, row *domain.InstalledExtension) *Entry {
	if IsScoped(row) {
		return nil
	}
	e, ok := cat.Entry(row.ExtensionID)
	if !ok || !catalogUpdateUsable(cat, row, e) {
		return nil
	}
	return e
}

func catalogUpdateUsable(cat *Catalog, row *domain.InstalledExtension, e *Entry) bool {
	if e.Runtime == "builtin" {
		return false
	}
	if e.Latest.Yanked || e.Latest.ArtifactSHA256 == nil || *e.Latest.ArtifactSHA256 == "" {
		// No signed digest, nothing verifiable to install: never offered.
		return false
	}
	if _, hit := cat.Blocks(e.ID, e.Latest.Version); hit {
		return false
	}
	if !repoinstall.MeshAPICompatible(e.Latest.MeshAPI, extensions.ExtensionsAPIVersion) || !semverNewer(e.Latest.Version, row.Version) {
		return false
	}
	return true
}

// Updates is the catalog half of the Updates tab: one row per catalog install.
func (in *Installer) Updates(ctx context.Context) ([]repoinstall.UpdateInfo, error) {
	rows, err := in.catalogRows(ctx)
	if err != nil {
		return nil, err
	}
	cat, cerr := in.Catalog.Current()
	out := make([]repoinstall.UpdateInfo, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		cur, _ := extensions.ParseManifest(row.Manifest)
		info := repoinstall.UpdateInfo{
			ExtensionID: row.ExtensionID, Name: cur.Name, CurrentVersion: row.Version, CurrentCommit: row.CommitSHA,
			NewCapabilities: []string{}, PreviousVersion: row.PreviousVersion, CatalogScope: row.CatalogScope,
		}
		// can_rollback is the answer of the same checks Rollback runs, so it is
		// true only when the endpoint would proceed (a legacy previous_version
		// has no snapshot and never qualifies).
		if _, perr := in.rollbackPlan(ctx, row); perr == nil {
			info.CanRollback = true
		}
		if cerr != nil {
			info.Error = "catalog is not available"
			out = append(out, info)
			continue
		}
		if e := in.updateTarget(cat, row); e != nil {
			info.UpdateAvailable = true
			info.LatestVersion, info.LatestCommit = e.Latest.Version, e.Latest.CommitSHA
			if added := repoinstall.NewCapabilities(repoinstall.Capabilities(cur), e.Latest.Flatten()); len(added) > 0 {
				info.NewCapabilities, info.NeedsConsent = added, true
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// Update moves one catalog install to the newest release. Added permissions
// need consent: consentSHA must be the commit the admin saw in the Updates tab.
func (in *Installer) Update(ctx context.Context, actor *uuid.UUID, id, consentSHA string) (*repoinstall.UpdateResult, error) {
	return in.update(ctx, actor, id, consentSHA, false, updateSelection{})
}

// updateSelection is an admin's explicit choice of a release; zero means the
// newest default-catalogue release.
type updateSelection struct {
	version, commit string
	// scope is ScopeScoped for a release read from its exact receipt.
	scope string
}

var targetVersionPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)
var targetCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// UpdateTarget pins a manual catalog update to a release in the verified copy.
// It shares the consent, artifact verification and guarded write used by Update.
func (in *Installer) UpdateTarget(ctx context.Context, actor *uuid.UUID, id, consentSHA, version, expectedCommitSHA string) (*repoinstall.UpdateResult, error) {
	if !targetVersionPattern.MatchString(version) || !targetCommitPattern.MatchString(expectedCommitSHA) {
		return nil, apierror.BadRequest("target_version and expected_target_commit_sha must be a version and full commit SHA")
	}
	return in.update(ctx, actor, id, consentSHA, false, updateSelection{version: version, commit: expectedCommitSHA, scope: ScopeDefault})
}

// selectedUpdateTarget never changes the cached entry. Every record naming the
// selected version must describe the same release, even when the pin matches one.
func selectedUpdateTarget(cat *Catalog, row *domain.InstalledExtension, version, expectedCommitSHA string) (*Entry, error) {
	e, ok := cat.Entry(row.ExtensionID)
	if !ok {
		return nil, apiErr(http.StatusConflict, CodeReleaseRevoked, "extension is not in the verified catalog")
	}
	selected, err := releaseByVersion(e, version)
	if err != nil {
		return nil, err
	}
	if selected.CommitSHA != expectedCommitSHA {
		return nil, apierror.Conflict("expected_target_commit_sha does not match the verified release")
	}
	entryCopy := *e
	entryCopy.Latest = *selected
	if row.Blocked || !catalogUpdateUsable(cat, row, &entryCopy) {
		return nil, apierror.Conflict("the selected catalog release is not an eligible upgrade")
	}
	return &entryCopy, nil
}

// releaseByVersion is the release of e named version in the verified copy.
// Every record naming it must describe the same release.
func releaseByVersion(e *Entry, version string) (*Release, error) {
	var selected *Release
	var selectedCaps any
	for _, r := range append([]Release{e.Latest}, e.Releases...) {
		if r.Version != version {
			continue
		}
		caps, err := checkedCapabilities(r)
		if err != nil {
			return nil, err
		}
		if selected != nil && (selected.MeshAPI != r.MeshAPI || selected.CommitSHA != r.CommitSHA ||
			!reflect.DeepEqual(selected.ArtifactSHA256, r.ArtifactSHA256) || selected.Yanked != r.Yanked || !reflect.DeepEqual(selectedCaps, caps)) {
			return nil, apierror.Conflict("the verified catalog has conflicting records for target_version")
		}
		release := r
		selected, selectedCaps = &release, caps
	}
	if selected == nil {
		return nil, apiErr(http.StatusConflict, CodeReleaseRevoked, "target_version is not in the verified catalog")
	}
	return selected, nil
}

// checkedCapabilities distinguishes a valid empty declaration from Flatten's
// nil-on-malformed result. Unknown groups (including network) remain supported.
func checkedCapabilities(r Release) (map[string]any, error) {
	invalid := apiErr(http.StatusUnprocessableEntity, repoinstall.CodeManifestInvalid, "the selected catalog release has malformed capabilities")
	var groups map[string]json.RawMessage
	if json.Unmarshal(r.Capabilities, &groups) != nil || groups == nil {
		return nil, invalid
	}
	for _, group := range []string{"api", "events", "webhooks_in", "ui", "secrets"} {
		raw, ok := groups[group]
		if !ok {
			continue
		}
		var vals []json.RawMessage
		if json.Unmarshal(raw, &vals) != nil {
			return nil, invalid
		}
		for _, val := range vals {
			var s string
			if json.Unmarshal(val, &s) != nil || s == "" {
				return nil, invalid
			}
		}
	}
	// Decode once more for semantic duplicate comparison: JSON object key order
	// and whitespace do not make two otherwise identical releases conflict.
	var canonical map[string]any
	if json.Unmarshal(r.Capabilities, &canonical) != nil {
		return nil, invalid
	}
	return canonical, nil
}

// update is Update; auto marks the bulk pass, which leaves alone the commit an
// admin rolled back from (a manual update still takes it).
//
// A scoped install is pinned: without a selection it is always up to date. An
// explicit selection decides its next scope: an exact receipt keeps it pinned
// (at the new version), a default-catalogue version unpins it.
func (in *Installer) update(ctx context.Context, actor *uuid.UUID, id, consentSHA string, auto bool, sel updateSelection) (*repoinstall.UpdateResult, error) {
	row, err := in.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Source != domain.ExtensionSourceCatalog {
		return nil, apierror.NotFound("catalog extension")
	}
	res := &repoinstall.UpdateResult{ExtensionID: id, Version: row.Version, CommitSHA: row.CommitSHA, NewCapabilities: []string{}}
	var e *Entry
	nextScope := row.CatalogScope
	if sel.scope == ScopeScoped {
		if e, err = in.exactUpdateTarget(ctx, row, sel.version, sel.commit); err != nil {
			return nil, err
		}
		nextScope = ScopeScoped
	} else {
		cat, cerr := in.Catalog.Current()
		if cerr != nil {
			return nil, apiErr(http.StatusServiceUnavailable, CodeSparkDown, "the catalog has not been verified yet")
		}
		e = in.updateTarget(cat, row)
		if sel.version != "" {
			if e, err = selectedUpdateTarget(cat, row, sel.version, sel.commit); err != nil {
				return nil, err
			}
			nextScope = ScopeDefault
		}
	}
	if e == nil {
		res.Outcome = repoinstall.OutcomeUpToDate
		return res, nil
	}
	// The Runner is installed separately from the catalog row; moving the row
	// moves the desired controller version out from under a pinned runtime.
	// It only changes on an explicit update.
	if auto && id == domain.RunnerExtensionID {
		res.Outcome = repoinstall.OutcomeUpToDate
		return res, nil
	}
	if auto {
		if skipped, serr := in.Store.SkippedCommit(ctx, id); serr != nil {
			return nil, serr
		} else if skipped != "" && skipped == e.Latest.CommitSHA {
			res.Outcome = repoinstall.OutcomeUpToDate
			return res, nil
		}
	}
	res.Version, res.CommitSHA = e.Latest.Version, e.Latest.CommitSHA
	cur, _ := extensions.ParseManifest(row.Manifest)
	res.NewCapabilities = repoinstall.NewCapabilities(repoinstall.Capabilities(cur), e.Latest.Flatten())
	if len(res.NewCapabilities) > 0 && consentSHA != e.Latest.CommitSHA {
		res.Outcome = repoinstall.OutcomePendingConsent
		return res, nil
	}
	m, err := in.fetchVerified(ctx, e, sel.scope == ScopeScoped)
	if err != nil {
		return nil, err
	}
	upd := &domain.InstalledExtension{
		ExtensionID: id, Version: e.Latest.Version, Manifest: m.Raw, CommitSHA: e.Latest.CommitSHA,
		ArtifactSHA256: *e.Latest.ArtifactSHA256, TrustLevel: domain.ExtensionTrustLevel(e.Trust), CatalogScope: nextScope,
	}
	if err := in.Store.UpdateCatalog(ctx, upd, domain.CatalogUpdateGuard{FromVersion: row.Version, FromCommitSHA: row.CommitSHA, Auto: auto}); err != nil {
		return nil, err
	}
	// A pinned install switched off for its old version comes back on at once:
	// the exact receipt just read says the new version is offered and not
	// blocked. The next blocklist pass judges it again.
	if row.Blocked && sel.scope == ScopeScoped {
		if bs, ok := in.Store.(interface {
			SetBlocked(ctx context.Context, extensionID string, blocked bool, reason string) (bool, error)
		}); ok {
			if _, berr := bs.SetBlocked(ctx, id, false, ""); berr != nil {
				log.Printf("[extensions] unblock %s after exact update: %v", id, berr)
			}
		}
	}
	if in.Journal != nil {
		details, _ := json.Marshal(map[string]any{"from": row.Version, "to": e.Latest.Version, "commit_sha": e.Latest.CommitSHA,
			"artifact_sha256": *e.Latest.ArtifactSHA256, "consented_capabilities": res.NewCapabilities, "source": "catalog",
			"from_catalog_scope": scopeName(row.CatalogScope), "catalog_scope": scopeName(nextScope)})
		if jerr := in.Journal.AppendJournal(ctx, &domain.InstanceExtensionJournalEntry{ActorID: actor, ExtensionID: id, Action: domain.InstanceJournalUpdated, Details: details}); jerr != nil {
			log.Printf("[extensions] journal updated %s: %v", id, jerr)
		}
	}
	if in.OnChange != nil {
		in.OnChange(ctx)
	}
	res.Outcome = repoinstall.OutcomeUpdated
	return res, nil
}

// UpdateAll updates every catalog install that has an update needing no consent.
func (in *Installer) UpdateAll(ctx context.Context, actor *uuid.UUID) ([]repoinstall.UpdateResult, error) {
	rows, err := in.catalogRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]repoinstall.UpdateResult, 0, len(rows))
	for i := range rows {
		r, uerr := in.update(ctx, actor, rows[i].ExtensionID, "", true, updateSelection{})
		if uerr != nil {
			out = append(out, repoinstall.UpdateResult{ExtensionID: rows[i].ExtensionID, NewCapabilities: []string{}, Error: fmt.Sprint(uerr)})
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// scopeName is the scope as reported: "scoped" or "default".
func scopeName(s string) string {
	if s == ScopeScoped {
		return ScopeScoped
	}
	return ScopeDefault
}
