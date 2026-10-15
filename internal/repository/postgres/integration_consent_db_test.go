package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Round-trips the consent columns through a real Postgres: NULL means "no
// consent recorded", and a legacy Upsert must not wipe recorded consent.
func TestIntegrationConsent_RoundTripDB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	owner := &domain.User{
		ID: uuid.New(), Email: "consent-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Consent Owner", Username: "consent-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, owner))
	w := &domain.Workspace{ID: uuid.New(), Name: "consent-ws", Slug: "consent-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, w))
	ws := w.ID

	integ := NewIntegrationRepo(db)
	repo := NewIntegrationConsentRepo(db)
	cfg := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: ws, Provider: "team-relay",
		Config: []byte(`{}`), IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, integ.Upsert(ctx, cfg))

	c, err := repo.GetConsent(ctx, ws, "team-relay")
	require.NoError(t, err)
	assert.Nil(t, c, "fresh connection has no consent")

	by := uuid.New()
	require.NoError(t, repo.SetConsent(ctx, cfg.ID, domain.ExtensionConsent{Capabilities: []string{"api:tasks.read"}, At: time.Now(), By: &by}))
	c, err = repo.GetConsent(ctx, ws, "team-relay")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.Equal(t, []string{"api:tasks.read"}, c.Capabilities)

	require.NoError(t, integ.Upsert(ctx, cfg), "legacy upsert")
	c, err = repo.GetConsent(ctx, ws, "team-relay")
	require.NoError(t, err)
	require.NotNil(t, c, "legacy Upsert keeps recorded consent")
}
