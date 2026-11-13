package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// SecretMask is what a secret setting (manifest x-secret) is shown as. Sending
// it back on PATCH means "keep the stored value".
const SecretMask = "***"

// ExtensionDataPurger is implemented by a provider that can remove the data it
// keeps for a workspace; DELETE ...?data=delete needs it.
type ExtensionDataPurger interface {
	PurgeData(ctx context.Context, workspaceID uuid.UUID) error
}

// SettingsHider is implemented by a provider whose stored settings are a
// resource inventory the generic extension view must not disclose (Runner:
// the scoped runtime API is the only place that returns it).
type SettingsHider interface {
	HideConnectionSettings() bool
}

// ListingHider is implemented by a built-in part of Mesh that is registered as
// an extension but is not one to the user (Runner: the My Runner screen). The
// view marks it hidden so the extension lists leave it out; it stays registered
// for the extensions that require it.
type ListingHider interface {
	HiddenFromListings() bool
}

// ExtensionSaveHook is the extension-specific part of saving workspace
// settings that the legacy integrations handler used to do inline (Telegram:
// getMe check and bot_username, poller restart).
type ExtensionSaveHook interface {
	// Prepare runs after the manifest validation and before secrets are
	// encrypted; it may reject the settings or fill derived fields. `sent` holds
	// what the caller submitted: only those values are new, the rest is kept.
	Prepare(ctx context.Context, settings, sent map[string]any) error
	// Saved runs after the row was created or changed (also on enable/disable).
	Saved(ctx context.Context, integrationID uuid.UUID)
	// Removed runs after the connection was deleted.
	Removed(ctx context.Context, integrationID uuid.UUID)
}

// ExtensionReconsentHook is implemented by a save hook that reacts to a
// re-consent differently from a save (external: the handshake runs without
// token delivery, since a consent never changes the token). A hook without it
// gets Saved.
type ExtensionReconsentHook interface {
	Reconsented(ctx context.Context, integrationID uuid.UUID)
}

// ExtensionHealthView is the health part of ExtensionStatusView.
type ExtensionHealthView struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// ExtensionStatusView is the API form of extensions.Status (docs/extensions/api.md).
// health, health_checked_at and last_error are absent while the extension is off.
type ExtensionStatusView struct {
	Enabled               bool                 `json:"enabled"`
	Source                string               `json:"source"`
	Version               string               `json:"version"`
	Health                *ExtensionHealthView `json:"health,omitempty"`
	HealthCheckedAt       *time.Time           `json:"health_checked_at,omitempty"`
	LastError             string               `json:"last_error,omitempty"`
	ConsentedCapabilities []string             `json:"consented_capabilities"`
	// NotAllowed: the instance administrator does not allow the extension, so
	// it is off whatever the connection says; the connection itself is kept.
	NotAllowed bool `json:"not_allowed,omitempty"`
	// InstanceDisabled: the instance-level setting is switched off (read-only).
	InstanceDisabled bool `json:"instance_disabled,omitempty"`
	// Degraded: the status could not be resolved right now (the secret store
	// or the allow-list did not answer), so Enabled says nothing; the list
	// still shows the extension instead of failing whole.
	Degraded bool `json:"degraded,omitempty"`
}

// ExtensionConnectionView is the workspace connection of an extension.
type ExtensionConnectionView struct {
	ID                    uuid.UUID      `json:"id"`
	Settings              map[string]any `json:"settings"`
	ConsentedCapabilities []string       `json:"consented_capabilities"`
	ConsentedAt           *time.Time     `json:"consented_at,omitempty"`
	ConsentedBy           *uuid.UUID     `json:"consented_by,omitempty"`
	// ConsentedVersion and ConsentedCommit name the manifest release consented
	// to; empty for a consent recorded before they were stored.
	ConsentedVersion string `json:"consented_version"`
	ConsentedCommit  string `json:"consented_commit"`
	// NeedsConsent: the installed manifest declares rights the workspace has
	// not agreed to (PendingConsent lists them), or nothing was ever recorded.
	NeedsConsent   bool                `json:"needs_consent"`
	PendingConsent *PendingConsentView `json:"pending_consent"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

// PendingConsentView is what POST .../consent would grant: the installed
// release and the capabilities it declares beyond the recorded consent. It is
// computed on read, nothing is stored for it.
type PendingConsentView struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	// AddedCapabilities are flat "kind:value", as consented_capabilities.
	AddedCapabilities []string `json:"added_capabilities"`
	// AddedEvents are the event names among them, without the "events:" prefix.
	AddedEvents []string `json:"added_events"`
}

// WorkspaceExtensionView is one item of GET /workspaces/{ws}/extensions.
type WorkspaceExtensionView struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Version    string                   `json:"version"`
	Kind       string                   `json:"kind"`
	Category   string                   `json:"category"`
	Levels     extensions.Levels        `json:"levels"`
	Manifest   json.RawMessage          `json:"manifest"`
	Status     ExtensionStatusView      `json:"status"`
	Connection *ExtensionConnectionView `json:"connection"`
	// CanPurgeData: DELETE ...?data=delete is supported for this extension.
	CanPurgeData bool `json:"can_purge_data"`
	// SettingsManagedElsewhere: the stored settings are written only through the
	// extension's own API, so the generic connect/settings/remove calls refuse.
	SettingsManagedElsewhere bool `json:"settings_managed_elsewhere"`
	// Hidden: a built-in part of Mesh, left out of the Catalog/Installed/Updates lists.
	Hidden bool `json:"hidden,omitempty"`
	// Activity is set for gitlab/github only, and only when links exist.
	Activity *ExtensionActivityView `json:"activity,omitempty"`
}

// ExtensionActivityView is what the workspace's data says about an extension
// regardless of its connection row: GitLab/GitHub links keep arriving through
// webhooks whose secret lives at instance level, with no integration_config.
type ExtensionActivityView struct {
	Links       int        `json:"links"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
	// Repos are the repositories the links point into, most linked first
	// (at most ExtensionActivityRepoLimit).
	Repos []ExtensionRepoView `json:"repos"`
}

// ExtensionRepoView is one repository named by a provider's links.
type ExtensionRepoView struct {
	Repo        string    `json:"repo"`
	Host        string    `json:"host"`
	Links       int       `json:"links"`
	LastEventAt time.Time `json:"last_event_at"`
}

// ExtensionActivityRepoLimit caps activity.repos of the workspace list.
const ExtensionActivityRepoLimit = 20

// VCSActivitySource counts a provider's vcs_links in a workspace.
type VCSActivitySource interface {
	WorkspaceProviderActivity(ctx context.Context, workspaceID uuid.UUID, provider string) (count int, last *time.Time, err error)
	WorkspaceProviderRepos(ctx context.Context, workspaceID uuid.UUID, provider string, limit int) ([]repository.VCSRepoActivity, error)
}

// ExtensionTestResult is the body of POST .../test.
type ExtensionTestResult struct {
	OK      bool                `json:"ok"`
	Message string              `json:"message"`
	Status  ExtensionStatusView `json:"status"`
}

// ExtensionWorkspaceService is the workspace level of the extension API:
// a connection is the integration_configs row whose provider is the extension id.
type ExtensionWorkspaceService struct {
	reg      *extensions.Registry
	integ    extensionIntegrationRepo
	consents repository.ExtensionConsentRepository
	allow    ExtensionAllowList
	hooks    map[string]ExtensionSaveHook
	// external serves every runtime=external extension (they are registered at
	// run time, so no id-specific hook can be wired at startup).
	external ExtensionSaveHook
	activity VCSActivitySource
	// installed reads the installed release (version, commit, manifest) of an
	// extension; nil: the registered manifest, with no commit.
	installed InstalledReleaseReader
}

// InstalledReleaseReader reads one installed_extensions row (nil when none).
type InstalledReleaseReader interface {
	Get(ctx context.Context, extensionID string) (*domain.InstalledExtension, error)
}

// WithInstalledReleases lets the view and POST .../consent name the installed
// commit and read the manifest of an extension installed from a repository or
// the catalog from its row, the same row the consent transaction locks.
func (s *ExtensionWorkspaceService) WithInstalledReleases(r InstalledReleaseReader) *ExtensionWorkspaceService {
	s.installed = r
	return s
}

// ExternalRuntime marks a provider backed by an external service.
type ExternalRuntime interface {
	IsExternal() bool
}

// WithVCSActivity lets the list report link activity for the VCS extensions.
func (s *ExtensionWorkspaceService) WithVCSActivity(a VCSActivitySource) *ExtensionWorkspaceService {
	s.activity = a
	return s
}

// WithExternalHook registers the save hook shared by external extensions.
func (s *ExtensionWorkspaceService) WithExternalHook(h ExtensionSaveHook) *ExtensionWorkspaceService {
	s.external = h
	return s
}

// hookFor returns the hook of an extension id: its own, else the shared one
// when the registered provider is external.
func (s *ExtensionWorkspaceService) hookFor(id string) ExtensionSaveHook {
	if h := s.hooks[id]; h != nil {
		return h
	}
	if s.external == nil {
		return nil
	}
	if p, ok := s.reg.Get(id); ok {
		if e, isExt := p.(ExternalRuntime); isExt && e.IsExternal() {
			return s.external
		}
	}
	return nil
}

// ExtensionAllowList answers whether the instance allows an extension.
type ExtensionAllowList = extensions.AllowList

// WithAllowList makes Connect and switching on refuse extensions outside the
// instance allow-list.
func (s *ExtensionWorkspaceService) WithAllowList(a ExtensionAllowList) *ExtensionWorkspaceService {
	s.allow = a
	return s
}

// WithSaveHook registers the save hook of one extension id.
func (s *ExtensionWorkspaceService) WithSaveHook(extensionID string, h ExtensionSaveHook) *ExtensionWorkspaceService {
	if s.hooks == nil {
		s.hooks = map[string]ExtensionSaveHook{}
	}
	s.hooks[extensionID] = h
	return s
}

// refuseManagedElsewhere rejects a generic connect, settings or remove for an
// extension whose connection is owned by another flow (Runner).
func refuseManagedElsewhere(p extensions.Provider) error {
	if h, ok := p.(SettingsHider); ok && h.HideConnectionSettings() {
		return apierror.ValidationError(map[string]string{"extension": "this extension's connection is managed elsewhere and cannot be changed here"})
	}
	return nil
}

// prepareStored runs the extension hook and encrypts every secret the caller
// sent. A submitted value is always plaintext, whatever it starts with; kept
// secrets are not in `sent` and stay ciphertext.
func (s *ExtensionWorkspaceService) prepareStored(ctx context.Context, ws uuid.UUID, p extensions.Provider, settings, sent map[string]any) error {
	if h := s.hookFor(p.Manifest().ID); h != nil {
		if err := h.Prepare(ctx, settings, sent); err != nil {
			return err
		}
	}
	for k := range secretFields(p.Manifest()) {
		str, ok := settings[k].(string)
		if _, fresh := sent[k]; !ok || !fresh || str == "" {
			continue
		}
		enc, err := secretstore.Default().Put(ctx, secretstore.Ref{WorkspaceID: ws, ExtensionID: p.Manifest().ID, Field: k}, str)
		if err != nil {
			return apierror.BadRequestWithDetails("failed to store the secret setting", k)
		}
		settings[k] = enc
	}
	return nil
}

// extensionIntegrationRepo is the integration repository plus the atomic
// create Connect needs: two concurrent connects must not overwrite each other.
type extensionIntegrationRepo interface {
	repository.IntegrationRepository
	InsertIfAbsent(ctx context.Context, cfg *domain.IntegrationConfig) (bool, error)
	// SetActive switches one connection on or off and nothing else; it is the
	// only write the repository allows on a Runner row outside the runtime API.
	SetActive(ctx context.Context, id uuid.UUID, active bool) error
}

func NewExtensionWorkspaceService(reg *extensions.Registry, integ extensionIntegrationRepo, consents repository.ExtensionConsentRepository) *ExtensionWorkspaceService {
	return &ExtensionWorkspaceService{reg: reg, integ: integ, consents: consents}
}

// storedRow reads the workspace's connection row under the canonical extension
// id first, then under the aliases a provider still has rows stored by (the
// provider data rename is a later contract step), so an existing row is never
// reported as missing or duplicated.
func (s *ExtensionWorkspaceService) storedRow(ctx context.Context, ws uuid.UUID, id string) (*domain.IntegrationConfig, error) {
	id = s.reg.Canonical(id)
	row, err := s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(id))
	if err != nil || row != nil {
		return row, err
	}
	if p, ok := s.reg.Get(id); ok {
		if a, ok := p.(extensions.Aliased); ok {
			for _, alias := range a.Aliases() {
				if row, err = s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(alias)); err != nil || row != nil {
					return row, err
				}
			}
		}
	}
	return nil, nil
}

func extNotFound(id string) *apierror.Error {
	return apierror.NotFoundWithDetails("Extension", "extension "+id)
}

func (s *ExtensionWorkspaceService) provider(id string) (extensions.Provider, error) {
	p, ok := s.reg.Get(id)
	if !ok || !p.Manifest().Levels.Workspace {
		return nil, extNotFound(id)
	}
	return p, nil
}

// List returns every registered extension with its status in the workspace.
func (s *ExtensionWorkspaceService) List(ctx context.Context, ws uuid.UUID) ([]WorkspaceExtensionView, error) {
	out := []WorkspaceExtensionView{}
	for _, p := range s.reg.List() {
		if !p.Manifest().Levels.Workspace {
			continue
		}
		m := p.Manifest()
		var sv ExtensionStatusView
		st, err := s.reg.ResolveStatus(ctx, ws, m.ID)
		switch {
		case err == nil:
			sv = statusView(st)
		case ctx.Err() != nil:
			return nil, err
		default:
			// One extension the secret store cannot open must not fail the
			// whole list (an internal ticket: Keep 502 turned GET /extensions into 500).
			// Provider and secret store errors carry no credential-free
			// guarantee, so only a fixed class goes to the log.
			log.Printf("WARN extensions list: workspace %s: extension %q: %s", ws, m.ID, statusErrorClass(err))
			sv = degradedStatusView(m.Version)
		}
		v, err := s.viewOf(ctx, ws, p, sv)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// statusErrorClass names why a status could not be resolved without echoing
// the error text.
func statusErrorClass(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "secret store degraded"):
		return "secret store degraded"
	case strings.Contains(msg, "allow-list"):
		return "allow-list unavailable"
	case strings.Contains(msg, "consents"):
		return "consents unavailable"
	default:
		return "status resolution failed"
	}
}

func degradedStatusView(version string) ExtensionStatusView {
	return ExtensionStatusView{
		Source: string(extensions.SourceNone), Version: version, ConsentedCapabilities: []string{},
		LastError: "status temporarily unavailable", Degraded: true,
	}
}

// Connect creates the workspace connection and records what was consented to.
func (s *ExtensionWorkspaceService) Connect(ctx context.Context, ws uuid.UUID, id string, by *uuid.UUID, settings map[string]any, consent []string) (*WorkspaceExtensionView, error) {
	id = s.reg.Canonical(id)
	p, err := s.provider(id)
	if err != nil {
		return nil, err
	}
	if aerr := extensions.RequireAllowed(ctx, s.allow, id); aerr != nil {
		return nil, aerr
	}
	if merr := refuseManagedElsewhere(p); merr != nil {
		return nil, merr
	}
	row, err := s.storedRow(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return nil, &apierror.Error{Code: http.StatusConflict, ErrorCode: "already_connected", Message: "The extension is already connected in this workspace."}
	}
	m := p.Manifest()
	if missing := missingCapabilities(extensions.DeclaredCapabilities(m.Capabilities), consent); len(missing) > 0 {
		return nil, apierror.ValidationError(map[string]string{"consent": fmt.Sprintf("consent must cover %v", missing)})
	}
	if settings == nil {
		settings = map[string]any{}
	}
	if inh, ok := p.(interface {
		InheritInstance(ctx context.Context, settings map[string]any) error
	}); ok {
		if ierr := inh.InheritInstance(ctx, settings); ierr != nil {
			return nil, ierr
		}
	}
	if err = p.Validate(extensions.LevelWorkspace, settings); err != nil {
		return nil, apierror.ValidationError(map[string]string{"settings": err.Error()})
	}
	sent := make(map[string]any, len(settings))
	for k, v := range settings {
		sent[k] = v
	}
	if perr := s.prepareStored(ctx, ws, p, settings, sent); perr != nil {
		return nil, perr
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, apierror.BadRequest("settings must be a JSON object")
	}
	now := time.Now()
	cfg := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: domain.IntegrationProvider(id), Config: raw, IsActive: true, CreatedAt: now, UpdatedAt: now}
	created, err := s.integ.InsertIfAbsent(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect extension: %w", err)
	}
	if !created {
		return nil, &apierror.Error{Code: http.StatusConflict, ErrorCode: "already_connected", Message: "The extension is already connected in this workspace."}
	}
	stored, err := s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(id))
	if err != nil || stored == nil {
		return nil, fmt.Errorf("connect extension: reload: %w", errOrMissing(err))
	}
	rel, err := s.currentRelease(ctx, p)
	if err != nil {
		rel = release{Version: m.Version}
	}
	if err = s.consents.SetConsent(ctx, stored.ID, domain.ExtensionConsent{Capabilities: consent, At: now, By: by, Version: rel.Version, Commit: rel.Commit}); err != nil {
		// Do not leave a connection without recorded consent: the retry would hit already_connected.
		if delErr := s.integ.Delete(ctx, stored.ID); delErr != nil {
			return nil, fmt.Errorf("connect extension: record consent: %w (rollback of the new connection also failed: %v)", err, delErr)
		}
		return nil, fmt.Errorf("connect extension: record consent: %w", err)
	}
	s.saved(ctx, id, stored.ID)
	return s.viewPtr(ctx, ws, p)
}

// release is one installed manifest version of an extension: what a consent is
// given for.
type release struct {
	Version  string
	Commit   string
	Declared []string
}

// releaseOf is the installed release of p: the installed_extensions row for an
// extension installed from a repository or the catalog (its manifest is what
// the external runtime registers), else the registered manifest with the row's
// commit (empty for a builtin).
func releaseOf(p extensions.Provider, inst *domain.InstalledExtension) (release, error) {
	m := p.Manifest()
	if inst != nil && (inst.Source == domain.ExtensionSourceRepo || inst.Source == domain.ExtensionSourceCatalog) {
		im, err := extensions.ParseManifest(inst.Manifest)
		if err != nil {
			return release{}, fmt.Errorf("installed manifest of %s: %w", inst.ExtensionID, err)
		}
		return release{Version: inst.Version, Commit: inst.CommitSHA, Declared: extensions.DeclaredCapabilities(im.Capabilities)}, nil
	}
	r := release{Version: m.Version, Declared: extensions.DeclaredCapabilities(m.Capabilities)}
	if inst != nil {
		r.Commit = inst.CommitSHA
	}
	return r, nil
}

func (s *ExtensionWorkspaceService) currentRelease(ctx context.Context, p extensions.Provider) (release, error) {
	var inst *domain.InstalledExtension
	if s.installed != nil {
		var err error
		if inst, err = s.installed.Get(ctx, p.Manifest().ID); err != nil {
			return release{}, err
		}
	}
	return releaseOf(p, inst)
}

// errVersionChanged is the 409 of POST .../consent for a release that is not
// the installed one; context names the installed one.
func errVersionChanged(cur release) *apierror.Error {
	return &apierror.Error{Code: http.StatusConflict, ErrorCode: "version_changed",
		Message: "The installed extension version changed; review the rights of the installed version and consent again.",
		Context: map[string]any{"version": cur.Version, "commit": cur.Commit}}
}

// Reconsent records the workspace's consent to the installed manifest release
// version/commit of an existing connection: the consent becomes exactly what
// that release declares, and the extension actor's capability snapshot is
// replaced in the same transaction, so the token in use gains the added rights
// (and loses the dropped ones) on its next request. Tokens, settings, project
// scope and bindings, the enabled flag and the actor id are not touched. A
// release other than the installed one is refused with 409 version_changed.
// After the commit the install handshake is re-sent (subscriptions follow the
// new consent); its failure leaves the consent recorded and is retried by the
// health check, as after a connect.
func (s *ExtensionWorkspaceService) Reconsent(ctx context.Context, ws uuid.UUID, id string, by *uuid.UUID, version, commit string) (*WorkspaceExtensionView, error) {
	p, row, err := s.connection(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if merr := refuseManagedElsewhere(p); merr != nil {
		return nil, merr
	}
	if strings.TrimSpace(version) == "" {
		return nil, apierror.ValidationError(map[string]string{"version": "the manifest version consented to is required"})
	}
	m := p.Manifest()
	if aerr := extensions.RequireAllowed(ctx, s.allow, m.ID); aerr != nil {
		return nil, aerr
	}
	now := time.Now()
	_, err = s.consents.Reconsent(ctx, row.ID, m.ID, func(t domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
		cur, rerr := releaseOf(p, t.Installed)
		if rerr != nil {
			return domain.ExtensionConsent{}, nil, rerr
		}
		if cur.Version != version || cur.Commit != commit {
			return domain.ExtensionConsent{}, nil, errVersionChanged(cur)
		}
		// The registry may lag the row on another replica: a provider whose
		// manifest is not this release yet is refused too, or the rights
		// enforced would not be the ones agreed to.
		if t.Installed == nil || t.Installed.Source == domain.ExtensionSourceBuiltin {
			if m.Version != version {
				return domain.ExtensionConsent{}, nil, errVersionChanged(cur)
			}
		}
		caps, merr := json.Marshal(CapabilitiesByKind(cur.Declared))
		if merr != nil {
			return domain.ExtensionConsent{}, nil, merr
		}
		return domain.ExtensionConsent{Capabilities: cur.Declared, At: now, By: by, Version: cur.Version, Commit: cur.Commit}, caps, nil
	})
	if err != nil {
		return nil, err
	}
	s.reconsented(ctx, m.ID, row.ID)
	return s.viewPtr(ctx, ws, p)
}

// reconsented runs the hook's re-consent reaction, else its Saved.
func (s *ExtensionWorkspaceService) reconsented(ctx context.Context, id string, integrationID uuid.UUID) {
	h := s.hookFor(id)
	if h == nil {
		return
	}
	if rh, ok := h.(ExtensionReconsentHook); ok {
		rh.Reconsented(ctx, integrationID)
		return
	}
	h.Saved(ctx, integrationID)
}

// PatchSettings merges settings over the current ones. A secret sent back as
// SecretMask keeps its stored value.
func (s *ExtensionWorkspaceService) PatchSettings(ctx context.Context, ws uuid.UUID, id string, patch map[string]any) (*WorkspaceExtensionView, error) {
	p, row, err := s.connection(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if merr := refuseManagedElsewhere(p); merr != nil {
		return nil, merr
	}
	cur := map[string]any{}
	if len(row.Config) > 0 {
		if err = json.Unmarshal(row.Config, &cur); err != nil {
			return nil, fmt.Errorf("patch settings: stored config: %w", err)
		}
	}
	secrets := secretFields(p.Manifest())
	submitted := make(map[string]any, len(patch))
	for k, v := range patch {
		if str, ok := v.(string); ok && str == SecretMask && secrets[k] {
			continue
		}
		cur[k] = v
		submitted[k] = v
	}
	if err = p.Validate(extensions.LevelWorkspace, cur); err != nil {
		return nil, apierror.ValidationError(map[string]string{"settings": err.Error()})
	}
	if perr := s.prepareStored(ctx, ws, p, cur, submitted); perr != nil {
		return nil, perr
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return nil, apierror.BadRequest("settings must be a JSON object")
	}
	if _, err := s.integ.Update(ctx, row.ID, domain.UpdateIntegrationInput{Config: raw}); err != nil {
		return nil, err
	}
	s.saved(ctx, p.Manifest().ID, row.ID)
	return s.viewPtr(ctx, ws, p)
}

// SetEnabled switches the workspace's own connection on or off. Switching off
// an extension the whole instance has on (status.source = instance) needs
// confirm=true and creates the workspace row that overrides the instance level.
func (s *ExtensionWorkspaceService) SetEnabled(ctx context.Context, ws uuid.UUID, id string, enabled, confirm bool) (*WorkspaceExtensionView, error) {
	id = s.reg.Canonical(id)
	p, err := s.provider(id)
	if err != nil {
		return nil, err
	}
	// Switching off is always allowed, or a disallowed extension could not be
	// turned off at all.
	if enabled {
		if aerr := extensions.RequireAllowed(ctx, s.allow, id); aerr != nil {
			return nil, aerr
		}
	}
	row, err := s.storedRow(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		if enabled {
			// A managed-elsewhere extension (Runner) cannot be connected through
			// the generic flow, so enabling creates its empty row.
			if h, ok := p.(SettingsHider); !ok || !h.HideConnectionSettings() {
				return nil, apierror.NotFoundWithDetails("Connection", "connect the extension first")
			}
			now := time.Now()
			rowID := uuid.New()
			created, err := s.integ.InsertIfAbsent(ctx, &domain.IntegrationConfig{ID: rowID, WorkspaceID: ws, Provider: domain.IntegrationProvider(id), Config: initialConfig(id), IsActive: true, CreatedAt: now, UpdatedAt: now})
			if err != nil {
				return nil, err
			}
			if created {
				s.saved(ctx, id, rowID)
				return s.viewPtr(ctx, ws, p)
			}
			// A concurrent enable or the runtime flow (possibly under a legacy
			// alias) won the race: just make sure its row is on.
			won, err := s.storedRow(ctx, ws, id)
			if err != nil {
				return nil, err
			}
			if won == nil {
				return nil, &apierror.Error{Code: http.StatusConflict, ErrorCode: "conflict", Message: "The connection changed while enabling; retry."}
			}
			if err := s.setActive(ctx, p, won.ID, enabled); err != nil {
				return nil, err
			}
			s.saved(ctx, id, won.ID)
			return s.viewPtr(ctx, ws, p)
		}
		st, err := s.reg.ResolveStatus(ctx, ws, id)
		if err != nil {
			return nil, err
		}
		if st.Source != extensions.SourceInstance {
			return nil, apierror.NotFoundWithDetails("Connection", "the extension is not connected")
		}
		if !confirm {
			return nil, &apierror.Error{Code: http.StatusConflict, ErrorCode: "confirmation_required",
				Message: "The extension is on for the whole instance. Disabling it switches it off for this workspace only; send confirm=true to proceed."}
		}
		now := time.Now()
		rowID := uuid.New()
		created, err := s.integ.InsertIfAbsent(ctx, &domain.IntegrationConfig{ID: rowID, WorkspaceID: ws, Provider: domain.IntegrationProvider(id), Config: []byte("{}"), IsActive: false, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return nil, err
		}
		if created {
			s.saved(ctx, id, rowID)
		} else {
			// A connect won the race: keep its settings and consent, only switch it off.
			won, err := s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(id))
			if err != nil {
				return nil, err
			}
			if won == nil {
				return nil, &apierror.Error{Code: http.StatusConflict, ErrorCode: "conflict", Message: "The connection changed while disabling; retry."}
			}
			if _, err = s.integ.Update(ctx, won.ID, domain.UpdateIntegrationInput{IsActive: &enabled}); err != nil {
				return nil, err
			}
			s.saved(ctx, id, won.ID)
		}
		return s.viewPtr(ctx, ws, p)
	}
	if !enabled && !confirm {
		// A placeholder row (no settings of its own) can still resolve to the
		// instance level, so switching it off is the same instance-wide step
		// as disabling without a row.
		st, err := s.reg.ResolveStatus(ctx, ws, id)
		if err != nil {
			return nil, err
		}
		if st.Enabled && st.Source == extensions.SourceInstance {
			return nil, &apierror.Error{Code: http.StatusConflict, ErrorCode: "confirmation_required",
				Message: "The extension is on for the whole instance. Disabling it switches it off for this workspace only; send confirm=true to proceed."}
		}
	}
	if err := s.setActive(ctx, p, row.ID, enabled); err != nil {
		return nil, err
	}
	s.saved(ctx, id, row.ID)
	return s.viewPtr(ctx, ws, p)
}

// setActive flips a connection. A managed-elsewhere row (Runner) is refused by
// the repository's generic Update, so it goes through SetActive, which also
// records the switch in the Runner's versioned catalog.
func (s *ExtensionWorkspaceService) setActive(ctx context.Context, p extensions.Provider, rowID uuid.UUID, enabled bool) error {
	if h, ok := p.(SettingsHider); ok && h.HideConnectionSettings() {
		return s.integ.SetActive(ctx, rowID, enabled)
	}
	_, err := s.integ.Update(ctx, rowID, domain.UpdateIntegrationInput{IsActive: &enabled})
	return err
}

func (s *ExtensionWorkspaceService) saved(ctx context.Context, id string, integrationID uuid.UUID) {
	if h := s.hookFor(id); h != nil {
		h.Saved(ctx, integrationID)
	}
}

// Test forces a fresh health check.
func (s *ExtensionWorkspaceService) Test(ctx context.Context, ws uuid.UUID, id string) (*ExtensionTestResult, error) {
	id = s.reg.Canonical(id)
	if _, err := s.provider(id); err != nil {
		return nil, err
	}
	st, err := s.reg.TestConnection(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	view := statusView(st)
	res := &ExtensionTestResult{OK: st.Enabled && st.Health.OK, Message: st.Health.Message, Status: view}
	if !st.Enabled {
		res.Message = "extension is not enabled in this workspace"
	}
	return res, nil
}

// Disconnect removes the connection; purge also removes the extension's data.
func (s *ExtensionWorkspaceService) Disconnect(ctx context.Context, ws uuid.UUID, id string, purge bool) error {
	id = s.reg.Canonical(id)
	p, row, err := s.connection(ctx, ws, id)
	if err != nil {
		return err
	}
	if merr := refuseManagedElsewhere(p); merr != nil {
		return merr
	}
	var purger ExtensionDataPurger
	if purge {
		var ok bool
		if purger, ok = p.(ExtensionDataPurger); !ok {
			return apierror.ValidationError(map[string]string{"data": "this extension does not support data=delete"})
		}
	}
	// Purge first: if it fails the connection stays, so the call can be retried.
	if purger != nil {
		if err := purger.PurgeData(ctx, ws); err != nil {
			return fmt.Errorf("disconnect extension %s: purge data: %w", id, err)
		}
	}
	if err := s.integ.Delete(ctx, row.ID); err != nil {
		return err
	}
	if h := s.hookFor(id); h != nil {
		h.Removed(ctx, row.ID)
	}
	return nil
}

func (s *ExtensionWorkspaceService) connection(ctx context.Context, ws uuid.UUID, id string) (extensions.Provider, *domain.IntegrationConfig, error) {
	id = s.reg.Canonical(id)
	p, err := s.provider(id)
	if err != nil {
		return nil, nil, err
	}
	row, err := s.storedRow(ctx, ws, id)
	if err != nil {
		return nil, nil, err
	}
	if row == nil {
		return nil, nil, apierror.NotFoundWithDetails("Connection", "the extension is not connected")
	}
	return p, row, nil
}

func (s *ExtensionWorkspaceService) viewPtr(ctx context.Context, ws uuid.UUID, p extensions.Provider) (*WorkspaceExtensionView, error) {
	v, err := s.view(ctx, ws, p)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *ExtensionWorkspaceService) view(ctx context.Context, ws uuid.UUID, p extensions.Provider) (WorkspaceExtensionView, error) {
	m := p.Manifest()
	st, err := s.reg.ResolveStatus(ctx, ws, m.ID)
	if err != nil {
		return WorkspaceExtensionView{}, err
	}
	return s.viewOf(ctx, ws, p, statusView(st))
}

func (s *ExtensionWorkspaceService) viewOf(ctx context.Context, ws uuid.UUID, p extensions.Provider, sv ExtensionStatusView) (WorkspaceExtensionView, error) {
	m := p.Manifest()
	v := WorkspaceExtensionView{
		ID: m.ID, Name: m.Name, Version: m.Version, Kind: m.Kind, Category: m.Category,
		Levels: m.Levels, Manifest: m.Raw, Status: sv,
	}
	_, v.CanPurgeData = p.(ExtensionDataPurger)
	if h, ok := p.(SettingsHider); ok && h.HideConnectionSettings() {
		v.SettingsManagedElsewhere = true
	}
	if h, ok := p.(ListingHider); ok && h.HiddenFromListings() {
		v.Hidden = true
	}
	if s.activity != nil && (m.ID == "gitlab" || m.ID == "github") {
		n, last, actErr := s.activity.WorkspaceProviderActivity(ctx, ws, m.ID)
		if actErr != nil {
			return WorkspaceExtensionView{}, actErr
		}
		if n > 0 {
			repos, repoErr := s.activity.WorkspaceProviderRepos(ctx, ws, m.ID, ExtensionActivityRepoLimit)
			if repoErr != nil {
				return WorkspaceExtensionView{}, repoErr
			}
			v.Activity = &ExtensionActivityView{Links: n, LastEventAt: last, Repos: RepoViews(repos)}
		}
	}
	row, err := s.storedRow(ctx, ws, m.ID)
	if err != nil {
		return WorkspaceExtensionView{}, err
	}
	if row != nil {
		c, err := s.consents.GetConsent(ctx, ws, m.ID)
		if err != nil {
			return WorkspaceExtensionView{}, err
		}
		v.Connection, err = connectionView(row, c, secretFields(m))
		if err != nil {
			return WorkspaceExtensionView{}, err
		}
		if h, ok := p.(SettingsHider); ok && h.HideConnectionSettings() {
			// Managed elsewhere (Runner): no generic consent flow either.
			v.Connection.Settings = map[string]any{}
		} else if err := s.fillPending(ctx, p, v.Connection, c); err != nil {
			return WorkspaceExtensionView{}, err
		}
	}
	return v, nil
}

// fillPending sets needs_consent and pending_consent: the installed release's
// declared capabilities minus the recorded consent; all of them when nothing was
// recorded. Rights the recorded consent has but the release no longer declares
// are not pending: they stopped acting already.
func (s *ExtensionWorkspaceService) fillPending(ctx context.Context, p extensions.Provider, v *ExtensionConnectionView, c *domain.ExtensionConsent) error {
	rel, err := s.currentRelease(ctx, p)
	if err != nil {
		return err
	}
	var consented []string
	if c != nil {
		consented = c.Capabilities
	}
	added := missingCapabilities(rel.Declared, consented)
	if c != nil && len(added) == 0 {
		return nil
	}
	pv := &PendingConsentView{Version: rel.Version, Commit: rel.Commit, AddedCapabilities: []string{}, AddedEvents: []string{}}
	for _, a := range added {
		pv.AddedCapabilities = append(pv.AddedCapabilities, a)
		if ev, ok := strings.CutPrefix(a, "events:"); ok {
			pv.AddedEvents = append(pv.AddedEvents, ev)
		}
	}
	v.NeedsConsent, v.PendingConsent = true, pv
	return nil
}

func statusView(st extensions.Status) ExtensionStatusView {
	v := ExtensionStatusView{
		Enabled: st.Enabled, Source: string(st.Source), Version: st.Version,
		ConsentedCapabilities: append([]string{}, st.ConsentedCapabilities...),
		NotAllowed:            st.NotAllowed,
		InstanceDisabled:      st.InstanceDisabled,
	}
	if st.Enabled {
		v.Health = &ExtensionHealthView{OK: st.Health.OK, Message: st.Health.Message}
		if !st.HealthCheckedAt.IsZero() {
			t := st.HealthCheckedAt
			v.HealthCheckedAt = &t
		}
		v.LastError = st.LastError
	}
	return v
}

func connectionView(row *domain.IntegrationConfig, c *domain.ExtensionConsent, secrets map[string]bool) (*ExtensionConnectionView, error) {
	settings := map[string]any{}
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &settings); err != nil {
			return nil, fmt.Errorf("connection %s: bad config: %w", row.ID, err)
		}
	}
	for k := range settings {
		if secrets[k] && settings[k] != nil && settings[k] != "" {
			settings[k] = SecretMask
		}
	}
	v := &ExtensionConnectionView{ID: row.ID, Settings: settings, ConsentedCapabilities: []string{}, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if c != nil {
		v.ConsentedCapabilities = append([]string{}, c.Capabilities...)
		at := c.At
		v.ConsentedAt, v.ConsentedBy = &at, c.By
		v.ConsentedVersion, v.ConsentedCommit = c.Version, c.Commit
	}
	return v, nil
}

// secretFields names the workspace-level settings the manifest marks x-secret,
// whether the level is inline or a "#/schemas/<name>" ref.
func secretFields(m extensions.Manifest) map[string]bool {
	out := map[string]bool{}
	for _, k := range m.SecretKeys("workspace") {
		out[k] = true
	}
	return out
}

func missingCapabilities(declared, consented []string) []string {
	have := map[string]bool{}
	for _, c := range consented {
		have[c] = true
	}
	var missing []string
	for _, d := range declared {
		if !have[d] {
			missing = append(missing, d)
		}
	}
	sort.Strings(missing)
	return missing
}

func errOrMissing(err error) error {
	if err != nil {
		return err
	}
	return errors.New("row not found after upsert")
}

// RepoViews turns repository rows into their API shape (never nil, so an
// empty list reads as []).
func RepoViews(rows []repository.VCSRepoActivity) []ExtensionRepoView {
	out := make([]ExtensionRepoView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ExtensionRepoView{Repo: r.Repo, Host: r.Host, Links: r.Links, LastEventAt: r.LastEventAt})
	}
	return out
}

// initialConfig is the config a connection starts with: Runner keeps a catalog
// (schema v2), so its empty row is the valid empty catalog rather than `{}`.
func initialConfig(id string) []byte {
	if id == "runner" {
		return domain.EmptyRuntimeCatalog
	}
	return []byte("{}")
}
