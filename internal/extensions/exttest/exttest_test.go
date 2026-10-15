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
