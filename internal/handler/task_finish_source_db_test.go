package handler

import (
	"context"
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

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// The server's own finish_task move keeps its name in the durable
// audit (activity_log.source and changes.source) instead of folding into
// "api", while a client still cannot claim it on POST /tasks/:id/move.

type finishSourceFixture struct {
	db          *sqlx.DB
	svc         service.TaskService
	project     uuid.UUID
	todo, doing uuid.UUID
	agent       uuid.UUID
}

func finishSourceSetup(t *testing.T) *finishSourceFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL or DATABASE_URL required for real PostgreSQL")
	}
	db, err := sqlx.Connect("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: "Finish source", Slug: "finish-source-" + uuid.NewString(), OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws.ID) })
	p := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Finish", Slug: "finish-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, p))
	statuses := postgres.NewTaskStatusRepo(db)
	f := &finishSourceFixture{db: db, project: p.ID, todo: uuid.New(), doing: uuid.New(), agent: uuid.New()}
	for _, s := range []*domain.TaskStatus{
		{ID: f.todo, ProjectID: p.ID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Color: "#000000", Position: 0},
		{ID: f.doing, ProjectID: p.ID, Name: "In Progress", Slug: "in-progress", Category: domain.StatusCategoryInProgress, Color: "#0000AA", Position: 1},
		{ID: uuid.New(), ProjectID: p.ID, Name: "Review", Slug: "review", Category: domain.StatusCategoryReview, Color: "#AAAA00", Position: 8},
		{ID: uuid.New(), ProjectID: p.ID, Name: "Done", Slug: "done", Category: domain.StatusCategoryDone, Color: "#00AA00", Position: 9},
	} {
		require.NoError(t, statuses.Create(ctx, s))
	}
	_, err = db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,api_key_hash,api_key_prefix) VALUES($1,$2,'finish source fixture',$3,'fixture-hash','test')`, f.agent, ws.ID, f.agent.String())
	require.NoError(t, err)
	f.svc = service.NewTaskService(postgres.NewTaskRepo(db), statuses, nil, postgres.NewActivityLogRepo(db), service.WithProjectRepo(postgres.NewProjectRepo(db)))
	return f
}

func (f *finishSourceFixture) task(t *testing.T) uuid.UUID {
	t.Helper()
	task := &domain.Task{ID: uuid.New(), ProjectID: f.project, StatusID: f.todo, Title: "finish source fixture", AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedByType: domain.ActorTypeSystem}
	require.NoError(t, f.svc.Create(actorctx.WithActor(context.Background(), uuid.Nil, domain.ActorTypeSystem), task))
	return task.ID
}

func (f *finishSourceFixture) post(t *testing.T, actor context.Context, path, taskID, body string, serve func(echo.Context) error) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)).WithContext(actor)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath(path)
	c.SetParamNames("task_id")
	c.SetParamValues(taskID)
	mw.SetProjectAccess(c, openAccess)
	require.NoError(t, serve(c))
	return rec
}

type movedRow struct {
	Source        string    `db:"source"`
	ChangesSource *string   `db:"changes_source"`
	ActorID       uuid.UUID `db:"actor_id"`
	ActorType     string    `db:"actor_type"`
}

func (f *finishSourceFixture) moved(t *testing.T, taskID uuid.UUID) []movedRow {
	t.Helper()
	var rows []movedRow
	require.NoError(t, f.db.Select(&rows, `SELECT source, changes->>'source' AS changes_source, actor_id, actor_type::text AS actor_type
		FROM activity_log WHERE entity_type='task' AND entity_id=$1 AND action='task.moved' ORDER BY created_at, new_version`, taskID))
	return rows
}

func (f *finishSourceFixture) counts(t *testing.T, taskID uuid.UUID) (version, activity int64) {
	t.Helper()
	require.NoError(t, f.db.Get(&version, `SELECT version FROM tasks WHERE id=$1`, taskID))
	require.NoError(t, f.db.Get(&activity, `SELECT count(*) FROM activity_log WHERE entity_id=$1`, taskID))
	return version, activity
}

func TestFinishTaskHTTP_AuditKeepsFinishTaskSource_DB(t *testing.T) {
	f := finishSourceSetup(t)
	id := f.task(t)
	actor := actorctx.WithActor(context.Background(), f.agent, domain.ActorTypeAgent)
	session, request := uuid.New(), uuid.New()
	_, err := f.svc.CheckoutTask(actor, id, 30, nil, domain.CheckoutScope{SessionID: &session, RequestID: &request})
	require.NoError(t, err)
	before := len(f.moved(t, id))

	h := NewTypedOutcomeCommandHandler(f.svc.(service.TypedOutcomeCommands), f.svc)
	rec := f.post(t, actor, "/tasks/:task_id/finish", id.String(),
		`{"result_kind":"defect","evidence":"fixture finish for the audit source","to":"done"}`, h.FinishTask)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := f.moved(t, id)
	require.Len(t, rows, before+1, "finish writes one task.moved row")
	last := rows[len(rows)-1]
	assert.Equal(t, "finish_task", last.Source, "activity_log.source")
	require.NotNil(t, last.ChangesSource)
	assert.Equal(t, "finish_task", *last.ChangesSource, "changes.source")
	assert.Equal(t, f.agent, last.ActorID, "the actor stays the calling agent's credential")
	assert.Equal(t, string(domain.ActorTypeAgent), last.ActorType)
}

func TestMoveTaskHTTP_ClientCannotClaimFinishTaskSource_DB(t *testing.T) {
	f := finishSourceSetup(t)
	id := f.task(t)
	user := uuid.New()
	actor := actorctx.WithActor(context.Background(), user, domain.ActorTypeUser)
	h := NewTaskHandler(f.svc)
	move := func(source string) *httptest.ResponseRecorder {
		return f.post(t, actor, "/tasks/:task_id/move", id.String(),
			`{"status_id":"`+f.doing.String()+`","source":"`+source+`"}`, h.MoveTask)
	}

	v0, a0 := f.counts(t, id)
	rec := move("finish_task")
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	v1, a1 := f.counts(t, id)
	assert.Equal(t, v0, v1, "a refused source leaves the task version")
	assert.Equal(t, a0, a1, "a refused source writes no activity")

	// Positive control: an ordinary client producer is kept as declared.
	rec = move("mcp")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rows := f.moved(t, id)
	require.NotEmpty(t, rows)
	last := rows[len(rows)-1]
	assert.Equal(t, "mcp", last.Source)
	require.NotNil(t, last.ChangesSource)
	assert.Equal(t, "mcp", *last.ChangesSource)
	assert.Equal(t, user, last.ActorID)
}
