package exttest

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
)

func TestMemIntegrations_RoundTrip(t *testing.T) {
	ctx := context.Background()
	m := NewMemIntegrations()
	ws := uuid.New()
	row := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo", Config: []byte(`{"a":1}`), IsActive: true}

	got, err := m.GetByProvider(ctx, ws, "demo")
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, m.Upsert(ctx, row))
	// a second upsert for the same provider updates in place
	require.NoError(t, m.Upsert(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo", Config: []byte(`{"a":2}`), IsActive: false}))
	assert.Len(t, m.Rows, 1)
	got, err = m.GetByProvider(ctx, ws, "demo")
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":2}`, string(got.Config))
	assert.False(t, got.IsActive)

	on := true
	upd, err := m.Update(ctx, row.ID, domain.UpdateIntegrationInput{Config: []byte(`{"a":3}`), IsActive: &on})
	require.NoError(t, err)
	assert.True(t, upd.IsActive)
	assert.JSONEq(t, `{"a":3}`, string(upd.Config))

	require.NoError(t, m.Delete(ctx, row.ID))
	assert.Empty(t, m.Rows)

	m.Err = errors.New("db down")
	_, err = m.GetByProvider(ctx, ws, "demo")
	assert.Error(t, err)
}

func TestMemConsents(t *testing.T) {
	ctx := context.Background()
	integ := NewMemIntegrations()
	c := &MemConsents{Integ: integ, ByID: map[uuid.UUID]domain.ExtensionConsent{}}
	ws := uuid.New()

	v, err := c.GetConsent(ctx, ws, "demo")
	require.NoError(t, err)
	assert.Nil(t, v, "no row, no consent")

	row := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "demo"}
	require.NoError(t, integ.Upsert(ctx, row))
	v, err = c.GetConsent(ctx, ws, "demo")
	require.NoError(t, err)
	assert.Nil(t, v, "row without stored consent")

	require.NoError(t, c.SetConsent(ctx, row.ID, domain.ExtensionConsent{}))
	v, err = c.GetConsent(ctx, ws, "demo")
	require.NoError(t, err)
	assert.NotNil(t, v)
}

func TestDemoProvider(t *testing.T) {
	assert.NoError(t, DemoProvider{}.Validate(extensions.Level("workspace"), map[string]any{"ok": 1}))
	assert.Error(t, DemoProvider{}.Validate(extensions.Level("workspace"), map[string]any{"bad": 1}))
	assert.True(t, DemoProvider{Healthy: true}.Health(context.Background(), extensions.Resolved{}).OK)
	assert.Equal(t, "unreachable", DemoProvider{}.Health(context.Background(), extensions.Resolved{}).Message)

	n := 0
	require.NoError(t, PurgingProvider{Purged: &n}.PurgeData(context.Background(), uuid.New()))
	assert.Equal(t, 1, n)

	s, err := InstanceOn{}.InstanceSetting(context.Background(), "demo")
	require.NoError(t, err)
	assert.True(t, s.Enabled)
}

func TestNewFixture(t *testing.T) {
	for _, purge := range []bool{false, true} {
		f, err := NewFixture(InstanceOn{}, purge)
		require.NoError(t, err)
		assert.NotNil(t, f.Registry)
		_, ok := f.Registry.Get("demo")
		assert.True(t, ok)
	}
}

func TestMemIntegrations_GetByID(t *testing.T) {
	ctx := context.Background()
	m := NewMemIntegrations()
	row := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: uuid.New(), Provider: "demo"}
	require.NoError(t, m.Upsert(ctx, row))
	got, err := m.GetByID(ctx, row.ID)
	require.NoError(t, err)
	assert.Equal(t, row.ID, got.ID)
	got, err = m.GetByID(ctx, uuid.New())
	require.NoError(t, err)
	assert.Nil(t, got)
	m.Err = errors.New("db down")
	_, err = m.GetByID(ctx, row.ID)
	assert.Error(t, err)
}

func TestMemConsents_Reconsent(t *testing.T) {
	ctx := context.Background()
	integ := NewMemIntegrations()
	inst := &domain.InstalledExtension{ExtensionID: "demo", Version: "0.4.0"}
	hooked := 0
	c := &MemConsents{Integ: integ, ByID: map[uuid.UUID]domain.ExtensionConsent{},
		Installed: map[string]*domain.InstalledExtension{"demo": inst}, Hook: func() { hooked++ }}
	row := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: uuid.New(), Provider: "demo"}
	require.NoError(t, integ.Upsert(ctx, row))

	var seen *domain.InstalledExtension
	ok := func(tg domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
		seen = tg.Installed
		return domain.ExtensionConsent{Capabilities: []string{"api:tasks:read"}, Version: "0.4.0"}, []byte(`{"api":["tasks:read"]}`), nil
	}

	_, err := c.Reconsent(ctx, uuid.New(), "demo", ok)
	assert.Error(t, err, "no such connection")

	v, err := c.Reconsent(ctx, row.ID, "demo", ok)
	require.NoError(t, err)
	assert.Equal(t, "0.4.0", v.Version)
	assert.Same(t, inst, seen, "resolve sees the installed row")
	assert.Equal(t, "0.4.0", c.ByID[row.ID].Version)
	assert.JSONEq(t, `{"api":["tasks:read"]}`, string(c.ActorCaps[row.ID]))
	assert.Equal(t, 2, hooked)

	refused := errors.New("version changed")
	_, err = c.Reconsent(ctx, row.ID, "demo", func(domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
		return domain.ExtensionConsent{}, nil, refused
	})
	assert.ErrorIs(t, err, refused)

	c.Err = errors.New("db down")
	_, err = c.Reconsent(ctx, row.ID, "demo", ok)
	assert.ErrorIs(t, err, c.Err)
}
