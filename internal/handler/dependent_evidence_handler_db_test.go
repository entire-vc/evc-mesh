package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// The blocker side of a cross-workspace card wait: a waiter in one workspace
// declares a server-observed artifact_published wait on a card of another
// workspace, and the blocker's owner, holding only a key of the blocker's
// workspace, records the named result. Real Postgres, real agent keys and the
// real guard chain of the authenticated API group (DualAuth, WorkspaceRLS,
// RequireWorkspaceMemberScoped, RequireTaskProjectMember, route wsAccess+rbac).

type depEvidenceEnv struct {
	db     *sqlx.DB
	server *httptest.Server
	agents service.AgentService
}

type depEvidenceSide struct {
	ws, project, status uuid.UUID
}

func depEvidenceDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for real PostgreSQL acceptance")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newDepEvidenceEnv(t *testing.T) *depEvidenceEnv {
	t.Helper()
	db := depEvidenceDB(t)
	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	memberRepo := postgres.NewWorkspaceMemberRepo(db)
	projectRepo := postgres.NewProjectRepo(db)
	agentSvc := service.NewAgentService(postgres.NewAgentRepo(db), postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	if configurable, ok := agentSvc.(service.AgentServiceConfigurable); ok {
		configurable.SetAgentWorkspaceGrantRepo(postgres.NewAgentWorkspaceGrantRepo(db))
	}
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, memberRepo, "dependent-evidence-test-jwt-secret")
	depRepo := postgres.NewTaskDependencyRepo(db)
	depSvc := service.NewTaskDependencyService(depRepo, postgres.NewTaskRepo(db), postgres.NewActivityLogRepo(db), projectRepo)
	h := NewDependencyHandler(depSvc, nil).WithDependentEdgeScope(depRepo)

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	api.Use(mw.WorkspaceRLS(db, projectRepo))
	api.Use(mw.RequireWorkspaceMemberScoped(db))
	api.Use(mw.RequireTaskProjectMember(db))
	wsAccess := mw.RequireWorkspaceMember(db)
	rbac := func(p mw.Permission) echo.MiddlewareFunc { return mw.RequirePermission(p, memberRepo) }
	// The same registrations as cmd/api/routes_tasks.go.
	api.GET("/tasks/:task_id/dependencies", h.List, wsAccess)
	api.POST("/tasks/:task_id/dependencies", h.Create, wsAccess, rbac(mw.PermUpdateTask))
	api.PATCH("/tasks/:task_id/dependencies/:dep_id", h.Update, wsAccess, rbac(mw.PermUpdateTask))
	api.PATCH("/tasks/:task_id/dependents/:dependent_edge_id/evidence", h.UpdateEvidenceFromBlocker, wsAccess, rbac(mw.PermUpdateTask))

	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return &depEvidenceEnv{db: db, server: server, agents: agentSvc}
}

func (env *depEvidenceEnv) side(t *testing.T, name string) depEvidenceSide {
	t.Helper()
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: name, Slug: "dep-ev-" + uuid.NewString(), OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(env.db).Create(ctx, ws))
	t.Cleanup(func() { _, _ = env.db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws.ID) })
	p := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: name, Slug: "dep-ev-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(env.db).Create(ctx, p))
	s := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Color: "#000000"}
	require.NoError(t, postgres.NewTaskStatusRepo(env.db).Create(ctx, s))
	return depEvidenceSide{ws: ws.ID, project: p.ID, status: s.ID}
}

func (env *depEvidenceEnv) task(t *testing.T, s depEvidenceSide, title string) uuid.UUID {
	t.Helper()
	task := &domain.Task{ID: uuid.New(), ProjectID: s.project, StatusID: s.status, Title: title,
		AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium,
		CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, postgres.NewTaskRepo(env.db).Create(context.Background(), task, nil))
	return task.ID
}

// agent registers an agent at home in s, a member of s's project; the key it
// returns authenticates into s only.
func (env *depEvidenceEnv) agent(t *testing.T, s depEvidenceSide, name string) (agentID uuid.UUID, apiKey string) {
	t.Helper()
	reg, err := env.agents.Register(context.Background(), service.RegisterAgentInput{
		WorkspaceID: s.ws, Name: name + " " + uuid.NewString()[:8], AgentType: domain.AgentTypeCustom})
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO project_members (id, project_id, workspace_id, agent_id, role) VALUES ($1,$2,$3,$4,'member')`,
		uuid.New(), s.project, s.ws, reg.Agent.ID)
	require.NoError(t, err)
	return reg.Agent.ID, reg.APIKey
}

// crossWait has waiter (at home in its own workspace, granted into the
// blocker's) declare a server-observed artifact_published wait of waiting on
// blocker, as the Runner's wait_for on=card does, and returns the edge id.
func (env *depEvidenceEnv) crossWait(t *testing.T, waiter, waiting, blocker, blockerWS uuid.UUID, criterion string) uuid.UUID {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO agent_workspace_grants (id, agent_id, workspace_id, api_key_prefix, api_key_hash)
		VALUES ($1,$2,$3,$4,'x')`, uuid.New(), waiter, blockerWS, uuid.NewString()[:16])
	require.NoError(t, err)
	_, err = env.db.Exec(`UPDATE tasks SET checked_out_by=$2, checkout_token=gen_random_uuid(),
		checkout_expires=now()+interval '1 hour', checkout_acquired_at=now() WHERE id=$1`, waiting, waiter)
	require.NoError(t, err)
	cond := string(domain.DependencyConditionArtifactPublished)
	res, err := postgres.NewTurnCommandRepo(env.db).CommitTurn(context.Background(), &domain.TurnCommand{
		TaskID: waiting, AgentID: waiter, Kind: domain.TurnOutcomeWait, CommentBody: "Waiting on the other workspace's card.",
		Wait: &domain.TaskWait{Kind: domain.TaskWaitCard, TargetTaskID: &blocker, TargetCondition: &cond, Criterion: &criterion}})
	require.NoError(t, err)
	require.NotNil(t, res.Wait)
	require.True(t, res.Wait.ServerObserved, "the declared wait must be server-observed")
	var edge uuid.UUID
	require.NoError(t, env.db.Get(&edge, `SELECT id FROM task_dependencies
		WHERE task_id=$1 AND depends_on_task_id=$2 AND wait_agent_id=$3`, waiting, blocker, waiter))
	return edge
}

func (env *depEvidenceEnv) call(t *testing.T, key, method, path string, body any) (status int, out map[string]any) {
	t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, env.server.URL+"/api/v1"+path, reader)
	require.NoError(t, err)
	req.Header.Set("X-Agent-Key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out = map[string]any{}
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

type depEvidenceEdgeRow struct {
	Status     string  `db:"evidence_status"`
	URL        *string `db:"evidence_url"`
	Condition  string  `db:"condition"`
	Criterion  string  `db:"criterion"`
	Generation int64   `db:"generation"`
	Actor      *string `db:"evidence_actor_id"`
}

func (env *depEvidenceEnv) edge(t *testing.T, id uuid.UUID) depEvidenceEdgeRow {
	t.Helper()
	var row depEvidenceEdgeRow
	require.NoError(t, env.db.Get(&row, `SELECT evidence_status, evidence_url, condition, criterion, generation,
		evidence_actor_id::text AS evidence_actor_id FROM task_dependencies WHERE id=$1`, id))
	return row
}

type depEvidenceWaitRow struct {
	Resolution *string `db:"resolution"`
	ReleasedBy *string `db:"released_by"`
	Resolved   bool    `db:"resolved"`
}

func (env *depEvidenceEnv) wait(t *testing.T, waiting uuid.UUID) depEvidenceWaitRow {
	t.Helper()
	var row depEvidenceWaitRow
	require.NoError(t, env.db.Get(&row, `SELECT resolution, released_by, resolved_at IS NOT NULL AS resolved
		FROM task_waits WHERE task_id=$1 AND kind='card' ORDER BY created_at DESC LIMIT 1`, waiting))
	return row
}

const depEvidenceCriterion = "Closure of the direct-rollout mode: the CI deploy job is the only path"

type depEvidenceCase struct {
	env                 *depEvidenceEnv
	waiterSide, blkSide depEvidenceSide
	waiting, blocker    uuid.UUID
	waiterKey, ownerKey string
	ownerID             uuid.UUID
	edge                uuid.UUID
}

func newDepEvidenceCase(t *testing.T) *depEvidenceCase {
	t.Helper()
	env := newDepEvidenceEnv(t)
	c := &depEvidenceCase{env: env}
	c.waiterSide, c.blkSide = env.side(t, "Waiter workspace"), env.side(t, "Blocker workspace")
	c.waiting = env.task(t, c.waiterSide, "Waiting card (title must not leak)")
	c.blocker = env.task(t, c.blkSide, "Blocker card")
	var waiterID uuid.UUID
	waiterID, c.waiterKey = env.agent(t, c.waiterSide, "Waiter")
	c.ownerID, c.ownerKey = env.agent(t, c.blkSide, "Blocker owner")
	c.edge = env.crossWait(t, waiterID, c.waiting, c.blocker, c.blkSide.ws, depEvidenceCriterion)
	return c
}

func (c *depEvidenceCase) attestBody() map[string]any {
	return map[string]any{"evidence_url": "https://git.example/ops/rollout/-/merge_requests/7",
		"evidence_criterion": depEvidenceCriterion, "attest": true}
}

func (c *depEvidenceCase) blockerPath() string {
	return "/tasks/" + c.blocker.String() + "/dependents/" + c.edge.String() + "/evidence"
}

// The incident: the only attestation path there was is the waiter's, and the
// blocker's key does not pass its workspace guard. It stays that way — the new
// route does not widen the old one.
func TestDependentEvidence_BlockerKeyRefusedOnWaiterRoute_DB(t *testing.T) {
	c := newDepEvidenceCase(t)
	body := c.attestBody()
	status, out := c.env.call(t, c.ownerKey, http.MethodPatch,
		"/tasks/"+c.waiting.String()+"/dependencies/"+c.edge.String(), body)
	assert.Equal(t, http.StatusForbidden, status, out)

	// The waiter's own key passes the route but may not attest for a blocker it
	// cannot open (dependency_evidence.go: actor is not authorized).
	status, out = c.env.call(t, c.waiterKey, http.MethodPatch,
		"/tasks/"+c.waiting.String()+"/dependencies/"+c.edge.String(), body)
	assert.Equal(t, http.StatusBadRequest, status, out)
	assert.Contains(t, out["message"], "actor is not authorized to attest this criterion")
	assert.Equal(t, "none", c.env.edge(t, c.edge).Status)
	assert.False(t, c.env.wait(t, c.waiting).Resolved)
}

func TestDependentEvidence_BlockerOwnerAttestsCrossWorkspaceWait_DB(t *testing.T) {
	c := newDepEvidenceCase(t)
	before := c.env.edge(t, c.edge)
	require.Equal(t, "none", before.Status)
	require.False(t, c.env.wait(t, c.waiting).Resolved)

	// The blocker side finds the edge on its own card: id and criterion, not
	// the waiting card's title or status.
	status, list := c.env.call(t, c.ownerKey, http.MethodGet, "/tasks/"+c.blocker.String()+"/dependencies", nil)
	require.Equal(t, http.StatusOK, status, list)
	incoming, _ := list["incoming"].([]any)
	require.Len(t, incoming, 1, list)
	in := incoming[0].(map[string]any)
	assert.Equal(t, c.edge.String(), in["id"])
	assert.Equal(t, depEvidenceCriterion, in["criterion"])
	assert.Nil(t, in["related_task_title"], "the waiting card's title must not reach the blocker side")
	assert.Nil(t, in["related_task_status_id"], "the waiting card's status must not reach the blocker side")

	status, out := c.env.call(t, c.ownerKey, http.MethodPatch, c.blockerPath(), c.attestBody())
	require.Equal(t, http.StatusOK, status, out)
	assert.Equal(t, "trusted", out["evidence_status"])
	assert.Equal(t, domain.ReleasedByEvidence, out["released_by"])
	assert.Equal(t, true, out["condition_satisfied"])
	assert.Equal(t, depEvidenceCriterion, out["criterion"])
	assert.Equal(t, c.edge.String(), out["id"])
	for _, k := range []string{"title", "related_task_title", "related_task_status_id", "status", "status_id", "owner_wait_id", "owner_agent_id"} {
		assert.NotContains(t, out, k, "the response must not describe the waiting side")
	}

	// Re-read what was stored: the edge, and the waiter's server-observed wait.
	after := c.env.edge(t, c.edge)
	assert.Equal(t, "trusted", after.Status)
	require.NotNil(t, after.Actor)
	assert.Equal(t, c.ownerID.String(), *after.Actor, "the attesting actor is stored on the edge")
	assert.Equal(t, before.Generation, after.Generation, "evidence does not bump the edge generation")
	assert.Equal(t, string(domain.DependencyConditionArtifactPublished), after.Condition)
	assert.Equal(t, depEvidenceCriterion, after.Criterion)
	w := c.env.wait(t, c.waiting)
	require.True(t, w.Resolved, "the waiter's wait must be released by the evidence")
	require.NotNil(t, w.Resolution)
	assert.Equal(t, string(domain.WaitResolutionMet), *w.Resolution)
	require.NotNil(t, w.ReleasedBy)
	assert.Equal(t, domain.ReleasedByEvidence, *w.ReleasedBy)
}

func TestDependentEvidence_Refusals_DB(t *testing.T) {
	c := newDepEvidenceCase(t)
	unchanged := func(t *testing.T) {
		t.Helper()
		row := c.env.edge(t, c.edge)
		assert.Equal(t, "none", row.Status)
		assert.Nil(t, row.URL)
		assert.Equal(t, depEvidenceCriterion, row.Criterion)
		assert.Equal(t, string(domain.DependencyConditionArtifactPublished), row.Condition)
		assert.False(t, c.env.wait(t, c.waiting).Resolved)
	}

	t.Run("no access to the blocker", func(t *testing.T) {
		// The waiter's home key names the blocker's workspace in the path.
		status, out := c.env.call(t, c.waiterKey, http.MethodPatch, c.blockerPath(), c.attestBody())
		assert.Equal(t, http.StatusForbidden, status, out)
		// An agent of the blocker's workspace outside the blocker's project.
		_, stranger := c.env.agent(t, c.env.side(t, "Third workspace"), "Stranger")
		status, out = c.env.call(t, stranger, http.MethodPatch, c.blockerPath(), c.attestBody())
		assert.Equal(t, http.StatusForbidden, status, out)
		outsiderReg, err := c.env.agents.Register(context.Background(), service.RegisterAgentInput{
			WorkspaceID: c.blkSide.ws, Name: "Outsider " + uuid.NewString()[:8], AgentType: domain.AgentTypeCustom})
		require.NoError(t, err)
		status, out = c.env.call(t, outsiderReg.APIKey, http.MethodPatch, c.blockerPath(), c.attestBody())
		assert.Equal(t, http.StatusForbidden, status, out, "a workspace member outside the blocker's project")
		unchanged(t)
	})

	t.Run("condition or criterion from the blocker side", func(t *testing.T) {
		for _, extra := range []map[string]any{
			{"condition": "task_done"},
			{"criterion": "something else"},
			{"criterion": nil},
		} {
			body := c.attestBody()
			for k, v := range extra {
				body[k] = v
			}
			body["expected_generation"] = c.env.edge(t, c.edge).Generation
			status, out := c.env.call(t, c.ownerKey, http.MethodPatch, c.blockerPath(), body)
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", extra, out)
		}
		unchanged(t)
	})

	t.Run("edge of another blocker", func(t *testing.T) {
		other := c.env.task(t, c.blkSide, "Another blocker-side card")
		status, out := c.env.call(t, c.ownerKey, http.MethodPatch,
			"/tasks/"+other.String()+"/dependents/"+c.edge.String()+"/evidence", c.attestBody())
		assert.Equal(t, http.StatusNotFound, status, out)
		unchanged(t)
	})

	t.Run("unverifiable evidence", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"empty url":      {"evidence_url": "", "evidence_criterion": depEvidenceCriterion, "attest": true},
			"no url":         {"evidence_criterion": depEvidenceCriterion, "attest": true},
			"no attest":      {"evidence_url": "https://git.example/x", "evidence_criterion": depEvidenceCriterion},
			"no criterion":   {"evidence_url": "https://git.example/x", "attest": true},
			"wrong criteron": {"evidence_url": "https://git.example/x", "evidence_criterion": "another result", "attest": true},
		} {
			status, out := c.env.call(t, c.ownerKey, http.MethodPatch, c.blockerPath(), body)
			assert.Equal(t, http.StatusBadRequest, status, "%s: %v", name, out)
		}
		unchanged(t)
	})

	t.Run("a trusted receipt is not downgraded", func(t *testing.T) {
		status, out := c.env.call(t, c.ownerKey, http.MethodPatch, c.blockerPath(), c.attestBody())
		require.Equal(t, http.StatusOK, status, out)
		status, out = c.env.call(t, c.ownerKey, http.MethodPatch, c.blockerPath(),
			map[string]any{"evidence_url": "https://git.example/other"})
		assert.Equal(t, http.StatusBadRequest, status, out)
		assert.Equal(t, "trusted", c.env.edge(t, c.edge).Status)
		assert.True(t, c.env.wait(t, c.waiting).Resolved)
	})
}

// A cross-workspace edge without a typed wait's provenance cannot be written
// (the completion_scope trigger refuses it); were one to exist, the blocker
// route must not reach across for it. Made here with triggers off for this
// session only.
func TestDependentEvidence_CrossWorkspaceWithoutWaitAgentNotFound_DB(t *testing.T) {
	c := newDepEvidenceCase(t)
	tx, err := c.env.db.Beginx()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	if _, serr := tx.Exec(`SET LOCAL session_replication_role = replica`); serr != nil {
		t.Skipf("cannot disable triggers for the session (needs superuser): %v", serr)
	}
	_, err = tx.Exec(`UPDATE task_dependencies SET wait_agent_id = NULL WHERE id = $1`, c.edge)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	status, out := c.env.call(t, c.ownerKey, http.MethodPatch, c.blockerPath(), c.attestBody())
	assert.Equal(t, http.StatusNotFound, status, out)
	assert.Equal(t, "none", c.env.edge(t, c.edge).Status)
}

// Edges inside one workspace keep the waiter-side route as it was, and the
// blocker route reaches them too.
func TestDependentEvidence_SameWorkspaceEdgeUnchanged_DB(t *testing.T) {
	env := newDepEvidenceEnv(t)
	s := env.side(t, "One workspace")
	_, key := env.agent(t, s, "Owner")
	blocker, waiting := env.task(t, s, "Blocker"), env.task(t, s, "Waiting")
	const crit = "contract v2 published"
	create := func(from uuid.UUID) uuid.UUID {
		status, out := env.call(t, key, http.MethodPost, "/tasks/"+from.String()+"/dependencies", map[string]any{
			"depends_on_task_id": blocker, "dependency_type": "blocks", "condition": "artifact_published", "criterion": crit})
		require.Equal(t, http.StatusCreated, status, out)
		return uuid.MustParse(out["id"].(string))
	}
	body := map[string]any{"evidence_url": "https://git.example/c/2", "evidence_criterion": crit, "attest": true}

	edge := create(waiting)
	status, out := env.call(t, key, http.MethodPatch, "/tasks/"+waiting.String()+"/dependencies/"+edge.String(), body)
	require.Equal(t, http.StatusOK, status, out)
	assert.Equal(t, "trusted", out["evidence_status"])

	// The waiter route still changes what the edge waits for.
	gen := env.edge(t, edge).Generation
	status, out = env.call(t, key, http.MethodPatch, "/tasks/"+waiting.String()+"/dependencies/"+edge.String(),
		map[string]any{"criterion": "contract v3 published", "expected_generation": gen})
	require.Equal(t, http.StatusOK, status, out)
	assert.Equal(t, "contract v3 published", env.edge(t, edge).Criterion)

	other := env.task(t, s, "Waiting 2")
	edge2 := create(other)
	status, out = env.call(t, key, http.MethodPatch,
		"/tasks/"+blocker.String()+"/dependents/"+edge2.String()+"/evidence", body)
	require.Equal(t, http.StatusOK, status, out)
	assert.Equal(t, "trusted", out["evidence_status"])
	assert.Equal(t, "trusted", env.edge(t, edge2).Status)
}
