package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
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
}

// ExtensionConnectionView is the workspace connection of an extension.
type ExtensionConnectionView struct {
	ID                    uuid.UUID      `json:"id"`
	Settings              map[string]any `json:"settings"`
	ConsentedCapabilities []string       `json:"consented_capabilities"`
	ConsentedAt           *time.Time     `json:"consented_at,omitempty"`
	ConsentedBy           *uuid.UUID     `json:"consented_by,omitempty"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
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
}

// extensionIntegrationRepo is the integration repository plus the atomic
// create Connect needs: two concurrent connects must not overwrite each other.
type extensionIntegrationRepo interface {
	repository.IntegrationRepository
	InsertIfAbsent(ctx context.Context, cfg *domain.IntegrationConfig) (bool, error)
}

func NewExtensionWorkspaceService(reg *extensions.Registry, integ extensionIntegrationRepo, consents repository.ExtensionConsentRepository) *ExtensionWorkspaceService {
	return &ExtensionWorkspaceService{reg: reg, integ: integ, consents: consents}
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
		v, err := s.view(ctx, ws, p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Connect creates the workspace connection and records what was consented to.
func (s *ExtensionWorkspaceService) Connect(ctx context.Context, ws uuid.UUID, id string, by *uuid.UUID, settings map[string]any, consent []string) (*WorkspaceExtensionView, error) {
	p, err := s.provider(id)
	if err != nil {
		return nil, err
	}
	row, err := s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(id))
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
	if err = p.Validate(extensions.LevelWorkspace, settings); err != nil {
		return nil, apierror.ValidationError(map[string]string{"settings": err.Error()})
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
	if err = s.consents.SetConsent(ctx, stored.ID, domain.ExtensionConsent{Capabilities: consent, At: now, By: by}); err != nil {
		// Do not leave a connection without recorded consent: the retry would hit already_connected.
		if delErr := s.integ.Delete(ctx, stored.ID); delErr != nil {
			return nil, fmt.Errorf("connect extension: record consent: %w (rollback of the new connection also failed: %v)", err, delErr)
		}
		return nil, fmt.Errorf("connect extension: record consent: %w", err)
	}
	return s.viewPtr(ctx, ws, p)
}

// PatchSettings merges settings over the current ones. A secret sent back as
// SecretMask keeps its stored value.
func (s *ExtensionWorkspaceService) PatchSettings(ctx context.Context, ws uuid.UUID, id string, patch map[string]any) (*WorkspaceExtensionView, error) {
	p, row, err := s.connection(ctx, ws, id)
	if err != nil {
		return nil, err
	}
	cur := map[string]any{}
	if len(row.Config) > 0 {
		if err = json.Unmarshal(row.Config, &cur); err != nil {
			return nil, fmt.Errorf("patch settings: stored config: %w", err)
		}
	}
	secrets := secretFields(p.Manifest())
	for k, v := range patch {
		if str, ok := v.(string); ok && str == SecretMask && secrets[k] {
			continue
		}
		cur[k] = v
	}
	if err = p.Validate(extensions.LevelWorkspace, cur); err != nil {
		return nil, apierror.ValidationError(map[string]string{"settings": err.Error()})
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return nil, apierror.BadRequest("settings must be a JSON object")
	}
	if _, err := s.integ.Update(ctx, row.ID, domain.UpdateIntegrationInput{Config: raw}); err != nil {
		return nil, err
	}
	return s.viewPtr(ctx, ws, p)
}

// SetEnabled switches the workspace's own connection on or off. Switching off
// an extension the whole instance has on (status.source = instance) needs
// confirm=true and creates the workspace row that overrides the instance level.
func (s *ExtensionWorkspaceService) SetEnabled(ctx context.Context, ws uuid.UUID, id string, enabled, confirm bool) (*WorkspaceExtensionView, error) {
	p, err := s.provider(id)
	if err != nil {
		return nil, err
	}
	row, err := s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(id))
	if err != nil {
		return nil, err
	}
	if row == nil {
		if enabled {
			return nil, apierror.NotFoundWithDetails("Connection", "connect the extension first")
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
		created, err := s.integ.InsertIfAbsent(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: domain.IntegrationProvider(id), Config: []byte("{}"), IsActive: false, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return nil, err
		}
		if !created {
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
		}
		return s.viewPtr(ctx, ws, p)
	}
	if _, err := s.integ.Update(ctx, row.ID, domain.UpdateIntegrationInput{IsActive: &enabled}); err != nil {
		return nil, err
	}
	return s.viewPtr(ctx, ws, p)
}

// Test forces a fresh health check.
func (s *ExtensionWorkspaceService) Test(ctx context.Context, ws uuid.UUID, id string) (*ExtensionTestResult, error) {
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
	p, row, err := s.connection(ctx, ws, id)
	if err != nil {
		return err
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
	return s.integ.Delete(ctx, row.ID)
}

func (s *ExtensionWorkspaceService) connection(ctx context.Context, ws uuid.UUID, id string) (extensions.Provider, *domain.IntegrationConfig, error) {
	p, err := s.provider(id)
	if err != nil {
		return nil, nil, err
	}
	row, err := s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(id))
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
	v := WorkspaceExtensionView{
		ID: m.ID, Name: m.Name, Version: m.Version, Kind: m.Kind, Category: m.Category,
		Levels: m.Levels, Manifest: m.Raw, Status: statusView(st),
	}
	row, err := s.integ.GetByProvider(ctx, ws, domain.IntegrationProvider(m.ID))
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
	}
	return v, nil
}

func statusView(st extensions.Status) ExtensionStatusView {
	v := ExtensionStatusView{
		Enabled: st.Enabled, Source: string(st.Source), Version: st.Version,
		ConsentedCapabilities: append([]string{}, st.ConsentedCapabilities...),
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
