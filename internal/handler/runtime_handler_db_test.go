package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type runtimeHTTP struct {
	t                *testing.T
	e                *echo.Echo
	owner, receiver  uuid.UUID
	ownerUser        uuid.UUID
	receiverUser     uuid.UUID
	reporter, worker uuid.UUID
	catalog          json.RawMessage
	db               *sqlx.DB
	capabilities     []string
	artifact         uuid.UUID
}

func runtimeHTTPDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://mesh:mesh@localhost:5432/mesh?sslmode=disable"
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Skipf("no reachable Postgres, skipping: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedRuntimeWorkspace(t *testing.T, db *sqlx.DB) (ws, user uuid.UUID) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	owner := &domain.User{ID: uuid.New(), Email: "rt-" + suffix + "@example.com", PasswordHash: "x", Name: "Runtime Owner", Username: "rt-" + suffix, IsActive: true}
	require.NoError(t, postgres.NewUserRepo(db).Create(context.Background(), owner))
	w := &domain.Workspace{ID: uuid.New(), Name: "rt-ws", Slug: "rt-ws-" + suffix, OwnerID: owner.ID}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(context.Background(), w))
	return w.ID, owner.ID
}

func seedRuntimeAgent(t *testing.T, db *sqlx.DB, ws uuid.UUID) (agent, grant uuid.UUID) {
	t.Helper()
	suffix := uuid.New().String()[:8]
	a := &domain.Agent{ID: uuid.New(), WorkspaceID: ws, Name: "Runtime Agent " + suffix, Slug: "rt-agent-" + suffix, AgentType: domain.AgentTypeClaudeCode, APIKeyHash: "$2a$12$hash-" + suffix, APIKeyPrefix: "pfx-" + suffix, Status: domain.AgentStatusOffline, Role: "developer"}
	require.NoError(t, postgres.NewAgentRepo(db).Create(context.Background(), a))
	grant = uuid.New()
	_, err := db.ExecContext(context.Background(), `INSERT INTO agent_workspace_grants (id, agent_id, workspace_id, role, api_key_prefix, api_key_hash, created_at) VALUES ($1,$2,$3,'member',$4,$5,NOW())`, grant, a.ID, ws, "p"+suffix, "synthetic-"+suffix)
	require.NoError(t, err)
	return a.ID, grant
}

// newRuntimeHTTP wires the real handler and repository behind echo. Caller
// identity is injected through the same context keys the auth middleware sets.
func newRuntimeHTTP(t *testing.T) *runtimeHTTP {
	t.Helper()
	db := runtimeHTTPDB(t)
	f := &runtimeHTTP{t: t, artifact: uuid.New(), db: db}
	f.owner, f.ownerUser = seedRuntimeWorkspace(t, db)
	f.receiver, f.receiverUser = seedRuntimeWorkspace(t, db)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workspaces WHERE id IN ($1,$2)`, f.owner, f.receiver)
	})
	reporter, reporterGrant := seedRuntimeAgent(t, db, f.owner)
	worker, workerGrant := seedRuntimeAgent(t, db, f.receiver)
	f.reporter, f.worker = reporter, worker
	data, err := os.ReadFile("../../docs/api/agent-runtime.example.json")
	require.NoError(t, err)
	catalog, err := domain.ParseRuntimeCatalog(data)
	require.NoError(t, err)
	c := catalog.Controllers["runner-a"]
	c.ReporterGrantID = reporterGrant
	f.capabilities = c.Capabilities
	catalog.Controllers["runner-a"] = c
	b := catalog.Bindings["worker-b"]
	b.Binding = domain.RuntimeIdentity{AgentID: worker, WorkspaceID: f.receiver, GrantID: workerGrant}
	catalog.Bindings["worker-b"] = b
	f.catalog, err = json.Marshal(catalog)
	require.NoError(t, err)

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	h := NewRuntimeHandler(postgres.NewRuntimeRepo(db))
	// Identity headers stand in for the auth middleware (tests only).
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if v := c.Request().Header.Get("X-Test-User"); v != "" {
				c.Set(mw.ContextKeyUserID, uuid.MustParse(v))
			}
			if v := c.Request().Header.Get("X-Test-Agent"); v != "" {
				c.Set(mw.ContextKeyAgentID, uuid.MustParse(v))
				c.Set(mw.ContextKeyAgentAuthWorkspaceID, uuid.MustParse(c.Request().Header.Get("X-Test-Agent-Workspace")))
			}
			return next(c)
		}
	})
	e.GET("/workspaces/:ws_id/runtime", h.Inventory)
	e.PUT("/workspaces/:ws_id/runtime", h.Save)
	e.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/report", h.Report)
	e.GET("/workspaces/:ws_id/runtime/bindings/:binding_id", h.Binding)
	e.PUT("/workspaces/:ws_id/runtime/bindings/:binding_id/admission", h.Admit)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/preview", h.Preview)
	e.POST("/workspaces/:ws_id/runtime/artifacts/:artifact_id/provenance", h.Provenance)
	f.e = e
	return f
}

func (f *runtimeHTTP) do(method, path, body string, headers map[string]string) (status int, payload map[string]any, raw string) {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func TestRuntimeHTTPEndToEndWithSecretFreeResponses(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	receiver := map[string]string{"X-Test-User": f.receiverUser.String()}
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	worker := map[string]string{"X-Test-Agent": f.worker.String(), "X-Test-Agent-Workspace": f.receiver.String()}
	base := "/workspaces/" + f.owner.String() + "/runtime"
	ownerQuery := "?resource_owner_workspace_id=" + f.owner.String()
	binding := "/workspaces/" + f.receiver.String() + "/runtime/bindings/worker-b"

	code, snapshot, _ := f.do(http.MethodGet, base, "", owner)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "direct", snapshot["mode"], "no integration keeps direct mode")

	save := `{"if_revision":0,"enabled":true,"config":` + string(f.catalog) + `}`
	code, _, body := f.do(http.MethodPut, base, save, receiver)
	require.Equal(t, http.StatusForbidden, code, body)
	code, _, body = f.do(http.MethodPut, base, save, owner)
	require.Equal(t, http.StatusOK, code, body)
	code, _, _ = f.do(http.MethodPut, base, save, owner)
	require.Equal(t, http.StatusConflict, code, "stale if_revision loses the CAS")

	code, snapshot, body = f.do(http.MethodGet, base, "", owner)
	require.Equal(t, http.StatusOK, code)
	digest, _ := snapshot["digest"].(string)
	require.NotEmpty(t, digest)
	for _, forbidden := range []string{"synthetic-", "fingerprint", "api_key"} {
		require.NotContains(t, body, forbidden)
	}

	capabilities, err := json.Marshal(f.capabilities)
	require.NoError(t, err)
	report := `{"schema_version":2,"revision":1,"digest":"` + digest + `","status":"applied","capabilities":` + string(capabilities) + `,"emergency_paused":false,"pools":{}}`
	code, _, _ = f.do(http.MethodPost, base+"/controllers/runner-a/report", report, owner)
	require.Equal(t, http.StatusForbidden, code, "a user cannot report for a controller")
	code, recorded, body := f.do(http.MethodPost, base+"/controllers/runner-a/report", report, reporter)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, recorded["launch_authorized"])

	code, view, body := f.do(http.MethodGet, binding+ownerQuery, "", worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, view["enabled"])
	code, _, _ = f.do(http.MethodGet, binding+ownerQuery, "", reporter)
	require.GreaterOrEqual(t, code, http.StatusBadRequest)
	code, _, _ = f.do(http.MethodGet, binding, "", worker)
	require.Equal(t, http.StatusBadRequest, code, "owner workspace is mandatory")

	admit := `{"if_revision":0,"enabled":true,"permitted_profiles":["preferred","reserve"]}`
	code, _, _ = f.do(http.MethodPut, binding+"/admission"+ownerQuery, admit, owner)
	require.Equal(t, http.StatusForbidden, code, "owner cannot consent for the receiver")
	code, _, body = f.do(http.MethodPut, binding+"/admission"+ownerQuery, admit, receiver)
	require.Equal(t, http.StatusOK, code, body)

	preview := `{"purpose":"new_launch","required_capabilities":[]}`
	code, result, body := f.do(http.MethodPost, binding+"/preview"+ownerQuery, preview, worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, result["launch_authorized"])

	artifact := f.artifact.String()
	attest := `{"controller_ref":"runner-a","artifact_revision":"sha-1","complete":true,"authors":[{"agent_id":"` + uuid.NewString() + `","model_developer":"developer-a","model_family":"family-a"}]}`
	provenance := base + "/artifacts/" + artifact + "/provenance"
	code, _, _ = f.do(http.MethodPost, provenance, attest, worker)
	require.GreaterOrEqual(t, code, http.StatusBadRequest, "a receiving worker cannot attest")
	code, recorded, body = f.do(http.MethodPost, provenance, attest, reporter)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, recorded["launch_authorized"])
}

// The generic integration surface must not be a side door around the versioned
// runtime API: not at the service, not at the repository, not in listings.
func TestRuntimeConnectionIsNotEditableThroughGenericIntegrations(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	code, _, body := f.do(http.MethodPut, "/workspaces/"+f.owner.String()+"/runtime", `{"if_revision":0,"enabled":true,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)

	ctx := context.Background()
	repo := postgres.NewIntegrationRepo(f.db)
	svc := service.NewIntegrationService(repo)
	connection, err := repo.GetByProvider(ctx, f.owner, domain.IntegrationProviderAgentRuntime)
	require.NoError(t, err)
	require.NotNil(t, connection)
	before := string(connection.Config)

	_, err = svc.Configure(ctx, domain.CreateIntegrationInput{WorkspaceID: f.owner, Provider: domain.IntegrationProviderAgentRuntime, Config: json.RawMessage(`{}`), IsActive: true})
	require.ErrorContains(t, err, "versioned runtime API")
	off := false
	_, err = svc.Update(ctx, connection.ID, domain.UpdateIntegrationInput{IsActive: &off})
	require.ErrorContains(t, err, "versioned runtime API")
	require.ErrorContains(t, svc.Delete(ctx, connection.ID), "versioned runtime API")
	require.ErrorContains(t, repo.Upsert(ctx, &domain.IntegrationConfig{ID: uuid.New(), WorkspaceID: f.owner, Provider: domain.IntegrationProviderAgentRuntime, Config: json.RawMessage(`{}`), IsActive: true}), "versioned runtime API")
	_, err = repo.Update(ctx, connection.ID, domain.UpdateIntegrationInput{IsActive: &off})
	require.ErrorContains(t, err, "versioned runtime API")
	require.ErrorContains(t, repo.Delete(ctx, connection.ID), "versioned runtime API")

	after, err := repo.GetByProvider(ctx, f.owner, domain.IntegrationProviderAgentRuntime)
	require.NoError(t, err)
	require.NotNil(t, after, "every refused write leaves the connection in place")
	require.Equal(t, before, string(after.Config))
	require.True(t, after.IsActive)

	masked := *after
	maskSecrets(&masked)
	require.NotContains(t, string(masked.Config), "credential_ref")
	require.NotContains(t, string(masked.Config), "runner-a")
	require.Contains(t, string(masked.Config), "versioned_api")
}
