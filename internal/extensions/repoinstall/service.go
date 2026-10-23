package repoinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Service is the install, update and rollback logic for repo extensions.
type Service struct {
	Client    *Client
	Repo      repository.RepoExtensionRepository
	Installed repository.InstalledExtensionRepository
	Policy    repository.InstanceExtensionPolicyRepository
	// OnChange runs after an install, update or rollback changed what is
	// installed, so the runtime can register the new version. May be nil.
	OnChange func(ctx context.Context)
	// Themes verifies and stores the bundle files of a kind=theme extension
	// before its row is written. Without it a theme is refused: its hashes
	// would otherwise never be checked.
	Themes ThemeAssets
}

// FetchFunc reads one file of the repository at the commit being installed.
type FetchFunc func(ctx context.Context, path string) ([]byte, error)

// ThemeAssets checks a theme's bundle against the hashes in its manifest and
// stores it; a returned error stops the install.
type ThemeAssets interface {
	PrepareInstall(ctx context.Context, m extensions.Manifest, fetch FetchFunc) error
}

// prepareTheme runs the theme asset check for a fetched manifest; a no-op for
// every other kind.
func (s *Service) prepareTheme(ctx context.Context, f *fetched, token string) error {
	if f.manifest.Kind != "theme" {
		return nil
	}
	if s.Themes == nil {
		return apiErr(http.StatusServiceUnavailable, CodeManifestInvalid, "themes are not enabled on this instance")
	}
	fetch := func(ctx context.Context, path string) ([]byte, error) {
		b, err := s.Client.File(ctx, f.src, f.sha, path, token, MaxThemeAssetBytes)
		if errors.Is(err, ErrBadPath) {
			return nil, apiErr(http.StatusUnprocessableEntity, CodeManifestInvalid, err.Error())
		}
		return b, wrapHost(err)
	}
	return s.Themes.PrepareInstall(ctx, f.manifest, fetch)
}

// MaxThemeAssetBytes bounds one file of a theme bundle read from a repository.
const MaxThemeAssetBytes = 2 << 20

func (s *Service) changed(ctx context.Context) {
	if s.OnChange != nil {
		s.OnChange(ctx)
	}
}

// Preview is what "read the manifest" shows before the install.
type Preview struct {
	Manifest     json.RawMessage `json:"manifest"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Publisher    string          `json:"publisher"`
	Description  string          `json:"description"`
	MeshAPI      string          `json:"mesh_api"`
	Compatible   bool            `json:"compatible"`
	Capabilities []string        `json:"capabilities"`
	Ref          string          `json:"ref"`
	CommitSHA    string          `json:"commit_sha"`
	TrustLevel   string          `json:"trust_level"`
	Warning      string          `json:"warning"`
}

// Refs is the tag and branch list the install form offers.
type Refs struct {
	Tags     []Ref `json:"tags"`
	Branches []Ref `json:"branches"`
}

const customWarning = "Custom extension: it was not reviewed by the catalog. Install only code you trust."

func wrapHost(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrBadURL):
		return apiErr(http.StatusBadRequest, CodeManifestInvalid, err.Error())
	case errors.Is(err, ErrNotFound):
		return apiErr(http.StatusNotFound, CodeRepoUnavailable, err.Error())
	case errors.Is(err, ErrUnauthorized):
		return apiErr(http.StatusForbidden, CodeRepoUnavailable, err.Error())
	}
	return apiErr(http.StatusBadGateway, CodeRepoUnavailable, err.Error())
}

// ListRefs returns the tags and branches of a repository.
func (s *Service) ListRefs(ctx context.Context, repoURL, token string) (*Refs, error) {
	src, err := s.Client.ParseURL(repoURL)
	if err != nil {
		return nil, wrapHost(err)
	}
	tags, err := s.Client.Tags(ctx, src, token)
	if err != nil {
		return nil, wrapHost(err)
	}
	branches, err := s.Client.Branches(ctx, src, token)
	if err != nil {
		return nil, wrapHost(err)
	}
	return &Refs{Tags: tags, Branches: branches}, nil
}

type fetched struct {
	src      Source
	manifest extensions.Manifest
	raw      []byte
	sha      string
}

func (s *Service) fetch(ctx context.Context, repoURL, ref, token string) (*fetched, error) {
	if ref == "" {
		return nil, apierror.BadRequest("ref (a tag or a branch) is required")
	}
	src, err := s.Client.ParseURL(repoURL)
	if err != nil {
		return nil, wrapHost(err)
	}
	return s.fetchSrc(ctx, src, ref, token)
}

func (s *Service) fetchSrc(ctx context.Context, src Source, ref, token string) (*fetched, error) {
	sha, err := s.Client.Commit(ctx, src, ref, token)
	if err != nil {
		return nil, wrapHost(err)
	}
	// Read the manifest at the resolved commit so the stored sha and the stored
	// manifest cannot disagree if the ref moves in between.
	raw, err := s.Client.Manifest(ctx, src, sha, token)
	if err != nil {
		return nil, wrapHost(err)
	}
	m, err := ValidateManifest(raw)
	if err != nil {
		return nil, err
	}
	return &fetched{src: src, manifest: m, raw: raw, sha: sha}, nil
}

// Preview reads and validates the manifest without installing anything.
func (s *Service) Preview(ctx context.Context, repoURL, ref, token string) (*Preview, error) {
	f, err := s.fetch(ctx, repoURL, ref, token)
	if err != nil {
		return nil, err
	}
	if existing, gerr := s.Installed.Get(ctx, f.manifest.ID); gerr != nil {
		return nil, gerr
	} else if existing != nil {
		return nil, apiErr(http.StatusConflict, CodeIDConflict, fmt.Sprintf("an extension with id %q is already installed", f.manifest.ID))
	}
	m := f.manifest
	return &Preview{
		Manifest: m.Raw, ID: m.ID, Name: m.Name, Version: m.Version, Publisher: m.Publisher, Description: m.Description,
		MeshAPI: m.MeshAPI, Compatible: true, Capabilities: Capabilities(m), Ref: ref, CommitSHA: f.sha,
		TrustLevel: string(domain.ExtensionTrustCustom), Warning: customWarning,
	}, nil
}

// Install installs the extension at ref with trust level custom.
func (s *Service) Install(ctx context.Context, actor uuid.UUID, repoURL, ref, token string) (*domain.InstalledExtension, error) {
	return s.install(ctx, actor, repoURL, ref, token, "")
}

// InstallTheme is Install for a repository that must hold a kind=theme manifest.
func (s *Service) InstallTheme(ctx context.Context, actor uuid.UUID, repoURL, ref, token string) (*domain.InstalledExtension, error) {
	return s.install(ctx, actor, repoURL, ref, token, "theme")
}

func (s *Service) install(ctx context.Context, actor uuid.UUID, repoURL, ref, token, wantKind string) (*domain.InstalledExtension, error) {
	f, err := s.fetch(ctx, repoURL, ref, token)
	if err != nil {
		return nil, err
	}
	m := f.manifest
	if wantKind != "" && m.Kind != wantKind {
		return nil, apiErr(http.StatusUnprocessableEntity, CodeManifestInvalid, fmt.Sprintf("the repository holds a %q extension, not a %s", m.Kind, wantKind))
	}
	if existing, gerr := s.Installed.Get(ctx, m.ID); gerr != nil {
		return nil, gerr
	} else if existing != nil {
		return nil, apiErr(http.StatusConflict, CodeIDConflict, fmt.Sprintf("an extension with id %q is already installed", m.ID))
	}
	if err := s.prepareTheme(ctx, f, token); err != nil {
		return nil, err
	}
	row := &domain.InstalledExtension{
		ExtensionID: m.ID, Version: m.Version, Manifest: m.Raw, Source: domain.ExtensionSourceRepo,
		RepoURL: repoURL, RepoRef: ref, CommitSHA: f.sha, TrustLevel: domain.ExtensionTrustCustom, InstalledBy: &actor,
	}
	if err := s.Repo.InsertRepo(ctx, row, token); err != nil {
		return nil, err
	}
	s.journal(ctx, &actor, m.ID, domain.InstanceJournalInstalled, map[string]any{"version": m.Version, "ref": ref, "commit_sha": f.sha, "repo_url": repoURL})
	s.changed(ctx)
	return s.Installed.Get(ctx, m.ID)
}

// UpdateInfo is one row of the Updates tab.
type UpdateInfo struct {
	ExtensionID     string   `json:"extension_id"`
	Name            string   `json:"name"`
	RepoURL         string   `json:"repo_url"`
	CurrentVersion  string   `json:"current_version"`
	CurrentRef      string   `json:"current_ref"`
	CurrentCommit   string   `json:"current_commit"`
	LatestVersion   string   `json:"latest_version,omitempty"`
	LatestRef       string   `json:"latest_ref,omitempty"`
	LatestCommit    string   `json:"latest_commit,omitempty"`
	UpdateAvailable bool     `json:"update_available"`
	NewCapabilities []string `json:"new_capabilities"`
	NeedsConsent    bool     `json:"needs_consent"`
	Pending         bool     `json:"pending"`
	CanRollback     bool     `json:"can_rollback"`
	PreviousVersion string   `json:"previous_version,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type candidate struct {
	f   *fetched
	ref string
}

// latest finds what the row's ref points at now. For a tag it also considers a
// newer semver tag; for a branch it is the branch head.
func (s *Service) latest(ctx context.Context, row *domain.InstalledExtension, token string) (*candidate, error) {
	src, err := s.Client.ParseURL(row.RepoURL)
	if err != nil {
		return nil, wrapHost(err)
	}
	ref := row.RepoRef
	tags, terr := s.Client.Tags(ctx, src, token)
	if terr != nil {
		return nil, wrapHost(terr)
	}
	for _, t := range tags {
		if t.Name == row.RepoRef {
			if best, ok := NewestTag(tags); ok && IsNewerTag(best.Name, row.RepoRef) {
				ref = best.Name
			}
			break
		}
	}
	f, err := s.fetchSrc(ctx, src, ref, token)
	if err != nil {
		return nil, err
	}
	if f.manifest.ID != row.ExtensionID {
		return nil, apiErr(http.StatusConflict, CodeIDConflict, fmt.Sprintf("the repository now declares id %q instead of %q", f.manifest.ID, row.ExtensionID))
	}
	return &candidate{f: f, ref: ref}, nil
}

func (s *Service) info(ctx context.Context, row *domain.InstalledExtension) UpdateInfo {
	cur, _ := extensions.ParseManifest(row.Manifest)
	in := UpdateInfo{
		ExtensionID: row.ExtensionID, Name: cur.Name, RepoURL: row.RepoURL, CurrentVersion: row.Version,
		CurrentRef: row.RepoRef, CurrentCommit: row.CommitSHA, NewCapabilities: []string{}, PreviousVersion: row.PreviousVersion,
		CanRollback: row.PreviousVersion != "",
	}
	token, err := s.Repo.GetToken(ctx, row.ExtensionID)
	if err != nil {
		in.Error = "token unreadable"
		return in
	}
	cand, err := s.latest(ctx, row, token)
	if err != nil {
		in.Error = err.Error()
		return in
	}
	in.LatestVersion, in.LatestRef, in.LatestCommit = cand.f.manifest.Version, cand.ref, cand.f.sha
	in.UpdateAvailable = cand.f.sha != row.CommitSHA || cand.ref != row.RepoRef
	if in.UpdateAvailable {
		in.NewCapabilities = NewCapabilities(Capabilities(cur), Capabilities(cand.f.manifest))
		in.NeedsConsent = len(in.NewCapabilities) > 0
		if st, _ := s.Repo.GetState(ctx, row.ExtensionID); st != nil && st.PendingCommitSHA == cand.f.sha {
			in.Pending = true
		}
	}
	return in
}

func (s *Service) repoRows(ctx context.Context) ([]domain.InstalledExtension, error) {
	rows, err := s.Installed.List(ctx)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for i := range rows {
		if rows[i].Source == domain.ExtensionSourceRepo {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// Updates compares every repo extension with its ref's current head.
func (s *Service) Updates(ctx context.Context) ([]UpdateInfo, error) {
	rows, err := s.repoRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UpdateInfo, 0, len(rows))
	for i := range rows {
		out = append(out, s.info(ctx, &rows[i]))
	}
	return out, nil
}

// Outcome of an update attempt.
type Outcome string

const (
	OutcomeUpdated        Outcome = "updated"
	OutcomeUpToDate       Outcome = "up_to_date"
	OutcomePendingConsent Outcome = "pending_consent"
)

// UpdateResult reports one update attempt.
type UpdateResult struct {
	ExtensionID     string   `json:"extension_id"`
	Outcome         Outcome  `json:"outcome"`
	Version         string   `json:"version,omitempty"`
	CommitSHA       string   `json:"commit_sha,omitempty"`
	NewCapabilities []string `json:"new_capabilities"`
	Error           string   `json:"error,omitempty"`
}

func (s *Service) repoRow(ctx context.Context, id string) (*domain.InstalledExtension, error) {
	row, err := s.Installed.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil || row.Source != domain.ExtensionSourceRepo {
		return nil, apierror.NotFound("repository extension")
	}
	return row, nil
}

// Update moves one extension to the newest commit of its ref. An update that
// adds permissions is parked as pending until consentSHA names that exact
// commit (the one the admin saw in the Updates tab).
func (s *Service) Update(ctx context.Context, actor *uuid.UUID, id, consentSHA string) (*UpdateResult, error) {
	return s.update(ctx, actor, id, consentSHA, false)
}

// update is Update; auto marks the unattended pass, which leaves alone the
// commit an admin rolled back from.
func (s *Service) update(ctx context.Context, actor *uuid.UUID, id, consentSHA string, auto bool) (*UpdateResult, error) {
	row, err := s.repoRow(ctx, id)
	if err != nil {
		return nil, err
	}
	token, err := s.Repo.GetToken(ctx, id)
	if err != nil {
		return nil, err
	}
	cand, err := s.latest(ctx, row, token)
	if err != nil {
		return nil, err
	}
	res := &UpdateResult{ExtensionID: id, Version: cand.f.manifest.Version, CommitSHA: cand.f.sha, NewCapabilities: []string{}}
	if cand.f.sha == row.CommitSHA && cand.ref == row.RepoRef {
		res.Outcome, res.Version, res.CommitSHA = OutcomeUpToDate, row.Version, row.CommitSHA
		return res, nil
	}
	if auto {
		if st, _ := s.Repo.GetState(ctx, id); st != nil && st.SkippedCommitSHA == cand.f.sha {
			res.Outcome, res.Version, res.CommitSHA = OutcomeUpToDate, row.Version, row.CommitSHA
			return res, nil
		}
	}
	cur, _ := extensions.ParseManifest(row.Manifest)
	res.NewCapabilities = NewCapabilities(Capabilities(cur), Capabilities(cand.f.manifest))
	if len(res.NewCapabilities) > 0 && consentSHA != cand.f.sha {
		if st, _ := s.Repo.GetState(ctx, id); st != nil && st.PendingCommitSHA == cand.f.sha {
			res.Outcome = OutcomePendingConsent
			return res, nil
		}
		if err := s.Repo.SetPending(ctx, id, cand.f.manifest.Version, cand.ref, cand.f.sha, cand.f.manifest.Raw); err != nil {
			return nil, err
		}
		s.journal(ctx, actor, id, domain.InstanceJournalUpdatePending, map[string]any{"version": cand.f.manifest.Version, "commit_sha": cand.f.sha, "new_capabilities": res.NewCapabilities})
		res.Outcome = OutcomePendingConsent
		return res, nil
	}
	if err := s.prepareTheme(ctx, cand.f, token); err != nil {
		return nil, err
	}
	if err := s.Repo.ApplyUpdate(ctx, id, cand.f.manifest.Version, cand.ref, cand.f.sha, cand.f.manifest.Raw); err != nil {
		return nil, err
	}
	s.journal(ctx, actor, id, domain.InstanceJournalUpdated, map[string]any{
		"from": row.Version, "to": cand.f.manifest.Version, "commit_sha": cand.f.sha, "ref": cand.ref, "consented_capabilities": res.NewCapabilities,
	})
	s.changed(ctx)
	res.Outcome = OutcomeUpdated
	return res, nil
}

// UpdateAll updates every extension that has an update and needs no consent;
// the ones that do need it are parked as pending.
func (s *Service) UpdateAll(ctx context.Context, actor *uuid.UUID) ([]UpdateResult, error) {
	return s.updateAll(ctx, actor, false)
}

func (s *Service) updateAll(ctx context.Context, actor *uuid.UUID, auto bool) ([]UpdateResult, error) {
	rows, err := s.repoRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UpdateResult, 0, len(rows))
	for i := range rows {
		r, uerr := s.update(ctx, actor, rows[i].ExtensionID, "", auto)
		if uerr != nil {
			out = append(out, UpdateResult{ExtensionID: rows[i].ExtensionID, NewCapabilities: []string{}, Error: uerr.Error()})
			continue
		}
		out = append(out, *r)
	}
	return out, nil
}

// Rollback returns an extension to the version before its last update.
func (s *Service) Rollback(ctx context.Context, actor *uuid.UUID, id string) (*domain.InstalledExtension, error) {
	row, err := s.repoRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.Repo.Rollback(ctx, id); err != nil {
		return nil, err
	}
	s.journal(ctx, actor, id, domain.InstanceJournalRolledBack, map[string]any{"from": row.Version, "to": row.PreviousVersion})
	s.changed(ctx)
	return s.Installed.Get(ctx, id)
}

// AutoUpdateOnce runs one pass of the instance auto-update: a no-op while the
// flag is off, otherwise the same as UpdateAll (permission growth waits for
// consent).
func (s *Service) AutoUpdateOnce(ctx context.Context) {
	cfg, err := s.Policy.GetConfig(ctx)
	if err != nil {
		log.Printf("[extensions] auto-update: read config: %v", err)
		return
	}
	if cfg == nil || !cfg.AutoUpdate {
		return
	}
	res, err := s.updateAll(ctx, nil, true)
	if err != nil {
		log.Printf("[extensions] auto-update: %v", err)
		return
	}
	for _, r := range res {
		if r.Error != "" {
			log.Printf("[extensions] auto-update %s: %s", r.ExtensionID, r.Error)
		}
	}
}

// RunAutoUpdate checks on every interval until ctx ends.
func (s *Service) RunAutoUpdate(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.AutoUpdateOnce(ctx)
		}
	}
}

func (s *Service) journal(ctx context.Context, actor *uuid.UUID, extID, action string, details map[string]any) {
	raw, _ := json.Marshal(details)
	if err := s.Policy.AppendJournal(ctx, &domain.InstanceExtensionJournalEntry{ActorID: actor, ExtensionID: extID, Action: action, Details: raw}); err != nil {
		log.Printf("[extensions] journal %s %s: %v", action, extID, err)
	}
}
