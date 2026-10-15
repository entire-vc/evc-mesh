package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/repository"
)

type fakeIntegrationRepo struct {
	repository.IntegrationRepository
	row *domain.IntegrationConfig
	err error
}

func (f fakeIntegrationRepo) GetByProvider(context.Context, uuid.UUID, domain.IntegrationProvider) (*domain.IntegrationConfig, error) {
	return f.row, f.err
}

func TestIntegrationConnections(t *testing.T) {
	ctx := context.Background()
	ws := uuid.New()

	c, err := extensions.IntegrationConnections{Repo: fakeIntegrationRepo{}}.Connection(ctx, ws, "gitlab")
	require.NoError(t, err)
	require.Nil(t, c, "no row means no connection")

	c, err = extensions.IntegrationConnections{Repo: fakeIntegrationRepo{row: &domain.IntegrationConfig{Config: []byte(`{"base_url":"https://g"}`), IsActive: true}}}.Connection(ctx, ws, "gitlab")
	require.NoError(t, err)
	require.True(t, c.IsActive)
	require.Equal(t, "https://g", c.Settings["base_url"])

	c, err = extensions.IntegrationConnections{Repo: fakeIntegrationRepo{row: &domain.IntegrationConfig{IsActive: false}}}.Connection(ctx, ws, "gitlab")
	require.NoError(t, err)
	require.False(t, c.IsActive)
	require.Empty(t, c.Settings)

	_, err = extensions.IntegrationConnections{Repo: fakeIntegrationRepo{row: &domain.IntegrationConfig{Config: []byte(`{`)}}}.Connection(ctx, ws, "gitlab")
	require.Error(t, err)

	boom := errors.New("db")
	_, err = extensions.IntegrationConnections{Repo: fakeIntegrationRepo{err: boom}}.Connection(ctx, ws, "gitlab")
	require.ErrorIs(t, err, boom)
}

func TestBaseProviderDefaults(t *testing.T) {
	b := extensions.BaseProvider{Man: extensions.Manifest{ID: "x"}}
	require.True(t, b.Health(context.Background(), extensions.Resolved{Enabled: true}).OK)
	require.False(t, b.Health(context.Background(), extensions.Resolved{}).OK)
	require.Empty(t, b.Hooks().Routes)
	require.Empty(t, b.UI().Slots)
}

type fakeConsentRepo struct {
	repository.ExtensionConsentRepository
	c   *domain.ExtensionConsent
	err error
}

func (f fakeConsentRepo) GetConsent(context.Context, uuid.UUID, string) (*domain.ExtensionConsent, error) {
	return f.c, f.err
}

func TestIntegrationConsents(t *testing.T) {
	declared := func(id string) []string { return []string{"declared:" + id} }
	ws := uuid.New()

	recorded := extensions.IntegrationConsents{
		Repo:     fakeConsentRepo{c: &domain.ExtensionConsent{Capabilities: []string{"api:tasks.read"}}},
		Declared: declared,
	}
	got, err := recorded.Consented(context.Background(), ws, "x")
	require.NoError(t, err)
	require.Equal(t, []string{"api:tasks.read"}, got, "recorded consent wins over the manifest")

	legacy := extensions.IntegrationConsents{Repo: fakeConsentRepo{}, Declared: declared}
	got, err = legacy.Consented(context.Background(), ws, "x")
	require.NoError(t, err)
	require.Equal(t, []string{"declared:x"}, got, "no consent recorded: the declared capabilities count")

	none := extensions.IntegrationConsents{Repo: fakeConsentRepo{}}
	got, err = none.Consented(context.Background(), ws, "x")
	require.NoError(t, err)
	require.Nil(t, got)

	boom := errors.New("db down")
	_, err = extensions.IntegrationConsents{Repo: fakeConsentRepo{err: boom}}.Consented(context.Background(), ws, "x")
	require.ErrorIs(t, err, boom)
}
