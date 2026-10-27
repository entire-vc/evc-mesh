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
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/exttest"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
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
	e.POST("/workspaces/:ws_id/runtime/restore", h.Restore)
	e.GET("/workspaces/:ws_id/runtime/revisions", h.Revisions)
	e.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/report", h.Report)
	e.GET("/workspaces/:ws_id/runtime/controllers/:controller_ref/desired", h.Desired)
	e.POST("/workspaces/:ws_id/runtime/controllers/:controller_ref/quota-observations", h.ReportQuota)
	e.GET("/workspaces/:ws_id/runtime/controllers/:controller_ref/quota-observations", h.QuotaObservations)
	e.GET("/workspaces/:ws_id/runtime/bindings/:binding_id", h.Binding)
	e.PUT("/workspaces/:ws_id/runtime/bindings/:binding_id/admission", h.Admit)
	e.PUT("/workspaces/:ws_id/runtime/bindings/:binding_id/policy", h.SetPolicy)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/preview", h.Preview)
	e.POST("/workspaces/:ws_id/runtime/artifacts/:provenance_artifact_id/provenance", h.Provenance)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations", h.AcquireReservation)
	e.GET("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id", h.GetReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/consume", h.ConsumeReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/renew", h.RenewReservation)
	e.POST("/workspaces/:ws_id/runtime/bindings/:binding_id/reservations/:reservation_id/release", h.ReleaseReservation)
	// The Runner on/off switch is the extension endpoint, over the real tables.
	integrations := postgres.NewIntegrationRepo(db)
	reg := extensions.NewRegistry()
	require.NoError(t, reg.Register(runner.New(extensions.Deps{Conns: extensions.IntegrationConnections{Repo: integrations}, Inst: exttest.InstanceOn{}})))
	ext := NewExtensionWorkspaceHandler(service.NewExtensionWorkspaceService(reg, integrations, postgres.NewIntegrationConsentRepo(db)))
	e.POST("/workspaces/:ws_id/extensions/:ext_id/enable", ext.Enable)
	e.POST("/workspaces/:ws_id/extensions/:ext_id/disable", ext.Disable)
	f.e = e
	return f
}

// runnerSwitch turns the owner workspace's Runner on or off through the one
// canonical switch, as the owner.
func (f *runtimeHTTP) runnerSwitch(on bool) {
	f.t.Helper()
	action := "disable"
	if on {
		action = "enable"
	}
	code, _, body := f.do(http.MethodPost, "/workspaces/"+f.owner.String()+"/extensions/runner/"+action, `{"confirm":true}`, map[string]string{"X-Test-User": f.ownerUser.String()})
	require.Equal(f.t, http.StatusOK, code, body)
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

	code, revisions, body := f.do(http.MethodGet, base+"/revisions?limit=10", "", owner)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, revisions["managed"])
	list, _ := revisions["revisions"].([]any)
	require.Len(t, list, 1, "one saved revision, newest first")
	require.Equal(t, f.ownerUser.String(), list[0].(map[string]any)["author_id"])
	code, _, _ = f.do(http.MethodGet, base+"/revisions", "", receiver)
	require.Equal(t, http.StatusForbidden, code, "another workspace's admin cannot read the history")

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
	f.runnerSwitch(false)
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

// The Runner switch has one place, the extension endpoint: PUT /runtime still
// accepts enabled for compatibility but ignores it, and the switch itself is
// what the controller sees (a revision with the same catalog).
func TestRuntimeHTTPSwitchIsTheExtensionEndpointOnly(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	base := "/workspaces/" + f.owner.String() + "/runtime"

	code, saved, body := f.do(http.MethodPut, base, `{"if_revision":0,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, "enabled is optional: "+body)
	require.Equal(t, true, saved["enabled"], "a connection created by the first save starts on")

	code, saved, body = f.do(http.MethodPut, base, `{"if_revision":1,"enabled":false,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, saved["enabled"], "PUT enabled:false is ignored")
	require.Equal(t, false, saved["drain_requested"])
	code, inventory, body := f.do(http.MethodGet, base, "", owner)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, true, inventory["enabled"])
	code, desired, body := f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", reporter)
	require.Equal(t, http.StatusOK, code, body)
	require.EqualValues(t, 2, desired["revision"])
	require.Equal(t, true, desired["enabled"])
	require.Equal(t, false, desired["drain_requested"])

	f.runnerSwitch(false)
	code, drained, body := f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", reporter)
	require.Equal(t, http.StatusOK, code, body)
	require.EqualValues(t, 3, drained["revision"], "the switch is a catalog revision")
	require.Equal(t, desired["digest"], drained["digest"], "with the same catalog")
	require.Equal(t, true, drained["drain_requested"])
	code, revisions, body := f.do(http.MethodGet, base+"/revisions", "", owner)
	require.Equal(t, http.StatusOK, code, body)
	latest := revisions["revisions"].([]any)[0].(map[string]any)
	require.Equal(t, false, latest["enabled"])
	require.Equal(t, f.ownerUser.String(), latest["author_id"], "the switch is authored by the acting user")

	// A catalog edit while off keeps it off.
	code, saved, body = f.do(http.MethodPut, base, `{"if_revision":3,"enabled":true,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, false, saved["enabled"], "PUT enabled:true is ignored too")

	f.runnerSwitch(false)
	code, revisions, body = f.do(http.MethodGet, base+"/revisions", "", owner)
	require.Equal(t, http.StatusOK, code, body)
	require.Len(t, revisions["revisions"], 4, "switching to the current state appends nothing")
	f.runnerSwitch(true)
	code, desired, body = f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", reporter)
	require.Equal(t, http.StatusOK, code, body)
	require.EqualValues(t, 5, desired["revision"])
	require.Equal(t, false, desired["drain_requested"])
}

// Restore saves a stored revision's catalog, credential refs included, as the
// next revision; the client names the revision, never the config.
func TestRuntimeHTTPRestoreRevision(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	receiver := map[string]string{"X-Test-User": f.receiverUser.String()}
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	base := "/workspaces/" + f.owner.String() + "/runtime"

	code, first, body := f.do(http.MethodPut, base, `{"if_revision":0,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	catalog, err := domain.ParseRuntimeCatalog(f.catalog)
	require.NoError(t, err)
	c := catalog.Controllers["runner-a"]
	c.Host = "moved-host"
	catalog.Controllers["runner-a"] = c
	edited, err := json.Marshal(catalog)
	require.NoError(t, err)
	code, second, body := f.do(http.MethodPut, base, `{"if_revision":1,"config":`+string(edited)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	require.NotEqual(t, first["digest"], second["digest"])

	code, _, _ = f.do(http.MethodPost, base+"/restore", `{"if_revision":2,"revision":1}`, receiver)
	require.Equal(t, http.StatusForbidden, code, "another workspace's admin cannot restore")
	code, _, _ = f.do(http.MethodPost, base+"/restore", `{"if_revision":2,"revision":9}`, owner)
	require.Equal(t, http.StatusNotFound, code, "unknown revision")
	code, _, _ = f.do(http.MethodPost, base+"/restore", `{"if_revision":1,"revision":1}`, owner)
	require.Equal(t, http.StatusConflict, code, "stale if_revision loses the CAS")

	code, restored, body := f.do(http.MethodPost, base+"/restore", `{"if_revision":2,"revision":1}`, owner)
	require.Equal(t, http.StatusOK, code, body)
	require.EqualValues(t, 3, restored["revision"])
	require.Equal(t, first["digest"], restored["digest"], "revision 3 is revision 1's catalog")
	require.Equal(t, true, restored["enabled"])
	require.NotContains(t, body, "cred:")
	code, desired, body := f.do(http.MethodGet, base+"/controllers/runner-a/desired", "", reporter)
	require.Equal(t, http.StatusOK, code, body)
	require.Equal(t, first["digest"], desired["digest"])
	require.Contains(t, body, "cred:prepared-preferred", "credential refs come back from the stored revision")
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

// The UI reads the catalog with credential_ref blanked, so it cannot send the
// catalog back. The policy route edits one binding server-side instead.
func TestRuntimeHTTPBindingPolicyWithoutResendingCatalog(t *testing.T) {
	f := newRuntimeHTTP(t)
	owner := map[string]string{"X-Test-User": f.ownerUser.String()}
	receiver := map[string]string{"X-Test-User": f.receiverUser.String()}
	reporter := map[string]string{"X-Test-Agent": f.reporter.String(), "X-Test-Agent-Workspace": f.owner.String()}
	base := "/workspaces/" + f.owner.String() + "/runtime"
	query := "?resource_owner_workspace_id=" + f.owner.String()
	policyURL := "/workspaces/" + f.receiver.String() + "/runtime/bindings/worker-b/policy" + query

	code, _, body := f.do(http.MethodPut, base, `{"if_revision":0,"enabled":true,"config":`+string(f.catalog)+`}`, owner)
	require.Equal(t, http.StatusOK, code, body)

	policy := func(primary string, reserve string) string {
		return `{"primary_profiles":["` + primary + `"],"quota_edges":{},"preferred_accounts":{},"api_reserve_providers":[` + reserve + `],"max_attempts":3,"evidence_max_age_seconds":120,"qa":{"mode":"strict","quota_fallback":true}}`
	}
	good := `{"if_revision":1,"policy":` + policy("reserve", "") + `}`

	// Red controls first: each of these must be refused and change nothing.
	code, _, body = f.do(http.MethodPut, policyURL, good, receiver)
	require.Equal(t, http.StatusForbidden, code, "a non-admin of the owner workspace: "+body)
	code, _, body = f.do(http.MethodPut, policyURL, good, reporter)
	require.Equal(t, http.StatusForbidden, code, "an agent key: "+body)
	code, _, body = f.do(http.MethodPut, policyURL, `{"if_revision":1,"policy":`+policy("reserve", "")+`,"credential_ref":"x"}`, owner)
	require.Equal(t, http.StatusBadRequest, code, "unknown top-level field: "+body)
	code, _, body = f.do(http.MethodPut, policyURL, `{"if_revision":1,"policy":{"primary_profiles":["reserve"],"surprise":1}}`, owner)
	require.Equal(t, http.StatusBadRequest, code, "unknown policy field: "+body)
	code, _, body = f.do(http.MethodPut, policyURL, `{"if_revision":1,"policy":`+policy("not-permitted", "")+`}`, owner)
	require.Equal(t, http.StatusBadRequest, code, "profile outside permitted_profiles: "+body)
	code, _, body = f.do(http.MethodPut, policyURL, `{"if_revision":1,"permitted_profiles":["preferred"],"policy":`+policy("reserve", "")+`}`, owner)
	require.Equal(t, http.StatusBadRequest, code, "policy refers to a profile the new permitted list drops: "+body)
	code, _, body = f.do(http.MethodPut, policyURL, `{"if_revision":1,"policy":{"primary_profiles":["reserve"]}}`, owner)
	require.Equal(t, http.StatusBadRequest, code, "partial routing block: "+body)
	code, _, body = f.do(http.MethodPut, strings.Replace(policyURL, "worker-b", "no-such-binding", 1), good, owner)
	require.Equal(t, http.StatusNotFound, code, body)

	code, snapshot, _ := f.do(http.MethodGet, base, "", owner)
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 1, snapshot["revision"], "refused writes created no revision")
	before, _ := snapshot["digest"].(string)

	code, view, body := f.do(http.MethodPut, policyURL, good, owner)
	require.Equal(t, http.StatusOK, code, body)
	require.EqualValues(t, 2, view["revision"])
	require.NotEqual(t, before, view["digest"])
	require.NotContains(t, body, "credential_ref")
	require.NotContains(t, body, "synthetic-")
	binding := view["binding"].(map[string]any)
	require.EqualValues(t, 3, binding["policy"].(map[string]any)["max_attempts"])

	code, _, body = f.do(http.MethodPut, policyURL, good, owner)
	require.Equal(t, http.StatusConflict, code, "stale if_revision: "+body)
	require.Contains(t, body, "stale_revision")

	// GET /runtime shows the new revision and the changed policy only.
	code, snapshot, body = f.do(http.MethodGet, base, "", owner)
	require.Equal(t, http.StatusOK, code, body)
	require.EqualValues(t, 2, snapshot["revision"])
	catalog := snapshot["config"].(map[string]any)
	policyNow := catalog["bindings"].(map[string]any)["worker-b"].(map[string]any)["policy"].(map[string]any)
	require.Equal(t, []any{"reserve"}, policyNow["primary_profiles"])

	// The stored catalog differs from the original in the policy of worker-b
	// and nowhere else: credential_ref and every other entry are intact.
	var stored []byte
	require.NoError(t, f.db.Get(&stored, `SELECT r.config FROM runtime_catalog_revisions r JOIN integration_configs c ON c.id=r.integration_id WHERE c.workspace_id=$1 AND c.provider='agent_runtime' AND r.revision=2`, f.owner))
	var was, now map[string]any
	require.NoError(t, json.Unmarshal(f.catalog, &was))
	require.NoError(t, json.Unmarshal(stored, &now))
	require.Equal(t, was["accounts"], now["accounts"], "credential_ref untouched")
	for _, key := range []string{"controllers", "pools", "profiles", "schema_version"} {
		require.Equal(t, was[key], now[key], key)
	}
	wasB := was["bindings"].(map[string]any)["worker-b"].(map[string]any)
	nowB := now["bindings"].(map[string]any)["worker-b"].(map[string]any)
	require.Equal(t, wasB["binding"], nowB["binding"])
	require.Equal(t, wasB["permitted_profiles"], nowB["permitted_profiles"])
	require.NotEqual(t, wasB["policy"], nowB["policy"])
	require.Equal(t, 2, countCredentialRefs(t, stored), "both accounts keep a non-empty credential_ref")
}

func countCredentialRefs(t *testing.T, raw []byte) int {
	t.Helper()
	var c struct {
		Accounts map[string]struct {
			CredentialRef string `json:"credential_ref"`
		} `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal(raw, &c))
	n := 0
	for _, a := range c.Accounts {
		if a.CredentialRef != "" {
			n++
		}
	}
	return n
}
