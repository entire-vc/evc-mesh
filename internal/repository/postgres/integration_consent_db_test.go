package postgres

import (
	"context"
	"errors"
	"strings"
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

// Two concurrent connects: the loser must not overwrite the winner's config.
func TestIntegrationInsertIfAbsent_DoesNotOverwriteDB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	owner := &domain.User{
		ID: uuid.New(), Email: "insabs-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Insert Owner", Username: "insabs-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, owner))
	w := &domain.Workspace{ID: uuid.New(), Name: "insabs-ws", Slug: "insabs-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, w))

	integ := NewIntegrationRepo(db)
	mk := func(cfg string) *domain.IntegrationConfig {
		return &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: w.ID, Provider: "team-relay",
			Config: []byte(cfg), IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	}
	first, second := mk(`{"who":"first"}`), mk(`{"who":"second"}`)

	created, err := integ.InsertIfAbsent(ctx, first)
	require.NoError(t, err)
	assert.True(t, created)
	created, err = integ.InsertIfAbsent(ctx, second)
	require.NoError(t, err)
	assert.False(t, created, "a second insert reports it lost")

	got, err := integ.GetByProvider(ctx, w.ID, "team-relay")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, first.ID, got.ID)
	assert.JSONEq(t, `{"who":"first"}`, string(got.Config), "the winner's config is untouched")
}

// Reconsent on a real Postgres: resolve sees the installed row, the consent and
// the actor snapshot are written together, nothing else of the connection or
// the actor changes; a refusal writes neither.
func TestIntegrationConsent_ReconsentDB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	suffix := uuid.New().String()[:8]
	owner := &domain.User{
		ID: uuid.New(), Email: "reconsent-" + suffix + "@example.com", PasswordHash: "x",
		Name: "Reconsent Owner", Username: "reconsent-" + suffix, IsActive: true,
	}
	require.NoError(t, NewUserRepo(db).Create(ctx, owner))
	w := &domain.Workspace{ID: uuid.New(), Name: "reconsent-ws", Slug: "reconsent-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, NewWorkspaceRepo(db).Create(ctx, w))
	ext := "reconsent-" + suffix
	_, err := db.Exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source, commit_sha, trust_level)
		VALUES ($1, '0.4.0', '{}', 'repo', 'c4', 'community')`, ext)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, ext) })

	integ := NewIntegrationRepo(db)
	repo := NewIntegrationConsentRepo(db)
	cfg := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: w.ID, Provider: domain.IntegrationProvider(ext),
		Config: []byte(`{"u":"x"}`), IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, integ.Upsert(ctx, cfg))
	require.NoError(t, repo.SetConsent(ctx, cfg.ID, domain.ExtensionConsent{Capabilities: []string{"api:tasks:read"}, Version: "0.3.0"}))
	actorID := uuid.New()
	_, err = db.Exec(`INSERT INTO extension_actors (connection_id, workspace_id, actor_id, extension_id, token_hash, capabilities)
		VALUES ($1, $2, $3, $4, $5, '{"api":["tasks:read"]}')`, cfg.ID, w.ID, actorID, ext, strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""))
	require.NoError(t, err)

	refused := errors.New("version changed")
	_, err = repo.Reconsent(ctx, cfg.ID, ext, func(domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
		return domain.ExtensionConsent{}, nil, refused
	})
	require.ErrorIs(t, err, refused)
	c, err := repo.GetConsent(ctx, w.ID, ext)
	require.NoError(t, err)
	assert.Equal(t, "0.3.0", c.Version, "a refusal writes nothing")

	by := owner.ID
	var seen *domain.InstalledExtension
	got, err := repo.Reconsent(ctx, cfg.ID, ext, func(tg domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
		seen = tg.Installed
		return domain.ExtensionConsent{Capabilities: []string{"api:tasks:read", "api:tasks:write"}, By: &by, Version: "0.4.0", Commit: "c4"},
			[]byte(`{"api":["tasks:read","tasks:write"]}`), nil
	})
	require.NoError(t, err)
	require.NotNil(t, seen, "resolve sees the installed row")
	assert.Equal(t, "0.4.0", seen.Version)
	assert.Equal(t, "c4", seen.CommitSHA)
	assert.Equal(t, "0.4.0", got.Version)

	c, err = repo.GetConsent(ctx, w.ID, ext)
	require.NoError(t, err)
	assert.Equal(t, []string{"api:tasks:read", "api:tasks:write"}, c.Capabilities)
	assert.Equal(t, "0.4.0", c.Version)
	assert.Equal(t, "c4", c.Commit)
	require.NotNil(t, c.By)
	assert.Equal(t, owner.ID, *c.By)
	var snap string
	var actorAfter uuid.UUID
	require.NoError(t, db.QueryRow(`SELECT capabilities::text, actor_id FROM extension_actors WHERE connection_id = $1`, cfg.ID).Scan(&snap, &actorAfter))
	assert.JSONEq(t, `{"api":["tasks:read","tasks:write"]}`, snap)
	assert.Equal(t, actorID, actorAfter, "same actor")
	row, err := integ.GetByProvider(ctx, w.ID, domain.IntegrationProvider(ext))
	require.NoError(t, err)
	assert.JSONEq(t, `{"u":"x"}`, string(row.Config), "settings untouched")
	assert.True(t, row.IsActive)

	_, err = repo.Reconsent(ctx, uuid.New(), ext, func(domain.ExtensionReconsentTarget) (domain.ExtensionConsent, []byte, error) {
		t.Fatal("resolve must not run without a connection")
		return domain.ExtensionConsent{}, nil, nil
	})
	require.Error(t, err)
}
