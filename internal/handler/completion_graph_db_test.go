package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type completionHTTPFixture struct {
	db              *sqlx.DB
	project, status uuid.UUID
	handler         *DependencyHandler
}

func completionHTTPSetup(t *testing.T) *completionHTTPFixture {
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
	ctx := context.Background()
	ws := &domain.Workspace{ID: uuid.New(), Name: "Completion graph HTTP", Slug: "graph-http-" + uuid.NewString(), OwnerID: uuid.New()}
	require.NoError(t, postgres.NewWorkspaceRepo(db).Create(ctx, ws))
	t.Cleanup(func() { _, err := db.Exec(`DELETE FROM workspaces WHERE id=$1`, ws.ID); require.NoError(t, err) })
	p := &domain.Project{ID: uuid.New(), WorkspaceID: ws.ID, Name: "Graph", Slug: "graph-" + uuid.NewString(), DefaultAssigneeType: domain.DefaultAssigneeNone}
	require.NoError(t, postgres.NewProjectRepo(db).Create(ctx, p))
	s := &domain.TaskStatus{ID: uuid.New(), ProjectID: p.ID, Name: "Todo", Slug: "todo", Category: domain.StatusCategoryTodo, IsDefault: true, Color: "#000000"}
	require.NoError(t, postgres.NewTaskStatusRepo(db).Create(ctx, s))
	svc := service.NewTaskDependencyService(postgres.NewTaskDependencyRepo(db), postgres.NewTaskRepo(db), postgres.NewActivityLogRepo(db), postgres.NewProjectRepo(db))
	return &completionHTTPFixture{db: db, project: p.ID, status: s.ID, handler: NewDependencyHandler(svc, nil)}
}

func (f *completionHTTPFixture) task(t *testing.T, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	task := &domain.Task{ID: uuid.New(), ProjectID: f.project, StatusID: f.status, Title: "Graph HTTP fixture", ParentTaskID: parent, AssigneeType: domain.AssigneeTypeUnassigned, Priority: domain.PriorityMedium, CreatedBy: uuid.New(), CreatedByType: domain.ActorTypeUser, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, postgres.NewTaskRepo(f.db).Create(context.Background(), task, nil))
	var stored *uuid.UUID
	require.NoError(t, f.db.Get(&stored, `SELECT parent_task_id FROM tasks WHERE id=$1`, task.ID))
	require.Equal(t, parent, stored)
	return task.ID
}

func (f *completionHTTPFixture) post(t *testing.T, from, to uuid.UUID, kind string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"depends_on_task_id": to, "dependency_type": kind})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+from.String()+"/dependencies", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetPath("/tasks/:task_id/dependencies")
	c.SetParamNames("task_id")
	c.SetParamValues(from.String())
	require.NoError(t, f.handler.Create(c))
	return rec
}

func TestCompletionGraphHTTPRejectsMixedCycle(t *testing.T) {
	f := completionHTTPSetup(t)
	p := f.task(t, nil)
	c := f.task(t, &p)
	s := f.task(t, nil)
	rec := f.post(t, c, p, "blocks")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var problem apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Contains(t, problem.Message, "completion cycle")
	require.Equal(t, c.String()+" -> "+p.String()+" -> "+c.String(), problem.Details)
	require.Equal(t, http.StatusCreated, f.post(t, c, s, "blocks").Code)
	rec = f.post(t, s, p, "blocks")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Equal(t, s.String()+" -> "+p.String()+" -> "+c.String()+" -> "+s.String(), problem.Details)
	var count int
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM task_dependencies WHERE (task_id=$1 OR task_id=$2) AND depends_on_task_id=$3 AND dependency_type='blocks'`, c, s, p))
	require.Zero(t, count, "HTTP failures must leave no partial edge")
}

func TestCompletionGraphHTTPPositiveAndTenantControls(t *testing.T) {
	f := completionHTTPSetup(t)
	p := f.task(t, nil)
	c := f.task(t, &p)
	require.Equal(t, http.StatusCreated, f.post(t, p, c, "blocks").Code)
	require.Equal(t, http.StatusCreated, f.post(t, c, p, "relates_to").Code)
	var kind string
	require.NoError(t, f.db.Get(&kind, `SELECT dependency_type FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, p, c))
	require.Equal(t, "blocks", kind)
	foreign := completionHTTPSetup(t).task(t, nil)
	rec := f.post(t, c, foreign, "blocks")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), foreign.String(), "tenant refusal must not disclose the foreign ID")
}

func TestCompletionGraphHTTPBaselineDAGPositive(t *testing.T) {
	f := completionHTTPSetup(t)
	a := f.task(t, nil)
	b := f.task(t, nil)
	c := f.task(t, nil)
	require.Equal(t, http.StatusCreated, f.post(t, a, b, "blocks").Code)
	require.Equal(t, http.StatusCreated, f.post(t, b, c, "blocks").Code)
	var count int
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM task_dependencies WHERE task_id IN ($1,$2) AND dependency_type='blocks'`, a, b))
	require.Equal(t, 2, count)
}

func TestCompletionGraphHTTPAtomicChildOf(t *testing.T) {
	f := completionHTTPSetup(t)
	a := f.task(t, nil)
	b := f.task(t, nil)
	p := f.task(t, nil)
	require.Equal(t, http.StatusCreated, f.post(t, a, b, "blocks").Code)
	require.Equal(t, http.StatusCreated, f.post(t, b, p, "blocks").Code)
	rec := f.post(t, a, p, "is_child_of")
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var problem apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Equal(t, p.String()+" -> "+a.String()+" -> "+b.String()+" -> "+p.String(), problem.Details)
	var count int
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, a, p))
	require.Zero(t, count)
	var parent *uuid.UUID
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, a))
	require.Nil(t, parent)
	x := f.task(t, nil)
	rec = f.post(t, x, p, "is_child_of")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Equal(t, &p, parent)
	var depID uuid.UUID
	require.NoError(t, f.db.Get(&depID, `SELECT id FROM task_dependencies WHERE task_id=$1 AND depends_on_task_id=$2`, x, p))
	require.NoError(t, f.handler.depService.Delete(context.Background(), depID))
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, x))
	require.Nil(t, parent, "deleting the relationship clears the matching hierarchy atomically")
	require.NoError(t, f.db.Get(&count, `SELECT count(*) FROM task_dependencies WHERE id=$1`, depID))
	require.Zero(t, count)
}

func TestCompletionGraphHTTPReverseOrder(t *testing.T) {
	f := completionHTTPSetup(t)
	p := f.task(t, nil)
	c := f.task(t, nil)
	require.Equal(t, http.StatusCreated, f.post(t, c, p, "blocks").Code)
	repo := postgres.NewTaskRepo(f.db)
	task, err := repo.GetByID(context.Background(), c)
	require.NoError(t, err)
	task.ParentTaskID = &p
	err = repo.Update(context.Background(), task)
	require.Error(t, err)
	// Reparent uses the same production error renderer as dependency creation.
	rec := httptest.NewRecorder()
	require.NoError(t, handleError(echo.New().NewContext(httptest.NewRequest(http.MethodPatch, "/tasks/"+c.String(), http.NoBody), rec), err))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	var problem apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &problem))
	require.Equal(t, p.String()+" -> "+c.String()+" -> "+p.String(), problem.Details)
	var parent *uuid.UUID
	require.NoError(t, f.db.Get(&parent, `SELECT parent_task_id FROM tasks WHERE id=$1`, c))
	require.Nil(t, parent)
}
