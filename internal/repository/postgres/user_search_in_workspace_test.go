package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// No build tag, like user_search_scope_test.go: skips when no Postgres is
// reachable, runs (and counts toward the coverage gate) in CI.

// SearchInWorkspace must surface users.service_agent_id: a service user comes
// back linked to its agent, a person with the same display-name shape does not.
func TestSearchInWorkspace_ReturnsServiceAgentLink(t *testing.T) {
	db := userRepoTestDB(t)
	ctx := context.Background()
	repo := NewUserRepo(db)
	token := "zzsvc" + strings.ReplaceAll(uuid.New().String()[:8], "-", "")

	ws, agent := uuid.New(), uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		ws, "Zzsvc", "zzsvc-"+uuid.New().String()[:8], uuid.New(),
		json.RawMessage(`{}`), time.Now().UTC(), time.Now().UTC())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM workspace_members WHERE workspace_id = $1", ws)
		_, _ = db.ExecContext(ctx, "DELETE FROM agents WHERE id = $1", agent)
		_, _ = db.ExecContext(ctx, "DELETE FROM workspaces WHERE id = $1", ws)
	})
	_, err = db.ExecContext(ctx,
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix) VALUES ($1,$2,$3,$4,'h','p')`,
		agent, ws, token, token)
	require.NoError(t, err)

	mk := func(label string, link *uuid.UUID) uuid.UUID {
		u := &domain.User{
			ID: uuid.New(), Email: token + "-" + label + "@test.example", PasswordHash: "x",
			Name: token + " (QA " + label + ")", Username: token + "-" + label, IsActive: true,
			ServiceAgentID: link, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		_, err = db.ExecContext(ctx,
			`INSERT INTO users (id, email, password_hash, display_name, username, is_active, service_agent_id, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			u.ID, u.Email, u.PasswordHash, u.Name, u.Username, u.IsActive, u.ServiceAgentID, u.CreatedAt, u.UpdatedAt)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx,
			`INSERT INTO workspace_members (id, workspace_id, user_id, role, invited_by, created_at, updated_at)
			 VALUES ($1,$2,$3,'member',NULL,$4,$5)`, uuid.New(), ws, u.ID, time.Now().UTC(), time.Now().UTC())
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DELETE FROM users WHERE id = $1", u.ID) })
		return u.ID
	}
	svc := mk("mesh", &agent)
	person := mk("x", nil)

	got, err := repo.SearchInWorkspace(ctx, ws, token, 10)
	require.NoError(t, err)
	byID := map[uuid.UUID]domain.User{}
	for _, u := range got {
		byID[u.ID] = u
	}

	require.Contains(t, byID, svc)
	require.NotNil(t, byID[svc].ServiceAgentID)
	assert.Equal(t, agent, *byID[svc].ServiceAgentID)
	require.Contains(t, byID, person)
	assert.Nil(t, byID[person].ServiceAgentID, "a person named like a service user stays unlinked")
}
