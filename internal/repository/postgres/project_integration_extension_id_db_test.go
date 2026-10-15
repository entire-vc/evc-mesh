//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func extensionIDOf(t *testing.T, repo *ProjectIntegrationRepo, projectID uuid.UUID, intType string) string {
	t.Helper()
	pi, err := repo.Get(context.Background(), projectID, intType)
	require.NoError(t, err)
	require.NotNil(t, pi)
	return pi.ExtensionID
}

// W1.8 expand step: the image from before the migration writes rows without
// extension_id; the column must still be filled for them, and a row rewritten
// by the new code carries it too.
func TestProjectIntegrationExtensionID_FilledForEveryWriter(t *testing.T) {
	db := testDB(t)
	repo := NewProjectIntegrationRepo(db)
	ctx := context.Background()

	// Old image: direct insert, no extension_id, no key.
	oldProj := newIntegrationProject(t, db)
	_, err := db.ExecContext(ctx,
		`INSERT INTO project_integrations (id, project_id, type, enabled, settings, created_at, updated_at)
		 VALUES ($1, $2, 'team_relay', true, '{"share_id":"x"}', NOW(), NOW())`, uuid.New(), oldProj)
	require.NoError(t, err)
	require.Equal(t, "team-relay", extensionIDOf(t, repo, oldProj, "team_relay"))

	// New code: Upsert writes both columns.
	newProj := newIntegrationProject(t, db)
	now := time.Now()
	require.NoError(t, repo.Upsert(ctx, &domain.ProjectIntegration{
		ID: uuid.New(), ProjectID: newProj, Type: "team_relay", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}))
	require.Equal(t, "team-relay", extensionIDOf(t, repo, newProj, "team_relay"))

	// A type with no special mapping keeps its own name.
	require.NoError(t, repo.Upsert(ctx, &domain.ProjectIntegration{
		ID: uuid.New(), ProjectID: newProj, Type: "github", Enabled: true, CreatedAt: now, UpdatedAt: now,
	}))
	require.Equal(t, "github", extensionIDOf(t, repo, newProj, "github"))

	// An update of an old-style row that changes nothing about the credential
	// keeps extension_id.
	_, err = db.ExecContext(ctx, `UPDATE project_integrations SET enabled = false WHERE project_id = $1`, oldProj)
	require.NoError(t, err)
	require.Equal(t, "team-relay", extensionIDOf(t, repo, oldProj, "team_relay"))

	// A row that somehow has a NULL extension_id (written while the trigger was
	// off) is repaired by an update of any other column, not only of type.
	_, err = db.ExecContext(ctx, `ALTER TABLE project_integrations DISABLE TRIGGER trg_project_integrations_fill_extension_id`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE project_integrations SET extension_id = NULL WHERE project_id = $1 AND type = 'team_relay'`, oldProj)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `ALTER TABLE project_integrations ENABLE TRIGGER trg_project_integrations_fill_extension_id`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE project_integrations SET enabled = true WHERE project_id = $1`, oldProj)
	require.NoError(t, err)
	require.Equal(t, "team-relay", extensionIDOf(t, repo, oldProj, "team_relay"))
}
