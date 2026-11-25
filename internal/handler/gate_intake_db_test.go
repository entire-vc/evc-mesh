package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// DB integration of gate intake (contract CH→Mesh gate-create/v1,
// docs/gate-intake.md) through the real handler, service and repositories
// against a migrated Postgres. The router is the production arrangement:
// GateIntakeGuard as an Echo-level middleware, the intake route on /api/v1
// outside the protected group, and the protected group (DualAuth and the
// tenant guards) carrying real task routes plus stand-ins for comment, admin,
// merge and publish routes that count how often they are reached.
//
// Skips without a reachable Postgres (same contract as the other *_db_test
// files); CI and `make test-pkg` provide one.

// crashAfterCommitTasks fails Create after the inner Create committed: the
// process "dies" between the commit and the answer.
type crashAfterCommitTasks struct {
	service.TaskService
	crash atomic.Bool
}

func (c *crashAfterCommitTasks) Create(ctx context.Context, task *domain.Task) error {
	if err := c.TaskService.Create(ctx, task); err != nil {
		return err
	}
	if c.crash.Load() {
		return errors.New("simulated crash after commit")
	}
	return nil
}

// dropResponseTransport performs the request fully (the server commits and
// answers) and then loses the answer: the client sees a transport error.
type dropResponseTransport struct{ inner http.RoundTripper }

func (d dropResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := d.inner.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return nil, errors.New("simulated: connection reset before the response arrived")
}

type gateIntakeEnv struct {
	db          *sqlx.DB
	server      *httptest.Server
	client      *http.Client
	repo        *postgres.GateIntakeRepo
	tasks       *crashAfterCommitTasks
	wsID        uuid.UUID
	projectID   uuid.UUID
	otherProjID uuid.UUID
	statusID    uuid.UUID
	assigneeID  uuid.UUID
	operatorID  uuid.UUID
	principal   *domain.GateIntakePrincipal
	name        string
	token       string
	agentKey    string
	jwt         string
	stubHits    atomic.Int64
}

func newGateIntakeEnv(t *testing.T) *gateIntakeEnv {
	t.Helper()
	db := oauthE2ETestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	workspaceMemberRepo := postgres.NewWorkspaceMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	activityLogRepo := postgres.NewActivityLogRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	projectMemberRepo := postgres.NewProjectMemberRepo(db)
	statusRepo := postgres.NewTaskStatusRepo(db)

	agentSvc := service.NewAgentService(agentRepo, activityLogRepo, workspaceRepo, userRepo)
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, workspaceMemberRepo, "gate-intake-db-test-jwt-secret")
	// The assignee options of the production wiring (cmd/api/wire_core_services.go):
	// without them the tenancy guard refuses every assignment.
	inner := service.NewTaskService(postgres.NewTaskRepo(db), statusRepo, nil, activityLogRepo,
		service.WithProjectRepo(projectRepo),
		service.WithProjectMemberRepoTask(projectMemberRepo),
		service.WithTaskAgentRepo(agentRepo),
		service.WithUserRepoTask(userRepo),
		service.WithWorkspaceMembershipReader(postgres.NewWorkspaceMembershipReader(db)),
	)
	tasks := &crashAfterCommitTasks{TaskService: inner}
	repo := postgres.NewGateIntakeRepo(db)
	svc := service.NewGateIntakeService(repo, tasks, statusRepo)

	user, tokens, err := authSvc.Register(ctx, fmt.Sprintf("gate-intake-%s@e2e.example.com", suffix), "Correct-Horse-Battery-Staple-1", "gate-intake-"+suffix)
	require.NoError(t, err)
	wss, err := workspaceRepo.ListForUser(ctx, user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, wss)
	wsID := wss[0].ID

	mkProject := func(slug string) (uuid.UUID, uuid.UUID) {
		p := &domain.Project{ID: uuid.New(), WorkspaceID: wsID, Name: slug, Slug: slug + "-" + suffix, DefaultAssigneeType: domain.DefaultAssigneeNone}
		require.NoError(t, projectRepo.Create(ctx, p))
		st := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Color: "#00FF00",
			Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true}
		require.NoError(t, statusRepo.Create(ctx, st))
		require.NoError(t, statusRepo.Create(ctx, &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Done", Slug: "done",
			Color: "#0000FF", Position: 5, Category: domain.StatusCategoryDone}))
		require.NoError(t, projectMemberRepo.Create(ctx, &domain.ProjectMember{ID: uuid.New(), ProjectID: p.ID, UserID: &user.ID,
			Role: "member", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}))
		return p.ID, st.ID
	}
	projectID, statusID := mkProject("gate-intake")
	otherProjID, _ := mkProject("gate-intake-other")

	assignee, err := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: wsID, Name: "gate-assignee-" + suffix, AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)
	caller, err := agentSvc.Register(ctx, service.RegisterAgentInput{WorkspaceID: wsID, Name: "gate-caller-" + suffix, AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)
	for _, pid := range []uuid.UUID{projectID, otherProjID} {
		for _, aid := range []uuid.UUID{assignee.Agent.ID, caller.Agent.ID} {
			a := aid
			require.NoError(t, projectMemberRepo.Create(ctx, &domain.ProjectMember{ID: uuid.New(), ProjectID: pid, AgentID: &a,
				Role: "member", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}))
		}
	}

	env := &gateIntakeEnv{db: db, repo: repo, tasks: tasks, wsID: wsID, projectID: projectID, otherProjID: otherProjID,
		statusID: statusID, assigneeID: assignee.Agent.ID, operatorID: uuid.New(), name: "gi-test-" + suffix,
		agentKey: caller.APIKey, jwt: tokens.AccessToken}
	env.token = env.issue(t)
	env.principal, err = repo.PrincipalByName(ctx, env.name)
	require.NoError(t, err)

	taskHandler := NewTaskHandler(inner)
	stub := func(c echo.Context) error {
		env.stubHits.Add(1)
		return c.JSON(http.StatusOK, map[string]string{"reached": c.Path()})
	}
	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	e.Use(mw.GateIntakeGuard(svc))
	v1 := e.Group("/api/v1")
	v1.POST("/gate-intake", NewGateIntakeHandler(svc).Create, mw.RequireGateIntakePrincipal())
	api := v1.Group("")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	api.Use(mw.RequireTaskProjectMember(db))
	rbac := func(p mw.Permission) echo.MiddlewareFunc { return mw.RequirePermission(p, workspaceMemberRepo) }
	api.GET("/projects/:proj_id/tasks", taskHandler.List, mw.RequireProjectMember(db))
	api.POST("/projects/:proj_id/tasks", taskHandler.Create, mw.RequireProjectMember(db), rbac(mw.PermCreateTask))
	api.GET("/tasks/:task_id", taskHandler.GetByID)
	api.PATCH("/tasks/:task_id", taskHandler.Update, rbac(mw.PermUpdateTask))
	api.DELETE("/tasks/:task_id", taskHandler.Delete, rbac(mw.PermDeleteTask))
	api.POST("/tasks/:task_id/move", taskHandler.MoveTask, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/assign", taskHandler.AssignTask, rbac(mw.PermUpdateTask))
	api.POST("/tasks/:task_id/comments", stub)
	api.PATCH("/workspaces/:ws_id/members/:member_id", stub)
	api.POST("/admin/extensions", stub)
	api.POST("/tasks/:task_id/merge", stub)
	api.POST("/tasks/:task_id/publish", stub)

	env.server = httptest.NewServer(e)
	env.client = &http.Client{Timeout: 30 * time.Second}

	t.Cleanup(func() {
		env.server.Close()
		_, _ = db.ExecContext(ctx, "DELETE FROM gate_intake_receipts WHERE principal_id = $1", env.principal.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM gate_intake_audit WHERE principal_id = $1", env.principal.ID)
		_, _ = db.ExecContext(ctx, "DELETE FROM gate_intake_principals WHERE id = $1", env.principal.ID)
		for _, pid := range []uuid.UUID{projectID, otherProjID} {
			_, _ = db.ExecContext(ctx, "DELETE FROM tasks WHERE project_id = $1", pid)
			_, _ = db.ExecContext(ctx, "DELETE FROM project_members WHERE project_id = $1", pid)
			_, _ = db.ExecContext(ctx, "DELETE FROM task_statuses WHERE project_id = $1", pid)
			_, _ = db.ExecContext(ctx, "DELETE FROM projects WHERE id = $1", pid)
		}
		_, _ = db.ExecContext(ctx, "DELETE FROM activity_log WHERE workspace_id = $1", wsID)
		_, _ = db.ExecContext(ctx, "DELETE FROM agents WHERE id = ANY($1)", pq.Array([]uuid.UUID{assignee.Agent.ID, caller.Agent.ID}))
	})
	return env
}

func (env *gateIntakeEnv) binding() postgres.GateIntakeBinding {
	return postgres.GateIntakeBinding{Name: env.name, WorkspaceID: env.wsID, ProjectID: env.projectID,
		AssigneeAgentID: env.assigneeID, Labels: []string{"kind:ops", "spark-gate"}, Priority: domain.PriorityHigh,
		Schema: domain.GateIntakeSchemaContentHub}
}

// issue issues (or re-arms) the env's principal and returns its token.
func (env *gateIntakeEnv) issue(t *testing.T) string {
	t.Helper()
	tok, err := domain.NewGateIntakeToken()
	require.NoError(t, err)
	_, _, err = env.repo.Issue(context.Background(), env.operatorID, env.binding(), domain.HashGateIntakeToken(tok), domain.GateIntakeTokenDisplay(tok))
	require.NoError(t, err)
	return tok
}

func (env *gateIntakeEnv) rotate(t *testing.T, grace time.Duration) string {
	t.Helper()
	tok, err := domain.NewGateIntakeToken()
	require.NoError(t, err)
	_, err = env.repo.Rotate(context.Background(), env.operatorID, env.name, domain.HashGateIntakeToken(tok), domain.GateIntakeTokenDisplay(tok), grace)
	require.NoError(t, err)
	return tok
}

func gateBody(approval uuid.UUID, draft int, extra map[string]any) []byte {
	m := map[string]any{
		"schema":           domain.GateIntakeSchemaContentHub,
		"approval_id":      approval.String(),
		"task_id":          4711,
		"draft_version":    draft,
		"payload_sha256":   strings.Repeat("a", 64),
		"manifest_sha256":  strings.Repeat("b", 64),
		"spark_project_id": 12,
		"mr_iid":           128,
		"head_sha":         strings.Repeat("c", 40),
		"mr_url":           "https://gitlab.example.com/entire-vc/spark/-/merge_requests/128",
		"article_url":      "https://spark.entire.vc/blog/gate-intake-test",
	}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

type gateResult struct {
	status  int
	header  http.Header
	raw     []byte
	resp    domain.GateIntakeResponse
	errBody map[string]any
}

func (env *gateIntakeEnv) do(t *testing.T, client *http.Client, method, path, authz string, body []byte, hdr map[string]string) (*gateResult, error) {
	t.Helper()
	req, err := http.NewRequest(method, env.server.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := &gateResult{status: resp.StatusCode, header: resp.Header, raw: raw}
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		_ = json.Unmarshal(raw, &out.resp)
	} else {
		_ = json.Unmarshal(raw, &out.errBody)
	}
	return out, nil
}

func (env *gateIntakeEnv) post(t *testing.T, token string, body []byte) *gateResult {
	t.Helper()
	r, err := env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", "Bearer "+token, body, nil)
	require.NoError(t, err)
	return r
}

func (env *gateIntakeEnv) countTasks(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM tasks WHERE project_id = ANY($1)`,
		pq.Array([]uuid.UUID{env.projectID, env.otherProjID})))
	return n
}

func (env *gateIntakeEnv) countCreated(t *testing.T, taskID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, env.db.Get(&n, `SELECT count(*) FROM activity_log WHERE entity_id = $1 AND action = 'task.created'`, taskID))
	return n
}

// tasksSnapshot is every column of the workspace's task rows that a write
// could change, as text, for a byte-for-byte before/after comparison.
func (env *gateIntakeEnv) tasksSnapshot(t *testing.T) string {
	t.Helper()
	var rows []string
	require.NoError(t, env.db.Select(&rows, `
		SELECT concat_ws('|', id, project_id, status_id, title, description, assignee_id, assignee_type, priority,
		                 labels::text, version, updated_at, deleted_at)
		FROM tasks WHERE project_id = ANY($1) ORDER BY id`, pq.Array([]uuid.UUID{env.projectID, env.otherProjID})))
	var extra int
	require.NoError(t, env.db.Get(&extra, `SELECT count(*) FROM gate_intake_receipts WHERE principal_id = $1`, env.principal.ID))
	return fmt.Sprintf("%s\nreceipts=%d", strings.Join(rows, "\n"), extra)
}

// --- AC2: one approval → exactly one card ---

func TestGateIntakeDB_SameApprovalTwice_OneTask(t *testing.T) {
	env := newGateIntakeEnv(t)
	approval := uuid.New()
	first := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusCreated, first.status, string(first.raw))
	assert.False(t, first.resp.Replayed)
	assert.Empty(t, first.header.Get("Idempotent-Replayed"))
	assert.Equal(t, domain.GateIntakeResponseSchema, first.resp.Schema)
	assert.Equal(t, approval, first.resp.ApprovalID)
	assert.Equal(t, env.projectID, first.resp.ProjectID)
	assert.Len(t, first.resp.RequestSHA256, 64)

	second := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusOK, second.status, string(second.raw))
	assert.Equal(t, "true", second.header.Get("Idempotent-Replayed"))
	assert.True(t, second.resp.Replayed)
	assert.Equal(t, first.resp.TaskID, second.resp.TaskID)
	assert.Equal(t, first.resp.RequestSHA256, second.resp.RequestSHA256)
	assert.True(t, first.resp.CreatedAt.Equal(second.resp.CreatedAt))
	assert.Equal(t, 1, env.countTasks(t))
	assert.Equal(t, 1, env.countCreated(t, first.resp.TaskID))

	// The card itself, re-read from the database: the principal's binding.
	var card struct {
		ProjectID     uuid.UUID      `db:"project_id"`
		StatusID      uuid.UUID      `db:"status_id"`
		Title         string         `db:"title"`
		Description   string         `db:"description"`
		AssigneeID    uuid.UUID      `db:"assignee_id"`
		AssigneeType  string         `db:"assignee_type"`
		Priority      string         `db:"priority"`
		Labels        pq.StringArray `db:"labels"`
		CreatedBy     uuid.UUID      `db:"created_by"`
		CreatedByType string         `db:"created_by_type"`
	}
	require.NoError(t, env.db.Get(&card, `SELECT project_id, status_id, title, description, assignee_id, assignee_type,
		priority, labels, created_by, created_by_type FROM tasks WHERE id = $1`, first.resp.TaskID))
	assert.Equal(t, env.projectID, card.ProjectID)
	assert.Equal(t, env.statusID, card.StatusID, "the project's default status")
	assert.Equal(t, fmt.Sprintf("Spark gate: MR !128 · approval %s · v3", approval.String()[:8]), card.Title)
	assert.Contains(t, card.Description, "approval_id: `"+approval.String()+"`")
	assert.Equal(t, env.assigneeID, card.AssigneeID)
	assert.Equal(t, "agent", card.AssigneeType)
	assert.Equal(t, "high", card.Priority)
	assert.Equal(t, []string{"kind:ops", "spark-gate"}, []string(card.Labels), "exactly the bound labels")
	assert.Equal(t, env.principal.ID, card.CreatedBy)
	assert.Equal(t, "system", card.CreatedByType)
	var ref struct {
		Ref  string `db:"ref"`
		Hash string `db:"hash"`
	}
	require.NoError(t, env.db.Get(&ref, `SELECT ref, hash FROM task_external_refs WHERE task_id = $1`, first.resp.TaskID))
	assert.Equal(t, "contenthub-approval:"+approval.String(), ref.Ref)
	assert.Equal(t, first.resp.RequestSHA256, ref.Hash)
}

func TestGateIntakeDB_Concurrent_N_OneTask(t *testing.T) {
	env := newGateIntakeEnv(t)
	approval := uuid.New()
	const n = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*gateResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", "Bearer "+env.token, gateBody(approval, 3, nil), nil)
		}(i)
	}
	close(start)
	wg.Wait()
	created, replayed := 0, 0
	ids := map[uuid.UUID]bool{}
	for i, r := range results {
		require.NoError(t, errs[i])
		switch r.status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayed++
		default:
			t.Fatalf("request %d: status %d (no 409/5xx is allowed for identical deliveries): %s", i, r.status, r.raw)
		}
		ids[r.resp.TaskID] = true
	}
	assert.Equal(t, 1, created, "exactly one delivery creates")
	assert.Equal(t, n-1, replayed)
	require.Len(t, ids, 1, "every delivery names the same card")
	for id := range ids {
		assert.Equal(t, 1, env.countCreated(t, id), "one task.created row")
	}
	assert.Equal(t, 1, env.countTasks(t))
}

func TestGateIntakeDB_LostResponse_RetryReplays(t *testing.T) {
	env := newGateIntakeEnv(t)
	approval := uuid.New()
	lossy := &http.Client{Timeout: 30 * time.Second, Transport: dropResponseTransport{inner: http.DefaultTransport}}
	_, err := env.do(t, lossy, http.MethodPost, "/api/v1/gate-intake", "Bearer "+env.token, gateBody(approval, 3, nil), nil)
	require.Error(t, err, "the client never saw the answer")
	assert.Equal(t, 1, env.countTasks(t), "the server committed the card")

	retry := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusOK, retry.status, string(retry.raw))
	assert.True(t, retry.resp.Replayed)
	var stored uuid.UUID
	require.NoError(t, env.db.Get(&stored, `SELECT task_id FROM gate_intake_receipts WHERE principal_id = $1 AND approval_id = $2`, env.principal.ID, approval))
	assert.Equal(t, stored, retry.resp.TaskID)
	assert.Equal(t, 1, env.countTasks(t))
}

func TestGateIntakeDB_CommitBeforeCache_CrashReplay(t *testing.T) {
	env := newGateIntakeEnv(t)
	approval := uuid.New()
	env.tasks.crash.Store(true)
	crashed := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusInternalServerError, crashed.status, "the server failed after its commit: %s", crashed.raw)
	env.tasks.crash.Store(false)
	assert.Equal(t, 1, env.countTasks(t), "the card and its receipt were committed before the failure")

	retry := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusOK, retry.status, string(retry.raw))
	assert.True(t, retry.resp.Replayed)
	assert.Equal(t, 1, env.countTasks(t))
	assert.Equal(t, 1, env.countCreated(t, retry.resp.TaskID))
}

func TestGateIntakeDB_NoResponseCacheExpiry(t *testing.T) {
	env := newGateIntakeEnv(t)
	approval := uuid.New()
	first := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusCreated, first.status, string(first.raw))

	// The route keeps nothing in the 24 h answer store …
	var cached int
	require.NoError(t, env.db.Get(&cached, `SELECT count(*) FROM idempotency_keys WHERE idem_key = $1`, approval.String()))
	assert.Zero(t, cached, "gate intake must not depend on the expiring answer store")
	// … so purging that store and moving the receipt a year into the past
	// changes nothing.
	_, err := env.db.Exec(`DELETE FROM idempotency_keys WHERE idem_key = $1`, approval.String())
	require.NoError(t, err)
	_, err = env.db.Exec(`UPDATE gate_intake_receipts SET created_at = now() - interval '400 days' WHERE principal_id = $1 AND approval_id = $2`, env.principal.ID, approval)
	require.NoError(t, err)
	later := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusOK, later.status, string(later.raw))
	assert.Equal(t, first.resp.TaskID, later.resp.TaskID)

	// After a key rotation (old key gone at once) the namespace is the same.
	newTok := env.rotate(t, 0)
	rotated := env.post(t, newTok, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusOK, rotated.status, string(rotated.raw))
	assert.Equal(t, first.resp.TaskID, rotated.resp.TaskID)

	// The receipt outlives the card: a deleted card is still that approval's card.
	_, err = env.db.Exec(`UPDATE tasks SET deleted_at = now() WHERE id = $1`, first.resp.TaskID)
	require.NoError(t, err)
	afterDelete := env.post(t, newTok, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusOK, afterDelete.status, string(afterDelete.raw))
	assert.Equal(t, first.resp.TaskID, afterDelete.resp.TaskID)
	assert.Equal(t, 1, env.countTasks(t))
}

func TestGateIntakeDB_DifferentPayload_409_NoNewTask(t *testing.T) {
	env := newGateIntakeEnv(t)
	approval := uuid.New()
	first := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusCreated, first.status, string(first.raw))
	before := env.tasksSnapshot(t)

	conflict := env.post(t, env.token, gateBody(approval, 4, nil))
	require.Equal(t, http.StatusConflict, conflict.status, string(conflict.raw))
	assert.Equal(t, domain.GateIntakeCodePayloadConflict, conflict.errBody["error_code"])
	ctxm, _ := conflict.errBody["context"].(map[string]any)
	require.NotNil(t, ctxm, string(conflict.raw))
	assert.Equal(t, approval.String(), ctxm["approval_id"])
	assert.Equal(t, first.resp.TaskID.String(), ctxm["task_id"])
	assert.Equal(t, first.resp.RequestSHA256, ctxm["existing_request_sha256"])
	assert.NotEqual(t, first.resp.RequestSHA256, ctxm["request_sha256"])
	assert.Equal(t, before, env.tasksSnapshot(t), "a conflict writes nothing")

	// The stored receipt still answers the original payload.
	again := env.post(t, env.token, gateBody(approval, 3, nil))
	require.Equal(t, http.StatusOK, again.status)
	assert.Equal(t, first.resp.TaskID, again.resp.TaskID)

	// A different approval is a different card.
	other := env.post(t, env.token, gateBody(uuid.New(), 3, nil))
	require.Equal(t, http.StatusCreated, other.status)
	assert.NotEqual(t, first.resp.TaskID, other.resp.TaskID)
	assert.Equal(t, 2, env.countTasks(t))
}

// --- AC3: the credential is checked by the server ---

func TestGateIntakeDB_Auth_401(t *testing.T) {
	env := newGateIntakeEnv(t)
	body := gateBody(uuid.New(), 3, nil)
	unknown, _ := domain.NewGateIntakeToken()

	// Rotated with a grace: the old token still works inside it …
	oldTok := env.token
	graceTok := env.rotate(t, time.Hour)
	inGrace := env.post(t, oldTok, gateBody(uuid.New(), 3, nil))
	require.Equal(t, http.StatusCreated, inGrace.status, "previous token inside its grace: %s", inGrace.raw)
	// … and not after it.
	_, err := env.db.Exec(`UPDATE gate_intake_principals SET previous_valid_until = now() - interval '1 second' WHERE id = $1`, env.principal.ID)
	require.NoError(t, err)

	cases := []struct{ name, authz string }{
		{"no header", ""},
		{"malformed mgi", "Bearer mgi_short"},
		{"unknown mgi", "Bearer " + unknown},
		{"rotated out after grace", "Bearer " + oldTok},
		{"user jwt", "Bearer " + env.jwt},
	}
	var first []byte
	tasksBefore := env.countTasks(t)
	for _, tc := range cases {
		r, derr := env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", tc.authz, body, nil)
		require.NoError(t, derr)
		assert.Equal(t, http.StatusUnauthorized, r.status, tc.name)
		if first == nil {
			first = r.raw
		}
		assert.Equal(t, string(first), string(r.raw), "%s: every 401 reads the same", tc.name)
	}
	// An agent key on the intake route is the same 401.
	r, err := env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", "", body, map[string]string{"X-Agent-Key": env.agentKey})
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, r.status)
	assert.Equal(t, string(first), string(r.raw))

	// Revoked: every token at once, including the current one.
	ok := env.post(t, graceTok, gateBody(uuid.New(), 3, nil))
	require.Equal(t, http.StatusCreated, ok.status)
	tasksBefore++
	_, err = env.repo.Revoke(context.Background(), env.operatorID, env.name, "test")
	require.NoError(t, err)
	rev := env.post(t, graceTok, body)
	assert.Equal(t, http.StatusUnauthorized, rev.status)
	assert.Equal(t, string(first), string(rev.raw))
	assert.Equal(t, tasksBefore, env.countTasks(t), "no 401 wrote a card")

	// Re-issue re-arms the same principal: same id, receipts still answer.
	newTok := env.issue(t)
	p, err := env.repo.PrincipalByName(context.Background(), env.name)
	require.NoError(t, err)
	assert.Equal(t, env.principal.ID, p.ID)
	replay := env.post(t, newTok, gateBody(inGrace.resp.ApprovalID, 3, nil))
	require.Equal(t, http.StatusOK, replay.status, string(replay.raw))
	assert.Equal(t, inGrace.resp.TaskID, replay.resp.TaskID)
}

func TestGateIntakeDB_ValidTokenElsewhere_403_StateUnchanged(t *testing.T) {
	env := newGateIntakeEnv(t)
	created := env.post(t, env.token, gateBody(uuid.New(), 3, nil))
	require.Equal(t, http.StatusCreated, created.status)
	taskID := created.resp.TaskID.String()
	before := env.tasksSnapshot(t)

	newTask, _ := json.Marshal(map[string]any{"title": "x", "assignee_id": env.assigneeID, "assignee_type": "agent"})
	routes := []struct {
		method, path string
		body         []byte
	}{
		{http.MethodPost, "/api/v1/projects/" + env.otherProjID.String() + "/tasks", newTask},
		{http.MethodPost, "/api/v1/projects/" + env.projectID.String() + "/tasks", newTask},
		{http.MethodGet, "/api/v1/projects/" + env.projectID.String() + "/tasks", nil},
		{http.MethodGet, "/api/v1/tasks/" + taskID, nil},
		{http.MethodPatch, "/api/v1/tasks/" + taskID, []byte(`{"title":"hijacked","priority":"low"}`)},
		{http.MethodDelete, "/api/v1/tasks/" + taskID, nil},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/move", []byte(`{"status_slug":"done"}`)},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/assign", []byte(`{"assignee_id":null}`)},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/comments", []byte(`{"body":"x"}`)},
		{http.MethodPatch, "/api/v1/workspaces/" + env.wsID.String() + "/members/" + uuid.NewString(), []byte(`{"role":"admin"}`)},
		{http.MethodPost, "/api/v1/admin/extensions", []byte(`{}`)},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/merge", []byte(`{}`)},
		{http.MethodPost, "/api/v1/tasks/" + taskID + "/publish", []byte(`{}`)},
		{http.MethodGet, "/api/v1/gate-intake", nil},
		{http.MethodPut, "/api/v1/gate-intake", gateBody(uuid.New(), 3, nil)},
		{http.MethodPost, "/api/v1/no-such-route", nil},
	}
	for _, rt := range routes {
		r, err := env.do(t, env.client, rt.method, rt.path, "Bearer "+env.token, rt.body, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, r.status, "%s %s: %s", rt.method, rt.path, r.raw)
	}
	assert.Zero(t, env.stubHits.Load(), "no handler behind the guard was reached")
	assert.Equal(t, before, env.tasksSnapshot(t), "the database is unchanged")

	// Positive control: the same routes do work for an ordinary credential,
	// so the 403s above are the guard and not a broken route.
	r, err := env.do(t, env.client, http.MethodGet, "/api/v1/tasks/"+taskID, "", nil, map[string]string{"X-Agent-Key": env.agentKey})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, r.status, string(r.raw))
	r, err = env.do(t, env.client, http.MethodPost, "/api/v1/tasks/"+taskID+"/comments", "", []byte(`{}`), map[string]string{"X-Agent-Key": env.agentKey})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, r.status, string(r.raw))
	assert.EqualValues(t, 1, env.stubHits.Load())
}

func TestGateIntakeDB_TargetFields_403_NoTask(t *testing.T) {
	env := newGateIntakeEnv(t)
	before := env.tasksSnapshot(t)
	for _, extra := range []map[string]any{
		{"project_id": env.otherProjID.String()},
		{"assignee_id": uuid.NewString()},
		{"assignee_type": "user"},
		{"labels": []string{"kind:change"}},
		{"workspace_id": env.wsID.String()},
		{"status": "done"},
		{"status_id": uuid.NewString()},
		{"priority": "urgent"},
	} {
		r := env.post(t, env.token, gateBody(uuid.New(), 3, extra))
		assert.Equal(t, http.StatusForbidden, r.status, "%v: %s", extra, r.raw)
		assert.Equal(t, domain.GateIntakeCodeTargetFixed, r.errBody["error_code"])
	}
	assert.Equal(t, before, env.tasksSnapshot(t))
}

// The article URL is /blog/<slug> by its parsed path: an empty slug hidden
// behind a query or a fragment is refused and writes neither a card nor a
// receipt; a real slug with the same shape is accepted.
func TestGateIntakeDB_ArticleSlug_EmptyRefused(t *testing.T) {
	env := newGateIntakeEnv(t)
	before := env.tasksSnapshot(t)
	for _, u := range []string{
		"https://spark.entire.vc/blog/?x=1",
		"https://spark.entire.vc/blog/#section",
		"https://spark.entire.vc/blog/",
	} {
		r := env.post(t, env.token, gateBody(uuid.New(), 3, map[string]any{"article_url": u}))
		assert.Equal(t, http.StatusBadRequest, r.status, "%s: %s", u, r.raw)
		assert.Equal(t, "validation_failed", r.errBody["error_code"], u)
	}
	assert.Equal(t, before, env.tasksSnapshot(t), "no refusal wrote a card or a receipt")

	r := env.post(t, env.token, gateBody(uuid.New(), 3, map[string]any{"article_url": "https://spark.entire.vc/blog/some-article"}))
	assert.Equal(t, http.StatusCreated, r.status, string(r.raw))
	assert.Equal(t, 1, env.countTasks(t), "the positive control creates its card")
}

func TestGateIntakeDB_RequestShape(t *testing.T) {
	env := newGateIntakeEnv(t)
	before := env.tasksSnapshot(t)
	approval := uuid.New()

	r, err := env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", "Bearer "+env.token, gateBody(approval, 3, nil),
		map[string]string{"Content-Type": "text/plain"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnsupportedMediaType, r.status)

	r = env.post(t, env.token, []byte(`not json`))
	assert.Equal(t, http.StatusUnsupportedMediaType, r.status)

	base := gateBody(approval, 3, nil)
	big := make([]byte, 0, len(base)+domain.GateIntakeMaxBody)
	big = append(append(append(big, base[:1]...), bytes.Repeat([]byte(" "), domain.GateIntakeMaxBody)...), base[1:]...)
	r = env.post(t, env.token, big)
	assert.Equal(t, http.StatusRequestEntityTooLarge, r.status)

	r = env.post(t, env.token, gateBody(approval, 3, map[string]any{"note": "x"}))
	assert.Equal(t, http.StatusBadRequest, r.status)
	assert.Equal(t, "validation_failed", r.errBody["error_code"])

	r, err = env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", "Bearer "+env.token, gateBody(approval, 3, nil),
		map[string]string{"Idempotency-Key": uuid.NewString()})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, r.status)
	assert.Equal(t, domain.GateIntakeCodeIdempotencyMismatch, r.errBody["error_code"])
	assert.Equal(t, before, env.tasksSnapshot(t), "no refusal wrote anything")

	// A header that is sent is checked even when empty, and must be one value.
	r, err = env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", "Bearer "+env.token, gateBody(approval, 3, nil),
		map[string]string{"Idempotency-Key": ""})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, r.status, string(r.raw))
	assert.Equal(t, domain.GateIntakeCodeIdempotencyMismatch, r.errBody["error_code"])
	dup, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/gate-intake", bytes.NewReader(gateBody(approval, 3, nil)))
	require.NoError(t, err)
	dup.Header.Set("Content-Type", "application/json")
	dup.Header.Set("Authorization", "Bearer "+env.token)
	dup.Header.Add("Idempotency-Key", approval.String())
	dup.Header.Add("Idempotency-Key", approval.String())
	dupResp, err := env.client.Do(dup)
	require.NoError(t, err)
	_ = dupResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, dupResp.StatusCode, "a repeated Idempotency-Key is refused")
	assert.Equal(t, before, env.tasksSnapshot(t), "no refusal wrote anything")

	r, err = env.do(t, env.client, http.MethodPost, "/api/v1/gate-intake", "Bearer "+env.token, gateBody(approval, 3, nil),
		map[string]string{"Idempotency-Key": approval.String()})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, r.status, string(r.raw))
}
