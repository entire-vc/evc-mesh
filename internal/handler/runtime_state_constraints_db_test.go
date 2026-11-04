package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Real handlers over real PostgreSQL: the same constraints must come back from
// the card (detail) and the board (project list), with a stored producer state
// and with none.
type constraintsDB struct {
	*completionHTTPFixture
	done uuid.UUID
	h    *TaskHandler
}

func newConstraintsDB(t *testing.T) *constraintsDB {
	t.Helper()
	f := completionHTTPSetup(t)
	doneStatus := &domain.TaskStatus{ID: uuid.New(), ProjectID: f.project, Name: "Done", Slug: "done", Category: domain.StatusCategoryDone, Color: "#000000"}
	require.NoError(t, postgres.NewTaskStatusRepo(f.db).Create(context.Background(), doneStatus))
	depRepo := postgres.NewTaskDependencyRepo(f.db)
	svc := service.NewTaskService(postgres.NewTaskRepo(f.db), postgres.NewTaskStatusRepo(f.db), depRepo, postgres.NewActivityLogRepo(f.db))
	return &constraintsDB{completionHTTPFixture: f, done: doneStatus.ID, h: NewTaskHandler(svc).WithDependencyReader(depRepo)}
}

func (f *constraintsDB) withStatus(t *testing.T, id, status uuid.UUID) {
	t.Helper()
	_, err := f.db.Exec(`UPDATE tasks SET status_id=$1 WHERE id=$2`, status, id)
	require.NoError(t, err)
}

func (f *constraintsDB) edge(t *testing.T, from, to uuid.UUID, kind string) {
	t.Helper()
	require.Equal(t, http.StatusCreated, f.post(t, from, to, kind).Code)
}

func (f *constraintsDB) call(t *testing.T, may func(uuid.UUID) bool, do func(c echo.Context) error, names, values []string, method, body string) []byte {
	t.Helper()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	mw.SetProjectAccess(c, mw.ProjectAccessCheck{Task: may, Project: func(uuid.UUID) bool { return true }})
	require.NoError(t, do(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.Bytes()
}

func allow(uuid.UUID) bool { return true }

func (f *constraintsDB) detail(t *testing.T, id uuid.UUID, may func(uuid.UUID) bool) (state *domain.RuntimeState, text string) {
	t.Helper()
	raw := f.call(t, may, f.h.GetByID, []string{"task_id"}, []string{id.String()}, http.MethodGet, "")
	var task domain.Task
	require.NoError(t, json.Unmarshal(raw, &task))
	return task.RuntimeState, string(raw)
}

func (f *constraintsDB) board(t *testing.T, id uuid.UUID, may func(uuid.UUID) bool) (state *domain.RuntimeState, text string) {
	t.Helper()
	raw := f.call(t, may, f.h.List, []string{"proj_id"}, []string{f.project.String()}, http.MethodGet, "")
	var page struct {
		Items []domain.Task `json:"items"`
	}
	require.NoError(t, json.Unmarshal(raw, &page))
	for _, it := range page.Items {
		if it.ID == id {
			return it.RuntimeState, string(raw)
		}
	}
	t.Fatalf("task %s not on the board page", id)
	return nil, ""
}

func constraintIDs(rs *domain.RuntimeState) []string {
	var out []string
	if rs != nil {
		for _, c := range rs.Constraints {
			out = append(out, c.ID)
		}
	}
	return out
}

func (f *constraintsDB) putWait(t *testing.T, id uuid.UUID) {
	t.Helper()
	body := `{"kind":"waiting","ref":"pipeline:g/p#1","constraints":[{"id":"runner:w1","class":"local_wait","reason_code":"wait_pipeline",
	"wake":{"predicate":"pipeline","ref":"g/p#1"},"source":{"kind":"runner"},"observed_at":"` + time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) + `"}]}`
	f.call(t, allow, f.h.PutRuntimeState, []string{"task_id"}, []string{id.String()}, http.MethodPut, body)
}

// F03 / F08: blocks + a stored local WAIT come back from both reads; fulfilled
// blocks and relates_to do not; closed cards show nothing.
func TestRuntimeStateConstraintsDB_DetailAndBoardAgree(t *testing.T) {
	f := newConstraintsDB(t)
	task, live, fulfilled, related := f.task(t, nil), f.task(t, nil), f.task(t, nil), f.task(t, nil)
	f.withStatus(t, fulfilled, f.done)
	f.edge(t, task, live, "blocks")
	f.edge(t, task, fulfilled, "blocks")
	f.edge(t, task, related, "relates_to")
	f.putWait(t, task)

	d, _ := f.detail(t, task, allow)
	b, _ := f.board(t, task, allow)
	require.NotNil(t, d)
	require.NotNil(t, b)
	require.Len(t, d.Constraints, 2, "live blocker + stored local wait")
	assert.ElementsMatch(t, constraintIDs(d), constraintIDs(b), "board and detail carry the same constraints")
	assert.Contains(t, constraintIDs(d), "runner:w1")
	raw, _ := json.Marshal(d)
	assert.NotContains(t, string(raw), fulfilled.String())
	assert.NotContains(t, string(raw), related.String())
}

// Blocks with no stored state: the dependency still shows, in detail and on the board.
func TestRuntimeStateConstraintsDB_NilRuntimeStateStillShowsDependency(t *testing.T) {
	f := newConstraintsDB(t)
	task, live := f.task(t, nil), f.task(t, nil)
	f.edge(t, task, live, "blocks")
	d, _ := f.detail(t, task, allow)
	b, _ := f.board(t, task, allow)
	for name, rs := range map[string]*domain.RuntimeState{"detail": d, "board": b} {
		require.NotNil(t, rs, name)
		assert.Equal(t, domain.RuntimeStateSourceDependency, rs.Source, name)
		require.Len(t, rs.Constraints, 1, name)
		assert.Equal(t, domain.ConstraintMeshDependency, rs.Constraints[0].Class, name)
	}
	// negative control: no edge, no state, nothing invented
	lone := f.task(t, nil)
	d2, _ := f.detail(t, lone, allow)
	assert.Nil(t, d2)
	// closed card keeps no active blocker
	closed := f.task(t, nil)
	f.edge(t, closed, live, "blocks")
	f.withStatus(t, closed, f.done)
	d3, _ := f.detail(t, closed, allow)
	assert.Nil(t, d3)
}

// F10: a blocker the reader may not open stays as unknown and is not leaked, on both reads.
func TestRuntimeStateConstraintsDB_ClosedBlockerRedactedOnBothReads(t *testing.T) {
	f := newConstraintsDB(t)
	task, secret := f.task(t, nil), f.task(t, nil)
	f.edge(t, task, secret, "blocks")
	deny := func(id uuid.UUID) bool { return id != secret }
	d, _ := f.detail(t, task, deny)
	b, _ := f.board(t, task, deny)
	// the board page legitimately lists the blocker card itself, so the leak check
	// is scoped to the target card's own runtime_state.
	for name, rs := range map[string]*domain.RuntimeState{"detail": d, "board": b} {
		require.NotNil(t, rs, name)
		enc, err := json.Marshal(rs)
		require.NoError(t, err)
		assert.NotContains(t, string(enc), secret.String(), name)
		assert.Contains(t, string(enc), "dependency_not_visible", name)
	}
	assert.Equal(t, constraintIDs(d), constraintIDs(b))
	// positive control: an authorised reader sees the ref
	_, rawOK := f.detail(t, task, allow)
	assert.Contains(t, rawOK, secret.String())
}
