package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// No //go:build integration tag — same convention as
// memory_handler_reserved_tag_db_test.go; skips without a reachable Postgres.
//
// Acceptance control for task #1c9f527d, AC1: an issuance through the real
// handler, real SecretRepo and real ActivityLogRepo lands as a row in
// activity_log, and the row's raw JSON does not contain the value. The unit
// tests pin the entry's shape against a recorder; this proves the same holds
// once it has been through Postgres (JSONB) and the production wiring.

const materializeDBCanary = "ghp_DBAUDITCANARY_9c41e7a2f0"

func newMaterializeAuditWorkspace(t *testing.T, db *sqlx.DB) uuid.UUID {
	t.Helper()
	wsID, ownerID := uuid.New(), uuid.New()
	short := strings.ReplaceAll(wsID.String(), "-", "")[:12]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username)
		 VALUES ($1, $2, 'x', 'Materialize Audit Test Owner', $3)`,
		ownerID, "mat-audit-"+short+"@example.invalid", "mat-audit-"+short,
	)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id, settings) VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
		wsID, "mat-audit-"+short, "mat-audit-"+short, ownerID,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM activity_log WHERE workspace_id = $1`, wsID)
		_, _ = db.Exec(`DELETE FROM secrets WHERE workspace_id = $1`, wsID)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, wsID)
		_, _ = db.Exec(`DELETE FROM users WHERE id = $1`, ownerID)
	})
	return wsID
}

func materializeOverRealStack(t *testing.T, h *SecretMaterializeHandler, wsID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/internal/secrets/materialize",
		bytes.NewBufferString(`{"workspace_id":"`+wsID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	require.NoError(t, h.Materialize(e.NewContext(req, rec)))
	return rec
}

type materializeAuditRow struct {
	EntityType string `db:"entity_type"`
	ActorType  string `db:"actor_type"`
	Changes    string `db:"changes"`
}

func materializeAuditRows(t *testing.T, db *sqlx.DB, wsID uuid.UUID) []materializeAuditRow {
	t.Helper()
	var rows []materializeAuditRow
	require.NoError(t, db.Select(&rows,
		`SELECT entity_type, actor_type::text AS actor_type, changes::text AS changes
		   FROM activity_log WHERE workspace_id = $1 AND entity_type = 'secret_materialization'
		  ORDER BY created_at`, wsID))
	return rows
}

func TestMaterializeAuditDB_IssuanceIsJournaledWithoutTheValue(t *testing.T) {
	db := reservedTagTestDB(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x5a + byte(i)
	}
	encryption.ResetForTest()
	t.Setenv(encryption.EnvKey, base64.StdEncoding.EncodeToString(key))
	t.Cleanup(encryption.ResetForTest)

	wsID := newMaterializeAuditWorkspace(t, db)
	secretRepo := postgres.NewSecretRepo(db)
	created, err := secretRepo.Create(context.Background(), domain.CreateSecretInput{
		WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace,
		Name: "AUDIT_GH_TOKEN", Value: materializeDBCanary,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
	})
	require.NoError(t, err)

	h := NewSecretMaterializeHandler(
		service.NewSecretMaterializationService(secretRepo),
		service.NewActivityLogService(postgres.NewActivityLogRepo(db)),
	)

	rec := materializeOverRealStack(t, h, wsID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// Positive control: the value was really decrypted and handed out.
	require.Contains(t, rec.Body.String(), materializeDBCanary)

	rows := materializeAuditRows(t, db, wsID)
	require.Len(t, rows, 1, "one issuance, one journal row")
	assert.NotContains(t, rows[0].Changes, materializeDBCanary, "activity_log row carries the secret value")
	assert.NotContains(t, rows[0].Changes, "DBAUDITCANARY")
	assert.Contains(t, rows[0].Changes, "AUDIT_GH_TOKEN")
	assert.Contains(t, rows[0].Changes, created.ID.String(), "journal must name the version handed out")
	assert.Contains(t, rows[0].Changes, created.ValueSHA256Prefix)
	assert.Equal(t, "system", rows[0].ActorType)

	// After a rotation the next issuance names the NEW version — the journal
	// can tell who received which value, not only which name.
	rotated, err := secretRepo.Rotate(context.Background(), wsID, domain.SecretScopeWorkspace, nil, nil, "AUDIT_GH_TOKEN",
		domain.CreateSecretInput{
			WorkspaceID: wsID, Scope: domain.SecretScopeWorkspace, Name: "AUDIT_GH_TOKEN",
			Value: materializeDBCanary + "_v2", CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser,
		})
	require.NoError(t, err)
	require.NotEqual(t, created.ID, rotated.ID)

	rec = materializeOverRealStack(t, h, wsID)
	require.Equal(t, http.StatusOK, rec.Code)
	rows = materializeAuditRows(t, db, wsID)
	require.Len(t, rows, 2)
	assert.Contains(t, rows[1].Changes, rotated.ID.String())
	assert.NotContains(t, rows[1].Changes, created.ID.String())
	assert.NotContains(t, rows[1].Changes, "DBAUDITCANARY")
}
