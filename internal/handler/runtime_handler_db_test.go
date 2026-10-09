package handler

import (
	"context"
	"encoding/json"
	"fmt"
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
	e.GET("/workspaces/:ws_id/runtime/capacity", h.Capacity)
	e.PUT("/workspaces/:ws_id/runtime", h.Save)
	e.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/report", h.Report)
	e.GET("/workspaces/:ws_id/runtime/controllers/:controller_ref/desired", h.Desired)
	e.GET("/workspaces/:ws_id/runtime/bindings/:binding_id", h.Binding)
	e.PUT("/workspaces/:ws_id/runtime/bindings/:binding_id/admission", h.Admit)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/preview", h.Preview)
	e.POST("/workspaces/:ws_id/runtime/artifacts/:provenance_artifact_id/provenance", h.Provenance)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations", h.AcquireReservation)
	e.GET("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id", h.GetReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/consume", h.ConsumeReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/renew", h.RenewReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/release", h.ReleaseReservation)
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

// Two controllers with different reporter grants: each reads only its own
// slice with its own key, the other's key and users are refused, and an
// up-to-date reader gets 304.
func TestRuntimeHTTPControllerDesiredIsScopedToItsController(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	reporterA := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	agentB, grantB := seedRuntimeAgent(t, f.db, f.owner)
	reporterB := map[string]string{"X-Test-Agent": agentB.String(), "X-Test-Agent-Workspace": f.owner.String()}
	worker := map[string]string{"X-Test-Agent": f.worker.String(), "X-Test-Agent-Workspace": f.receiver.String()}

	catalog, err := domain.ParseRuntimeCatalog(f.catalog)
	require.NoError(t, err)
	b := catalog.Controllers["runner-a"]
	b.ReporterGrantID, b.Host = grantB, "other-host"
	catalog.Controllers["runner-b"] = b
	catalog.Pools["pool-b"] = domain.RuntimePool{Provider: "provider-b", ResourceRef: "pool-b", Aliases: []string{}, MaxConcurrency: 1}
	catalog.Accounts["acct-b"] = domain.RuntimeAccount{Provider: "provider-b", CredentialRef: "cred:only-for-b", QuotaPoolsByMode: map[string][]string{"subscription": {"pool-b"}}}
	profile := catalog.Profiles["preferred"]
	profile.ControllerRef, profile.AccountRef = "runner-b", "acct-b"
	catalog.Profiles["only-b"] = profile
	binding := catalog.Bindings["worker-b"]
	binding.PermittedProfiles = append(binding.PermittedProfiles, "only-b")
	binding.Policy.QuotaEdges["reserve"] = []string{"only-b"}
	binding.Policy.PreferredAccounts["provider-b"] = "acct-b"
	catalog.Bindings["worker-b"] = binding
	raw, err := json.Marshal(catalog)
	require.NoError(t, err)

	base := "/workspaces/" + f.owner.String() + "/runtime"
	code, _, body := f.do(http.MethodPut, base, `{"if_revision":0,"enabled":true,"config":`+string(raw)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)

	code, a, bodyA := f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", reporterA)
	require.Equal(t, http.StatusOK, code, bodyA)
	require.Equal(t, "runner-a", a["controller_ref"])
	require.Equal(t, true, a["enabled"])
	require.Equal(t, false, a["drain_requested"])
	require.Contains(t, bodyA, "cred:prepared-preferred", "own credential_ref is delivered to its controller")
	for _, foreign := range []string{"only-b", "acct-b", "pool-b", "cred:only-for-b", "provider-b", "other-host"} {
		require.NotContains(t, bodyA, foreign)
	}

	code, bView, bodyB := f.do(http.MethodGet, base+"/controllers/runner-b/desired", "", reporterB)
	require.Equal(t, http.StatusOK, code, bodyB)
	require.Equal(t, "runner-b", bView["controller_ref"])
	require.Contains(t, bodyB, "cred:only-for-b")
	for _, foreign := range []string{"cred:prepared", "reserve-window", "shared-window", `"preferred"`, `"reserve"`, "prepared-host"} {
		require.NotContains(t, bodyB, foreign)
	}

	code, _, _ = f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", reporterB)
	require.Equal(t, http.StatusForbidden, code, "another controller's key is refused")
	code, _, _ = f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", worker)
	require.Equal(t, http.StatusForbidden, code, "a receiving worker key is refused")
	code, _, _ = f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", owner)
	require.Equal(t, http.StatusForbidden, code, "a human admin is refused")
	code, _, _ = f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", nil)
	require.Equal(t, http.StatusForbidden, code, "anonymous is refused")
	code, _, _ = f.do(http.MethodGet, base+"/controllers/nope/desired", "", reporterA)
	require.Equal(t, http.StatusNotFound, code, "unknown ref")

	req := httptest.NewRequest(http.MethodGet, base+"/controllers/runner-a/desired", http.NoBody)
	req.Header.Set("X-Test-Agent", f.reporter.String())
	req.Header.Set("X-Test-Agent-Workspace", f.owner.String())
	rec := httptest.NewRecorder()
	f.e.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)
	hdr := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String(), "If-None-Match": etag}
	code, _, body = f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", hdr)
	require.Equal(t, http.StatusNotModified, code)
	require.Empty(t, body)
	hdr["If-None-Match"] = `"stale"`
	code, _, _ = f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", hdr)
	require.Equal(t, http.StatusOK, code, "a different digest gets the body")

	// Same catalog, only enabled flips: the digest is unchanged but the body is
	// not, so the old tag must not answer 304.
	code, _, body = f.do(http.MethodPut, base, `{"if_revision":1,"enabled":false,"config":`+string(raw)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	hdr["If-None-Match"] = etag
	code, flipped, body := f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", hdr)
	require.Equal(t, http.StatusOK, code, "enabled-only change must not be 304: "+body)
	require.Equal(t, a["digest"], flipped["digest"], "digest alone cannot see this change")
	require.Equal(t, true, flipped["drain_requested"])

	// Revoking the reporter grant makes the controller's old key stale.
	_, err = f.db.ExecContext(context.Background(), `UPDATE agent_workspace_grants SET revoked_at=now() WHERE id=$1`, grantB)
	require.NoError(t, err)
	code, _, _ = f.do(http.MethodGet, base+"/controllers/runner-b/desired", "", reporterB)
	require.Equal(t, http.StatusForbidden, code, "revoked reporter grant")

	// A new save moves the revision and digest; 304 no longer matches.
	code, _, body = f.do(http.MethodPut, base, `{"if_revision":2,"enabled":false,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	code, a2, body := f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", hdr2(f, etag))
	require.Equal(t, http.StatusOK, code, "same catalog but new revision and enabled=false must not be 304: "+body)
	require.EqualValues(t, 3, a2["revision"])
	require.Equal(t, true, a2["drain_requested"])
}

func hdr2(f *runtimeHTTP, etag string) map[string]string {
	return map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String(), "If-None-Match": etag}
}

// Wire contract of execution admission: status codes, strict bodies and
// secret-free responses through the real handler, error handler and database.
func TestRuntimeHTTPReservationLifecycle(t *testing.T) {
	f := newRuntimeHTTP(t)
	ctx := context.Background()
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	receiver := map[string]string{"X-Test-User": f.receiverUser.String()}
	worker := map[string]string{"X-Test-Agent": f.worker.String(), "X-Test-Agent-Workspace": f.receiver.String()}
	ownerQuery := "?resource_owner_workspace_id=" + f.owner.String()
	binding := "/workspaces/" + f.receiver.String() + "/runtime/bindings/worker-b"

	code, _, body := f.do(http.MethodPut, "/workspaces/"+f.owner.String()+"/runtime", `{"if_revision":0,"enabled":true,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	code, _, body = f.do(http.MethodPut, binding+"/admission"+ownerQuery, `{"if_revision":0,"enabled":true,"permitted_profiles":["preferred","reserve"]}`, receiver)
	require.Equal(t, http.StatusOK, code, body)
	code, view, body := f.do(http.MethodGet, binding+ownerQuery, "", worker)
	require.Equal(t, http.StatusOK, code, body)

	// Admission needs a current controller report for the applied revision.
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	capabilities, err := json.Marshal(f.capabilities)
	require.NoError(t, err)
	report := fmt.Sprintf(`{"schema_version":2,"revision":%v,"digest":%q,"status":"applied","capabilities":%s,"emergency_paused":false,"pools":{}}`, view["revision"], view["digest"], capabilities)
	code, _, body = f.do(http.MethodPost, "/workspaces/"+f.owner.String()+"/runtime/controllers/runner-a/report", report, reporter)
	require.Equal(t, http.StatusOK, code, body)

	project := &domain.Project{ID: uuid.New(), WorkspaceID: f.receiver, Name: "Runtime", Slug: "rt-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(f.db).Create(ctx, project))
	status := &domain.TaskStatus{ID: uuid.New(), ProjectID: project.ID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Color: "#000000"}
	require.NoError(t, postgres.NewTaskStatusRepo(f.db).Create(ctx, status))
	task := &domain.Task{ID: uuid.New(), ProjectID: project.ID, StatusID: status.ID, Title: "rt", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser}
	require.NoError(t, postgres.NewTaskRepo(f.db).Create(ctx, task, nil))
	request := uuid.New()
	_, err = f.db.Exec(`UPDATE tasks SET checked_out_by=$2,checkout_token=$3,checkout_expires=now()+interval '1 hour',checkout_generation=1,checkout_request_id=$4 WHERE id=$1`, task.ID, f.worker, uuid.New(), request)
	require.NoError(t, err)

	catalog, err := domain.ParseRuntimeCatalog(f.catalog)
	require.NoError(t, err)
	pools, ok := catalog.RuntimeProfilePools(f.owner, "preferred")
	require.True(t, ok)
	admission := view["admission"].(map[string]any)
	acquire := fmt.Sprintf(`{"idempotency_key":"attempt-1","profile_id":"preferred","task_id":%q,"checkout_generation":1,"checkout_request_id":%q,"worker_ref":"slot-1","expected_catalog_revision":%v,"expected_catalog_digest":%q,"expected_admission_revision":%v,"expected_profile_revision":"rev-1","expected_pool_set_digest":%q,"ttl_seconds":60}`,
		task.ID, request, view["revision"], view["digest"], admission["revision"], domain.RuntimePoolSetDigest(pools))
	reservations := binding + "/reservations" + ownerQuery

	code, _, body = f.do(http.MethodPost, reservations, acquire, worker)
	require.Equal(t, http.StatusLocked, code, "max_concurrent_tasks unset: "+body)
	require.Contains(t, body, "identity_cap_unset")
	_, err = f.db.Exec(`UPDATE agents SET max_concurrent_tasks=1 WHERE id=$1`, f.worker)
	require.NoError(t, err)

	code, _, _ = f.do(http.MethodPost, reservations, strings.Replace(acquire, `"ttl_seconds":60`, `"ttl_seconds":60,"pools":["chosen"]`, 1), worker)
	require.Equal(t, http.StatusBadRequest, code, "client cannot name pools")
	code, _, _ = f.do(http.MethodPost, reservations, acquire, receiver)
	require.Equal(t, http.StatusForbidden, code, "a human admin cannot acquire for the agent")
	code, created, body := f.do(http.MethodPost, reservations, acquire, worker)
	require.Equal(t, http.StatusCreated, code, body)
	for _, forbidden := range []string{"fingerprint", "idempotency", "attempt-1", "synthetic-", "cred:"} {
		require.NotContains(t, body, forbidden)
	}
	code, replay, _ := f.do(http.MethodPost, reservations, acquire, worker)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, created["reservation_id"], replay["reservation_id"])
	id := created["reservation_id"].(string)
	fence := created["fence"]
	item := binding + "/reservations/" + id

	consume := fmt.Sprintf(`{"fence":%v,"checkout_generation":1,"run_lease_seconds":600}`, fence)
	code, consumed, body := f.do(http.MethodPost, item+"/consume"+ownerQuery, consume, worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "consumed", consumed["state"])

	renew := fmt.Sprintf(`{"fence":%v,"checkout_request_id":%q,"checkout_generation":1,"run_lease_seconds":900}`, fence, request)
	code, renewed, body := f.do(http.MethodPost, item+"/renew"+ownerQuery, renew, worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "consumed", renewed["state"])
	code, _, body = f.do(http.MethodPost, item+"/renew"+ownerQuery, `{"fence":1}`, worker)
	require.Equal(t, http.StatusBadRequest, code, "renew body is strict: "+body)
	code, _, body = f.do(http.MethodPost, item+"/renew"+ownerQuery, strings.Replace(renew, `"checkout_generation":1`, `"checkout_generation":2`, 1), worker)
	require.Equal(t, http.StatusConflict, code, "another generation is not the writer: "+body)

	unproven := fmt.Sprintf(`{"fence":%v,"checkout_request_id":%q,"checkout_generation":1,"stopped":false,"proof":{"kind":"no_child","evidence_ref":""}}`, fence, request)
	code, _, body = f.do(http.MethodPost, item+"/release"+ownerQuery, unproven, worker)
	require.Equal(t, http.StatusConflict, code, body)
	require.Contains(t, body, "release_unproven")
	code, got, _ := f.do(http.MethodGet, item+ownerQuery, "", receiver)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "consumed", got["state"], "unproven release keeps occupancy")

	proven := strings.Replace(unproven, `"stopped":false`, `"stopped":true`, 1)
	code, released, body := f.do(http.MethodPost, item+"/release"+ownerQuery, proven, worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "released", released["state"])
	code, again, _ := f.do(http.MethodPost, item+"/release"+ownerQuery, proven, worker)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, released["release_receipt"], again["release_receipt"])
	code, _, body = f.do(http.MethodPost, item+"/consume"+ownerQuery, consume, worker)
	require.Equal(t, http.StatusGone, code, "a late consume replay after release must never authorize a start")
	require.Contains(t, body, "reservation_released")
}

func TestRuntimeHTTPCapacityProjection(t *testing.T) {
	f := newRuntimeHTTP(t)
	worker := map[string]string{"X-Test-Agent": f.worker.String(), "X-Test-Agent-Workspace": f.receiver.String()}
	_, err := f.db.Exec(`UPDATE agents SET max_concurrent_tasks=2 WHERE id=$1`, f.worker)
	require.NoError(t, err)
	path := "/workspaces/" + f.receiver.String() + "/runtime/capacity"
	code, got, body := f.do(http.MethodGet, path+"?agent_id="+f.worker.String(), "", worker)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, "durable_state", got["source"])
	require.NotEmpty(t, got["observed_at"])
	agents := got["agents"].([]any)
	require.Len(t, agents, 1)
	agent := agents[0].(map[string]any)
	for field, want := range map[string]any{"configured": 2.0, "effective": 2.0, "reserved": 0.0, "running": 0.0, "occupied": 0.0, "ready": 2.0, "reason": "available"} {
		require.Equal(t, want, agent[field], field)
	}
	require.NotContains(t, body, "task_id")
	code, _, _ = f.do(http.MethodGet, path+"?agent_id=not-a-uuid", "", worker)
	require.Equal(t, http.StatusBadRequest, code)
	foreign := map[string]string{"X-Test-Agent": f.worker.String(), "X-Test-Agent-Workspace": f.owner.String()}
	code, _, _ = f.do(http.MethodGet, path, "", foreign)
	require.Equal(t, http.StatusForbidden, code, "a key for another workspace cannot read this projection")
}
