package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Keep transaction contracts in the repository package as well as the service
// scenario: Go's per-package coverage does not instrument imported repositories.
func TestExtensionUninstallRepo_Transaction_DB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	extID := "uninst-tx-" + uuid.New().String()[:8]
	agent := newDigestAgent(t, db, uuid.New().String())
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, agent.WorkspaceID)
		_, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, extID)
		_, _ = db.Exec(`DELETE FROM extension_repo_credentials WHERE extension_id = $1`, extID)
		_, _ = db.Exec(`DELETE FROM extension_removals WHERE extension_id = $1`, extID)
		_, _ = db.Exec(`DELETE FROM instance_extension_journal WHERE extension_id = $1`, extID)
	})
	install := func() {
		require.NoError(t, NewRepoExtensionRepo(db).InsertRepo(ctx, &domain.InstalledExtension{
			ExtensionID: extID, Version: "1.0.0", Manifest: []byte(`{"kind":"integration"}`),
			Source: domain.ExtensionSourceRepo, RepoURL: "https://example.com/extension.git",
			RepoRef: "v1.0.0", TrustLevel: domain.ExtensionTrustCustom,
		}, "", nil))
	}
	install()
	// Synthetic ciphertext fixture, never used as an actual credential.
	_, err := db.Exec(`INSERT INTO extension_repo_credentials (extension_id, token_enc) VALUES ($1, $2)`, extID, "test-ciphertext")
	require.NoError(t, err)
	conn := &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: agent.WorkspaceID,
		Provider: domain.IntegrationProvider(extID), Config: []byte(`{"retained":"setting"}`), IsActive: true}
	require.NoError(t, NewIntegrationRepo(db).Upsert(ctx, conn))
	actors := NewExtensionActorRepo(db)
	previousHash, currentHash := hash64(uuid.New().String()), hash64(uuid.New().String())
	actor, err := actors.Issue(ctx, &domain.ExtensionActor{
		ConnectionID: conn.ID, WorkspaceID: agent.WorkspaceID, ActorID: uuid.New(),
		ExtensionID: extID, DisplayName: "Uninstall test", TokenHash: previousHash, Capabilities: []byte(`{}`),
	})
	require.NoError(t, err)
	ok, err := actors.Rotate(ctx, conn.ID, currentHash, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)

	repo := NewExtensionUninstallRepo(db)
	// Exceed the journal action's VARCHAR(32) after the deletes. All mutations
	// must roll back when the audit record cannot be written.
	report, err := repo.Uninstall(ctx, extID, &domain.InstanceExtensionJournalEntry{
		Action: strings.Repeat("x", 33),
	})
	require.Error(t, err)
	assert.Nil(t, report)
	row, err := NewInstalledExtensionRepo(db).Get(ctx, extID)
	require.NoError(t, err)
	require.NotNil(t, row)
	kept, err := NewIntegrationRepo(db).GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.NotNil(t, kept)
	assert.True(t, kept.IsActive)
	var credentials int
	require.NoError(t, db.Get(&credentials, `SELECT count(*) FROM extension_repo_credentials WHERE extension_id = $1`, extID))
	assert.Equal(t, 1, credentials)
	for _, hash := range []string{previousHash, currentHash} {
		got, lookupErr := actors.GetByTokenHash(ctx, hash, time.Now())
		require.NoError(t, lookupErr)
		require.NotNil(t, got, "rollback must keep both current and previous tokens")
	}
	var entries int
	require.NoError(t, db.Get(&entries, `SELECT count(*) FROM instance_extension_journal WHERE extension_id = $1`, extID))
	assert.Zero(t, entries)

	report, err = repo.Uninstall(ctx, extID, &domain.InstanceExtensionJournalEntry{Action: domain.InstanceJournalUninstalled})
	require.NoError(t, err)
	assert.Equal(t, extID, report.ExtensionID)
	assert.Equal(t, "1.0.0", report.Version)
	assert.Equal(t, string(domain.ExtensionSourceRepo), report.Source)
	assert.EqualValues(t, 1, report.Connections)
	assert.EqualValues(t, 1, report.TokensRevoked)
	row, err = NewInstalledExtensionRepo(db).Get(ctx, extID)
	require.NoError(t, err)
	assert.Nil(t, row)
	require.NoError(t, db.Get(&credentials, `SELECT count(*) FROM extension_repo_credentials WHERE extension_id = $1`, extID))
	assert.Zero(t, credentials)
	got, err := actors.GetByConnection(ctx, conn.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, actor.ActorID, got.ActorID)
	assert.NotNil(t, got.RevokedAt)
	// Current token lookup retains the revoked actor for authentication to deny;
	// the previous token's grace window is removed altogether.
	byToken, err := actors.GetByTokenHash(ctx, currentHash, time.Now())
	require.NoError(t, err)
	require.NotNil(t, byToken)
	assert.NotNil(t, byToken.RevokedAt)
	byToken, err = actors.GetByTokenHash(ctx, previousHash, time.Now())
	require.NoError(t, err)
	assert.Nil(t, byToken)
	var details []byte
	require.NoError(t, db.Get(&details, `SELECT details FROM instance_extension_journal WHERE extension_id = $1 AND action = $2`, extID, domain.InstanceJournalUninstalled))
	var out map[string]any
	require.NoError(t, json.Unmarshal(details, &out))
	assert.Equal(t, "1.0.0", out["version"])
	assert.Equal(t, true, out["data_kept"])
	assert.Equal(t, float64(1), out["connections_disabled"])
	assert.Equal(t, float64(1), out["tokens_revoked"])

	install()
	kept, err = NewIntegrationRepo(db).GetByID(ctx, conn.ID)
	require.NoError(t, err)
	require.NotNil(t, kept)
	assert.False(t, kept.IsActive)
	assert.JSONEq(t, `{"retained":"setting"}`, string(kept.Config))
	// Optional journal is also supported, without creating a second entry.
	_, err = repo.Uninstall(ctx, extID, nil)
	require.NoError(t, err)
	require.NoError(t, db.Get(&entries, `SELECT count(*) FROM instance_extension_journal WHERE extension_id = $1`, extID))
	assert.Equal(t, 1, entries)
}

func TestExtensionUninstallRepo_Refused_DB(t *testing.T) {
	db := agentDigestTestDB(t)
	ctx := context.Background()
	repo := NewExtensionUninstallRepo(db)
	for _, tc := range []struct {
		name, source, manifest string
	}{
		{"builtin", "builtin", `{"kind":"integration"}`},
		{"theme", "repo", `{"id":"uninstall-theme","version":"1.0.0","runtime":"builtin","kind":"theme"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "uninst-refuse-" + uuid.New().String()[:8]
			_, err := db.Exec(`INSERT INTO installed_extensions (extension_id, version, manifest, source, trust_level) VALUES ($1, '1.0.0', $2, $3, 'custom')`, id, tc.manifest, tc.source)
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM installed_extensions WHERE extension_id = $1`, id) })
			report, err := repo.Uninstall(ctx, id, nil)
			var apiErr *apierror.Error
			require.ErrorAs(t, err, &apiErr)
			assert.Equal(t, http.StatusConflict, apiErr.Code)
			assert.Nil(t, report)
			row, err := NewInstalledExtensionRepo(db).Get(ctx, id)
			require.NoError(t, err)
			assert.NotNil(t, row)
		})
	}
	_, err := repo.Uninstall(ctx, "uninst-missing-"+uuid.New().String(), nil)
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	report, err := repo.Uninstall(canceled, "unused", nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, report)
}
