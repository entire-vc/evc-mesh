package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

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
func (in *Installer) updateTarget(cat *Catalog, row *domain.InstalledExtension) *Entry {
	e, ok := cat.Entry(row.ExtensionID)
	if !ok || e.Runtime == "builtin" {
		return nil
	}
	if e.Latest.Yanked || e.Latest.ArtifactSHA256 == nil || *e.Latest.ArtifactSHA256 == "" {
		// No signed digest, nothing verifiable to install: never offered.
		return nil
	}
	if _, hit := cat.Blocks(e.ID, e.Latest.Version); hit {
		return nil
	}
	if !repoinstall.MeshAPICompatible(e.Latest.MeshAPI, extensions.ExtensionsAPIVersion) || !semverNewer(e.Latest.Version, row.Version) {
		return nil
	}
	return e
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
			NewCapabilities: []string{}, PreviousVersion: row.PreviousVersion,
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
	row, err := in.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Source != domain.ExtensionSourceCatalog {
		return nil, apierror.NotFound("catalog extension")
	}
	cat, err := in.Catalog.Current()
	if err != nil {
		return nil, apiErr(http.StatusServiceUnavailable, CodeSparkDown, "the catalog has not been verified yet")
	}
	res := &repoinstall.UpdateResult{ExtensionID: id, Version: row.Version, CommitSHA: row.CommitSHA, NewCapabilities: []string{}}
	e := in.updateTarget(cat, row)
	if e == nil {
		res.Outcome = repoinstall.OutcomeUpToDate
		return res, nil
	}
	res.Version, res.CommitSHA = e.Latest.Version, e.Latest.CommitSHA
	cur, _ := extensions.ParseManifest(row.Manifest)
	res.NewCapabilities = repoinstall.NewCapabilities(repoinstall.Capabilities(cur), e.Latest.Flatten())
	if len(res.NewCapabilities) > 0 && consentSHA != e.Latest.CommitSHA {
		res.Outcome = repoinstall.OutcomePendingConsent
		return res, nil
	}
	m, err := in.fetchVerified(ctx, e)
	if err != nil {
		return nil, err
	}
	upd := &domain.InstalledExtension{
		ExtensionID: id, Version: e.Latest.Version, Manifest: m.Raw, CommitSHA: e.Latest.CommitSHA,
		ArtifactSHA256: *e.Latest.ArtifactSHA256, TrustLevel: domain.ExtensionTrustLevel(e.Trust),
	}
	if err := in.Store.UpdateCatalog(ctx, upd); err != nil {
		return nil, err
	}
	if in.Journal != nil {
		details, _ := json.Marshal(map[string]any{"from": row.Version, "to": e.Latest.Version, "commit_sha": e.Latest.CommitSHA,
			"artifact_sha256": *e.Latest.ArtifactSHA256, "consented_capabilities": res.NewCapabilities, "source": "catalog"})
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
		r, uerr := in.Update(ctx, actor, rows[i].ExtensionID, "")
		if uerr != nil {
			out = append(out, repoinstall.UpdateResult{ExtensionID: rows[i].ExtensionID, NewCapabilities: []string{}, Error: fmt.Sprint(uerr)})
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}
