// Package exttest holds in-memory stand-ins for the extension API tests: an
// integration_configs table, the consent columns and a demo provider. Test-only;
// nothing in the server imports it.
package exttest

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

// Manifest is the manifest of the demo extension (workspace level, one secret).
const Manifest = `{"id":"demo","name":"Demo","version":"1.0.0","kind":"integration","runtime":"builtin","category":"vcs",
 "levels":{"instance":true,"workspace":true,"project":false},
 "settings":{"workspace":{"properties":{"base_url":{"type":"string"},"token":{"type":"string","x-secret":true}}}},
 "capabilities":{"api":["tasks.read"],"events":["task.updated"]}}`

// MemIntegrations is an in-memory integration_configs table.
type MemIntegrations struct {
	repository.IntegrationRepository
	mu   sync.Mutex
	Rows map[uuid.UUID]*domain.IntegrationConfig
	Err  error
	// DeleteErr makes Delete fail; RaceRow is inserted just before the next InsertIfAbsent
	// looks (a concurrent connect that wins).
	DeleteErr error
	RaceRow   *domain.IntegrationConfig
}

func NewMemIntegrations() *MemIntegrations {
	return &MemIntegrations{Rows: map[uuid.UUID]*domain.IntegrationConfig{}}
}

func (m *MemIntegrations) Find(ws uuid.UUID, p domain.IntegrationProvider) *domain.IntegrationConfig {
	for _, r := range m.Rows {
		if r.WorkspaceID == ws && r.Provider == p {
			return r
		}
	}
	return nil
}

func (m *MemIntegrations) GetByProvider(_ context.Context, ws uuid.UUID, p domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Find(ws, p), nil
}

func (m *MemIntegrations) Upsert(_ context.Context, c *domain.IntegrationConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.Find(c.WorkspaceID, c.Provider); old != nil {
		old.Config, old.IsActive = c.Config, c.IsActive
		return nil
	}
	cp := *c
	m.Rows[c.ID] = &cp
	return nil
}

func (m *MemIntegrations) InsertIfAbsent(_ context.Context, c *domain.IntegrationConfig) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.RaceRow != nil {
		cp := *m.RaceRow
		m.Rows[cp.ID] = &cp
		m.RaceRow = nil
	}
	if m.Find(c.WorkspaceID, c.Provider) != nil {
		return false, nil
	}
	cp := *c
	m.Rows[c.ID] = &cp
	return true, nil
}

func (m *MemIntegrations) Update(_ context.Context, id uuid.UUID, in domain.UpdateIntegrationInput) (*domain.IntegrationConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.Rows[id]
	if in.Config != nil {
		r.Config = in.Config
	}
	if in.IsActive != nil {
		r.IsActive = *in.IsActive
	}
	return r, nil
}

func (m *MemIntegrations) SetActive(_ context.Context, id uuid.UUID, active bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Rows[id].IsActive = active
	return nil
}

func (m *MemIntegrations) Delete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	delete(m.Rows, id)
	return nil
}

type MemConsents struct {
	repository.ExtensionConsentRepository
	Integ *MemIntegrations
	ByID  map[uuid.UUID]domain.ExtensionConsent
	Err   error // SetConsent fails with it when set
}

func (c *MemConsents) GetConsent(_ context.Context, ws uuid.UUID, ext string) (*domain.ExtensionConsent, error) {
	r := c.Integ.Find(ws, domain.IntegrationProvider(ext))
	if r == nil {
		return nil, nil
	}
	v, ok := c.ByID[r.ID]
	if !ok {
		return nil, nil
	}
	return &v, nil
}

func (c *MemConsents) SetConsent(_ context.Context, id uuid.UUID, v domain.ExtensionConsent) error {
	if c.Err != nil {
		return c.Err
	}
	c.ByID[id] = v
	return nil
}

type DemoProvider struct {
	extensions.BaseProvider
	Healthy bool
}

func (p DemoProvider) Validate(_ extensions.Level, s map[string]any) error {
	if _, bad := s["bad"]; bad {
		return errors.New("bad is not allowed")
	}
	return nil
}

func (p DemoProvider) Health(context.Context, extensions.Resolved) extensions.HealthReport {
	if p.Healthy {
		return extensions.HealthReport{OK: true}
	}
	return extensions.HealthReport{Message: "unreachable"}
}

type PurgingProvider struct {
	DemoProvider
	Purged *int
	Err    func() error // PurgeData fails with its result when set
}

// PurgeData lets the demo extension support data=delete.
func (p PurgingProvider) PurgeData(context.Context, uuid.UUID) error {
	*p.Purged++
	if p.Err != nil {
		return p.Err()
	}
	return nil
}

type InstanceOn struct{}

func (InstanceOn) InstanceSetting(context.Context, string) (*extensions.InstanceSetting, error) {
	return &extensions.InstanceSetting{Enabled: true}, nil
}

// Fixture is a registry with the demo extension over in-memory tables.
type Fixture struct {
	Registry *extensions.Registry
	Integ    *MemIntegrations
	Consents *MemConsents
	Purged   int
	PurgeErr error // PurgeData fails with it when set
}

// NewFixture builds the fixture. instance may be nil (no instance level);
// purge makes the demo provider support data=delete.
func NewFixture(instance extensions.InstanceStore, purge bool) (*Fixture, error) {
	man, err := extensions.ParseManifest([]byte(Manifest))
	if err != nil {
		return nil, err
	}
	f := &Fixture{Integ: NewMemIntegrations()}
	f.Consents = &MemConsents{Integ: f.Integ, ByID: map[uuid.UUID]domain.ExtensionConsent{}}
	base := extensions.BaseProvider{Man: man, Conns: extensions.IntegrationConnections{Repo: f.Integ}, Inst: instance}
	var p extensions.Provider = DemoProvider{BaseProvider: base, Healthy: true}
	if purge {
		p = PurgingProvider{DemoProvider: DemoProvider{BaseProvider: base, Healthy: true}, Purged: &f.Purged, Err: func() error { return f.PurgeErr }}
	}
	f.Registry = extensions.NewRegistry()
	if err := f.Registry.Register(p); err != nil {
		return nil, err
	}
	f.Registry.WithConsentStore(extensions.IntegrationConsents{Repo: f.Consents, Declared: func(string) []string { return nil }})
	return f, nil
}
