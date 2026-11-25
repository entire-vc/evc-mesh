package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// DB tests of GateIntakeService (contract CH→Mesh gate-create/v1,
// docs/gate-intake.md) on the real task service and repositories: one card per
// approval whatever the number of deliveries, 409 on a changed fingerprint,
// and the single 401 for every token failure. The HTTP layer is covered by
// internal/handler/gate_intake_db_test.go. Skips without a reachable Postgres
// (agentKeyWsReuseTestDB, like the other *_db_test.go files here).

type gateIntakeSvcFixture struct {
	db        *sqlx.DB
	repo      *postgres.GateIntakeRepo
	svc       *GateIntakeService
	projectID uuid.UUID
	principal *domain.GateIntakePrincipal
	token     string
}

func newGateIntakeSvcFixture(t *testing.T) *gateIntakeSvcFixture {
	t.Helper()
	db := agentKeyWsReuseTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	ownerID := agentKeyWsReuseOwner(t, db)
	ws := &domain.Workspace{ID: uuid.New(), Name: "gate-svc", Slug: "gate-svc-" + suffix, OwnerID: ownerID}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	projectRepo := postgres.NewProjectRepo(db)
	statusRepo := postgres.NewTaskStatusRepo(db)
	projectMemberRepo := postgres.NewProjectMemberRepo(db)
	agentRepo := postgres.NewAgentRepo(db)
	project := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "gate svc", Slug: "gate-svc-" + suffix,
		DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, statusRepo.Create(ctx, &domain.TaskStatus{ID: uuid.New(), ProjectID: project.ID, Name: "Todo",
		Slug: "todo", Color: "#00FF00", Position: 0, Category: domain.StatusCategoryTodo, IsDefault: true}))
	agent := &domain.Agent{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Gate Assignee " + suffix, Slug: "gate-assignee-" + suffix,
		AgentType: domain.AgentTypeCustom, APIKeyHash: "$2a$12$hash-" + suffix, APIKeyPrefix: "pfx-" + suffix,
		Status: domain.AgentStatusOffline, Role: "developer"}
	require.NoError(t, agentRepo.Create(ctx, agent))
	require.NoError(t, projectMemberRepo.Create(ctx, &domain.ProjectMember{ID: uuid.New(), ProjectID: project.ID, AgentID: &agent.ID,
		Role: "member", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}))

	// The assignee options of the production wiring (cmd/api/wire_core_services.go).
	tasks := NewTaskService(postgres.NewTaskRepo(db), statusRepo, nil, postgres.NewActivityLogRepo(db),
		WithProjectRepo(projectRepo),
		WithProjectMemberRepoTask(projectMemberRepo),
		WithTaskAgentRepo(agentRepo),
		WithUserRepoTask(postgres.NewUserRepo(db)),
		WithWorkspaceMembershipReader(postgres.NewWorkspaceMembershipReader(db)),
	)
	repo := postgres.NewGateIntakeRepo(db)
	f := &gateIntakeSvcFixture{db: db, repo: repo, svc: NewGateIntakeService(repo, tasks, statusRepo), projectID: project.ID}

	tok, err := domain.NewGateIntakeToken()
	require.NoError(t, err)
	f.principal, _, err = repo.Issue(ctx, uuid.New(), postgres.GateIntakeBinding{Name: "gi-svc-" + suffix, WorkspaceID: ws.ID,
		ProjectID: project.ID, AssigneeAgentID: agent.ID, Labels: []string{"kind:ops", "spark-gate"}, Priority: domain.PriorityHigh,
		Schema: domain.GateIntakeSchemaContentHub}, domain.HashGateIntakeToken(tok), domain.GateIntakeTokenDisplay(tok))
	require.NoError(t, err)
	f.token = tok

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_receipts WHERE principal_id = $1`, f.principal.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_audit WHERE principal_id = $1`, f.principal.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM gate_intake_principals WHERE id = $1`, f.principal.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM tasks WHERE project_id = $1`, project.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = $1`, project.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM task_statuses WHERE project_id = $1`, project.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, project.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM activity_log WHERE workspace_id = $1`, ws.ID)
		_, _ = db.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, agent.ID)
	})
	return f
}

func gateIntakeSvcRequest(t *testing.T, approval uuid.UUID, draftVersion int) *domain.GateIntakeRequest {
	t.Helper()
	body := `{"schema":"contenthub-spark-gate-wake/v1","approval_id":"` + approval.String() + `","task_id":4711,` +
		`"draft_version":` + strconv.Itoa(draftVersion) + `,` +
		`"payload_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
		`"manifest_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","spark_project_id":12,"mr_iid":128,` +
		`"head_sha":"0123456789abcdef0123456789abcdef01234567",` +
		`"mr_url":"https://gitlab.example.com/entire-vc/spark/-/merge_requests/128",` +
		`"article_url":"https://spark.entire.vc/blog/some-article"}`
	req, perr := domain.ParseGateIntakeRequest([]byte(body))
	require.Nil(t, perr)
	return req
}

func (f *gateIntakeSvcFixture) cards(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.db.GetContext(context.Background(), &n, `SELECT count(*) FROM tasks WHERE project_id = $1`, f.projectID))
	return n
}

func TestGateIntakeService_Authenticate(t *testing.T) {
	f := newGateIntakeSvcFixture(t)
	ctx := context.Background()

	p, err := f.svc.AuthenticateGateIntake(ctx, f.token)
	require.NoError(t, err)
	assert.Equal(t, f.principal.ID, p.ID)

	unknown, err := domain.NewGateIntakeToken()
	require.NoError(t, err)
	for name, tok := range map[string]string{
		"empty": "", "agent key shape": "agk_ws_abc", "truncated": f.token[:20], "unknown": unknown,
	} {
		_, aerr := f.svc.AuthenticateGateIntake(ctx, tok)
		assert.ErrorIs(t, aerr, domain.ErrGateIntakeUnauthorized, name)
	}

	_, err = f.repo.Revoke(ctx, uuid.New(), f.principal.Name, "test")
	require.NoError(t, err)
	_, err = f.svc.AuthenticateGateIntake(ctx, f.token)
	assert.ErrorIs(t, err, domain.ErrGateIntakeUnauthorized, "a revoked token is the same 401")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = f.svc.AuthenticateGateIntake(cancelled, unknown)
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrGateIntakeUnauthorized, "a storage failure is not a 401")
}

func TestGateIntakeService_IntakeCreateReplayConflict(t *testing.T) {
	f := newGateIntakeSvcFixture(t)
	ctx := context.Background()
	approval := uuid.New()
	req := gateIntakeSvcRequest(t, approval, 10)

	resp, code, err := f.svc.Intake(ctx, f.principal, req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, code)
	assert.False(t, resp.Replayed)
	assert.Equal(t, approval, resp.ApprovalID)
	assert.Equal(t, f.projectID, resp.ProjectID)
	assert.Equal(t, req.RequestSHA256(), resp.RequestSHA256)
	require.Equal(t, 1, f.cards(t))

	var card struct {
		AssigneeID    uuid.UUID `db:"assignee_id"`
		Priority      string    `db:"priority"`
		Labels        string    `db:"labels"`
		CreatedBy     uuid.UUID `db:"created_by"`
		CreatedByType string    `db:"created_by_type"`
	}
	require.NoError(t, f.db.GetContext(ctx, &card,
		`SELECT assignee_id, priority, labels::text AS labels, created_by, created_by_type FROM tasks WHERE id = $1`, resp.TaskID))
	assert.Equal(t, f.principal.AssigneeAgentID, card.AssigneeID)
	assert.Equal(t, "high", card.Priority)
	assert.Equal(t, "{kind:ops,spark-gate}", card.Labels, "labels come from the principal; no duplicate-title hint")
	assert.Equal(t, f.principal.ID, card.CreatedBy)
	assert.Equal(t, string(domain.ActorTypeSystem), card.CreatedByType)

	again, code, err := f.svc.Intake(ctx, f.principal, gateIntakeSvcRequest(t, approval, 10))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, again.Replayed)
	assert.Equal(t, resp.TaskID, again.TaskID)
	assert.True(t, resp.CreatedAt.Equal(again.CreatedAt), "the replay answers the stored response")
	assert.Equal(t, 1, f.cards(t))

	_, _, err = f.svc.Intake(ctx, f.principal, gateIntakeSvcRequest(t, approval, 11))
	var ae *apierror.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, http.StatusConflict, ae.Code)
	assert.Equal(t, domain.GateIntakeCodePayloadConflict, ae.ErrorCode)
	assert.Equal(t, resp.TaskID, ae.Context["task_id"])
	assert.Equal(t, 1, f.cards(t), "a conflict opens no card")
	n, err := f.repo.ReceiptCount(ctx, f.principal.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	// A card deleted later is still the card that approval opened.
	_, err = f.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = $1`, resp.TaskID)
	require.NoError(t, err)
	after, code, err := f.svc.Intake(ctx, f.principal, gateIntakeSvcRequest(t, approval, 10))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, resp.TaskID, after.TaskID)
	assert.Equal(t, 0, f.cards(t), "a replay never recreates a deleted card")
}

func TestGateIntakeService_ConcurrentDeliveriesOpenOneCard(t *testing.T) {
	f := newGateIntakeSvcFixture(t)
	approval := uuid.New()
	const n = 8
	var wg sync.WaitGroup
	type out struct {
		id   uuid.UUID
		code int
		err  error
	}
	results := make([]out, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, code, err := f.svc.Intake(context.Background(), f.principal, gateIntakeSvcRequest(t, approval, 10))
			if err == nil {
				results[i] = out{id: resp.TaskID, code: code}
			} else {
				results[i] = out{err: err}
			}
		}(i)
	}
	wg.Wait()
	created := 0
	ids := map[uuid.UUID]bool{}
	for _, r := range results {
		require.NoError(t, r.err)
		ids[r.id] = true
		if r.code == http.StatusCreated {
			created++
		}
	}
	assert.Equal(t, 1, created, "exactly one delivery creates")
	assert.Len(t, ids, 1, "every delivery answers the same card")
	assert.Equal(t, 1, f.cards(t))
}

// pendingReceipt is another delivery of the approval that has written its
// card and receipt in a real transaction but not committed yet: Intake's
// fast-path read cannot see it, and Intake's own receipt INSERT must wait on
// its row.
type pendingReceipt struct {
	tx     *sql.Tx
	pid    int
	taskID uuid.UUID
}

func (f *gateIntakeSvcFixture) beginPendingReceipt(t *testing.T, req *domain.GateIntakeRequest) *pendingReceipt {
	t.Helper()
	ctx := context.Background()
	var statusID uuid.UUID
	require.NoError(t, f.db.GetContext(ctx, &statusID, `SELECT id FROM task_statuses WHERE project_id = $1 AND is_default`, f.projectID))
	winner := &domain.Task{ID: uuid.New(), ProjectID: f.projectID, StatusID: statusID, Title: req.Title(),
		Priority: domain.PriorityHigh, AssigneeType: domain.AssigneeTypeUnassigned, CreatedBy: f.principal.ID,
		CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, postgres.NewTaskRepo(f.db).Create(ctx, winner, nil))

	tx, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	var pid int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	sha := req.RequestSHA256()
	created := time.Now().UTC().Truncate(time.Microsecond)
	body, err := json.Marshal(&domain.GateIntakeResponse{Schema: domain.GateIntakeResponseSchema, ApprovalID: req.ApprovalID,
		TaskID: winner.ID, ProjectID: f.projectID, RequestSHA256: sha, CreatedAt: created})
	require.NoError(t, err)
	inserted, err := f.repo.InsertReceiptTx(ctx, tx, &domain.GateIntakeReceipt{PrincipalID: f.principal.ID,
		ApprovalID: req.ApprovalID, RequestSHA256: sha, TaskID: winner.ID, Response: body, CreatedAt: created}, f.principal.ID)
	require.NoError(t, err)
	require.True(t, inserted)
	return &pendingReceipt{tx: tx, pid: pid, taskID: winner.ID}
}

// waitBlockedBy waits until some backend waits on a lock held by the pending
// transaction: Intake's receipt INSERT has reached the receipt row.
func (f *gateIntakeSvcFixture) waitBlockedBy(t *testing.T, p *pendingReceipt) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		require.NoError(t, f.db.GetContext(context.Background(), &n,
			`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, p.pid))
		if n > 0 {
			return
		}
		require.True(t, time.Now().Before(deadline), "Intake never reached the pending receipt row")
		time.Sleep(20 * time.Millisecond)
	}
}

type intakeResult struct {
	resp *domain.GateIntakeResponse
	code int
	err  error
}

func (f *gateIntakeSvcFixture) intakeAsync(req *domain.GateIntakeRequest) <-chan intakeResult {
	ch := make(chan intakeResult, 1)
	go func() {
		resp, code, err := f.svc.Intake(context.Background(), f.principal, req)
		ch <- intakeResult{resp: resp, code: code, err: err}
	}()
	return ch
}

// The receipt INSERT, not the fast-path read, decides: a delivery that passed
// the read while another one was still committing waits on the receipt row,
// rolls its own card back and answers from the receipt that committed.
func TestGateIntakeService_ReceiptCommittedDuringCreateWins(t *testing.T) {
	f := newGateIntakeSvcFixture(t)
	req := gateIntakeSvcRequest(t, uuid.New(), 10)
	pending := f.beginPendingReceipt(t, req)

	done := f.intakeAsync(gateIntakeSvcRequest(t, req.ApprovalID, 10))
	f.waitBlockedBy(t, pending)
	require.NoError(t, pending.tx.Commit())

	var got intakeResult
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Intake did not finish after the other delivery committed")
	}
	require.NoError(t, got.err)
	assert.Equal(t, http.StatusOK, got.code)
	assert.True(t, got.resp.Replayed)
	assert.Equal(t, pending.taskID, got.resp.TaskID, "the committed delivery's card is the answer")
	assert.Equal(t, 1, f.cards(t), "the late card was rolled back with its refused receipt")

	// The rolled-back create released the project lock: the next approval opens its card.
	next, code, err := f.svc.Intake(context.Background(), f.principal, gateIntakeSvcRequest(t, uuid.New(), 10))
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, code)
	assert.NotEqual(t, pending.taskID, next.TaskID)
	assert.Equal(t, 2, f.cards(t))
}

func TestGateIntakeService_ReceiptCommittedDuringCreateConflicts(t *testing.T) {
	f := newGateIntakeSvcFixture(t)
	req := gateIntakeSvcRequest(t, uuid.New(), 10)
	pending := f.beginPendingReceipt(t, req)

	done := f.intakeAsync(gateIntakeSvcRequest(t, req.ApprovalID, 12))
	f.waitBlockedBy(t, pending)
	require.NoError(t, pending.tx.Commit())

	var got intakeResult
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Intake did not finish after the other delivery committed")
	}
	var ae *apierror.Error
	require.ErrorAs(t, got.err, &ae)
	assert.Equal(t, http.StatusConflict, ae.Code, "a late delivery with another payload is a 409")
	assert.Equal(t, domain.GateIntakeCodePayloadConflict, ae.ErrorCode)
	assert.Equal(t, pending.taskID, ae.Context["task_id"])
	assert.Equal(t, 1, f.cards(t), "the conflicting delivery opened no card")
}

// A failed receipt read is an error, never a new card.
func TestGateIntakeService_ReceiptReadFailureOpensNoCard(t *testing.T) {
	f := newGateIntakeSvcFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := f.svc.Intake(ctx, f.principal, gateIntakeSvcRequest(t, uuid.New(), 10))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, f.cards(t))
}

func TestGateIntakeService_ProjectWithoutDefaultStatus(t *testing.T) {
	f := newGateIntakeSvcFixture(t)
	ctx := context.Background()
	_, err := f.db.ExecContext(ctx, `UPDATE task_statuses SET is_default = FALSE WHERE project_id = $1`, f.projectID)
	require.NoError(t, err)
	_, _, err = f.svc.Intake(ctx, f.principal, gateIntakeSvcRequest(t, uuid.New(), 10))
	require.Error(t, err)
	assert.Equal(t, 0, f.cards(t))
}
