package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// No //go:build integration tag — same convention as the *_db_test.go files
// under internal/repository/postgres.
//
// These are the end-to-end acceptance controls for task #0104878c: the
// reserved-tag guard exercised through the ACTUAL production handler
// (POST /api/v1/memories, wired to real Postgres repos), not through a mock
// service. The unit tests in internal/service/memory_service_test.go pin the
// guard's decision logic against mocks; these prove the same decision reaches
// a real HTTP caller through the real wiring — which is also the proof that
// the `remember` MCP tool path and the REST path are the same server-side
// code, since evc-mesh-mcp's RESTClient.Remember is itself an HTTP client of
// this exact endpoint (see enforceReservedTags's doc comment).

func reservedTagTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres at %s, skipping: %v", dsn, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Postgres at %s not accepting connections, skipping: %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newReservedTagTestWorkspace inserts a bare workspace row (id + required
// NOT NULL columns only) and returns its id. Bypasses WorkspaceRepo.Create so
// the test does not depend on that method's own column list.
func newReservedTagTestWorkspace(t *testing.T, db *sqlx.DB, isBench bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	ownerID := uuid.New()
	shortID := strings.ReplaceAll(id.String(), "-", "")[:12]
	_, err := db.Exec(
		`INSERT INTO users (id, email, password_hash, display_name, username)
		 VALUES ($1, $2, 'x', 'Reserved Tag Test Owner', $3)`,
		ownerID, "rtag-"+shortID+"@example.invalid", "rtag-"+shortID,
	)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO workspaces (id, name, slug, owner_id, settings, is_bench)
		 VALUES ($1, $2, $3, $4, '{}'::jsonb, $5)`,
		id, "reserved-tag-test-"+id.String(), "reserved-tag-test-"+id.String(), ownerID, isBench,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM memories WHERE workspace_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM workspaces WHERE id = $1`, id)
		_, _ = db.Exec(`DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// newReservedTagTestAgent inserts a bare agent row so memories.agent_id's FK
// (agents(id)) is satisfiable — the handler always sets AgentID from the auth
// context, and this repo's schema enforces the reference on INSERT.
func newReservedTagTestAgent(t *testing.T, db *sqlx.DB, wsID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	shortID := strings.ReplaceAll(id.String(), "-", "")[:12]
	_, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, slug, api_key_hash, api_key_prefix)
		 VALUES ($1, $2, $3, $4, 'x', 'x')`,
		id, wsID, "rtag-agent-"+shortID, "rtag-agent-"+shortID,
	)
	require.NoError(t, err)
	return id
}

func newReservedTagHandler(db *sqlx.DB) *MemoryHandler {
	memSvc := service.NewMemoryService(
		postgres.NewMemoryRepo(db),
		postgres.NewMemoryEdgesRepo(db),
		nil, // noop embedder
		service.MemoryWithWorkspaceRepo(postgres.NewWorkspaceRepo(db)),
	)
	return NewMemoryHandler(memSvc, &mockWorkspaceMemberRepo{})
}

func doRemember(t *testing.T, h *MemoryHandler, agentID, wsID uuid.UUID, key string, tags []string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"key":     key,
		"content": "reserved-tag acceptance control content",
		"scope":   "workspace",
		"tags":    tags,
	})
	require.NoError(t, err)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/memories", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	asAgent(c, agentID, wsID)
	require.NoError(t, h.Remember(c))
	return rec
}

// Acceptance criterion 3 (red, REST path): remember(tags=["lme-bench"]) under
// a normal agent key writing into a NON-bench workspace -> 400, named reason,
// nothing persisted.
func TestRemember_ReservedTag_DB_RejectedOutsideBenchWorkspace(t *testing.T) {
	db := reservedTagTestDB(t)
	wsID := newReservedTagTestWorkspace(t, db, false)
	h := newReservedTagHandler(db)

	agentID := newReservedTagTestAgent(t, db, wsID)
	rec := doRemember(t, h, agentID, wsID, "reserved-tag-red-"+uuid.NewString(), []string{"lme-bench"})

	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "lme-bench")
	assert.Contains(t, rec.Body.String(), "reserved")

	var count int
	require.NoError(t, db.Get(&count, `SELECT count(*) FROM memories WHERE workspace_id = $1`, wsID))
	assert.Equal(t, 0, count, "a refused write must not persist a row")
}

// Acceptance criterion 4 (green, REST path): the same write into the
// dedicated bench workspace succeeds.
func TestRemember_ReservedTag_DB_AllowedInBenchWorkspace(t *testing.T) {
	db := reservedTagTestDB(t)
	wsID := newReservedTagTestWorkspace(t, db, true)
	h := newReservedTagHandler(db)

	agentID := newReservedTagTestAgent(t, db, wsID)
	rec := doRemember(t, h, agentID, wsID, "reserved-tag-green-"+uuid.NewString(), []string{"lme-bench"})

	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var count int
	require.NoError(t, db.Get(&count, `SELECT count(*) FROM memories WHERE workspace_id = $1`, wsID))
	assert.Equal(t, 1, count)
}

// Acceptance criterion 6: a workspace whose is_bench flag was never set to
// true (the default-false case every pre-existing and future workspace
// starts in) must still reject — "not flagged" and "flagged false" are the
// same case here, which is the point of DEFAULT FALSE in the migration.
func TestRemember_ReservedTag_DB_FailsClosedOnDefaultFalseWorkspace(t *testing.T) {
	db := reservedTagTestDB(t)
	wsID := newReservedTagTestWorkspace(t, db, false) // explicit false, mirrors DEFAULT
	h := newReservedTagHandler(db)

	agentID := newReservedTagTestAgent(t, db, wsID)
	rec := doRemember(t, h, agentID, wsID, "reserved-tag-default-"+uuid.NewString(), []string{"lme-bench"})

	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
}

// Acceptance criterion 5: the real fleet tags `memory-bench` / `bench-*`
// (genuine agent notes about the bench, not fixtures) are unaffected outside
// the bench workspace too.
func TestRemember_ReservedTag_DB_FleetTagsUnaffected(t *testing.T) {
	db := reservedTagTestDB(t)
	wsID := newReservedTagTestWorkspace(t, db, false)
	h := newReservedTagHandler(db)
	agentID := newReservedTagTestAgent(t, db, wsID)

	for _, tag := range []string{"memory-bench", "bench-recap-2026-09-06"} {
		t.Run(tag, func(t *testing.T) {
			rec := doRemember(t, h, agentID, wsID, "reserved-tag-fleet-"+uuid.NewString(), []string{tag})
			assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
		})
	}
}
