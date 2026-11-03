package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// End-to-end test of the receipt-bound product done path: real Postgres, the
// real repo/service/handler, only the transport is httptest. No build tag; it
// skips when no Postgres is reachable (CI provides one), like the other
// *_e2e_db_test.go files in this package.

const receiptHead = "cccccccccccccccccccccccccccccccccccccccc"

type receiptE2E struct {
	t        *testing.T
	db       *sqlx.DB
	e        *echo.Echo
	taskID   uuid.UUID
	projID   uuid.UUID
	todoID   uuid.UUID
	doneID   uuid.UUID
	linkID   uuid.UUID
	producer uuid.UUID
	acceptor uuid.UUID
}

func newReceiptE2E(t *testing.T) *receiptE2E {
	t.Helper()
	db := oauthE2ETestDB(t)
	r := &receiptE2E{t: t, db: db, taskID: uuid.New(), projID: uuid.New(), todoID: uuid.New(), doneID: uuid.New(),
		linkID: uuid.New(), producer: uuid.New(), acceptor: uuid.New()}
	ws := uuid.New()
	r.exec(`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'receipt e2e',$2,$3)`, ws, ws.String(), uuid.New())
	r.exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES ($1, $2, 'p', $3)`, r.projID, ws, r.projID.String()[:8])
	r.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Todo','todo','todo'::task_status_category,0)`, r.todoID, r.projID)
	r.exec(`INSERT INTO task_statuses (id, project_id, name, slug, category, position) VALUES ($1,$2,'Done','done','done'::task_status_category,1)`, r.doneID, r.projID)
	r.exec(`INSERT INTO tasks (id, project_id, status_id, title, task_number, created_by) VALUES ($1,$2,$3,'receipt task',1,$4)`, r.taskID, r.projID, r.todoID, uuid.New())
	r.exec(`INSERT INTO vcs_links(id,task_id,provider,link_type,external_id,url,status,metadata) VALUES($1,$2,'gitlab','pr','9','https://git.example/mr/9','merged',$3::jsonb)`,
		r.linkID, r.taskID, `{"head_sha":"`+receiptHead+`","ship_sha":"`+receiptHead+`"}`)

	taskRepo := postgres.NewTaskRepo(db)
	taskSvc := service.NewTaskService(taskRepo, postgres.NewTaskStatusRepo(db), postgres.NewTaskDependencyRepo(db), postgres.NewActivityLogRepo(db))
	h := NewTransitionReceiptHandler(service.NewTransitionReceiptService(postgres.NewTransitionReceiptRepo(db), taskRepo), taskSvc)

	r.e = echo.New()
	r.e.HTTPErrorHandler = NewHTTPErrorHandler(r.e.DefaultHTTPErrorHandler)
	// The credential actor comes from the X-Test-Actor header, standing in for auth.
	api := r.e.Group("/api/v1", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if a, err := uuid.Parse(c.Request().Header.Get("X-Test-Actor")); err == nil {
				c.SetRequest(c.Request().WithContext(actorctx.WithActor(c.Request().Context(), a, domain.ActorTypeAgent)))
			}
			return next(c)
		}
	})
	api.POST("/tasks/:task_id/transitions/product-done", h.ProductDone)
	api.GET("/tasks/:task_id/transition-receipts", h.ListReceipts)
	api.GET("/tasks/:task_id/product-done-acceptance", h.ProductDoneAcceptance)
	api.GET("/projects/:proj_id/product-done-capabilities", h.Capabilities)
	api.GET("/projects/:proj_id/transition-receipt-policy", h.GetPolicy)
	api.PUT("/projects/:proj_id/transition-receipt-policy", h.SetPolicy)
	api.POST("/tasks/:task_id/acceptance-intents", h.RecordAcceptance)
	api.GET("/tasks/:task_id/acceptance-intents", h.ListAcceptances)
	api.POST("/tasks/:task_id/acceptance-intents/:intent_id/revoke", h.RevokeAcceptance)
	return r
}

func (r *receiptE2E) exec(q string, args ...any) {
	r.t.Helper()
	_, err := r.db.Exec(q, args...)
	require.NoError(r.t, err)
}

func (r *receiptE2E) do(method, path string, actor uuid.UUID, body any) (status int, resp map[string]any) {
	r.t.Helper()
	var buf bytes.Buffer
	if s, ok := body.(string); ok {
		buf.WriteString(s)
	} else if body != nil {
		require.NoError(r.t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, "/api/v1"+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if actor != uuid.Nil {
		req.Header.Set("X-Test-Actor", actor.String())
	}
	rec := httptest.NewRecorder()
	r.e.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func (r *receiptE2E) enablePolicy() {
	r.t.Helper()
	code, _ := r.do(http.MethodPut, "/projects/"+r.projID.String()+"/transition-receipt-policy", r.producer, map[string]any{
		"enabled":            true,
		"allowed_producers":  []map[string]any{{"producer_id": "board", "actor_id": r.producer}},
		"allowed_acceptors":  []uuid.UUID{r.acceptor},
		"allowed_exemptions": []string{"research"},
	})
	require.Equal(r.t, http.StatusOK, code)
}

func (r *receiptE2E) version() int64 {
	var v int64
	require.NoError(r.t, r.db.Get(&v, `SELECT version FROM tasks WHERE id=$1`, r.taskID))
	return v
}

func (r *receiptE2E) body(key string) map[string]any {
	return map[string]any{
		"domain": "product", "class": "code", "operation": "product_done",
		"to_status_id": r.doneID, "expected_status_id": r.todoID, "expected_version": r.version(),
		"idempotency_key": key, "producer_id": "board", "accepting_actor_id": r.acceptor,
		"reason": "accepted on exact head", "evidence": map[string]any{"vcs_link_id": r.linkID, "head_sha": receiptHead},
	}
}

func (r *receiptE2E) statusIsDone() bool {
	var id uuid.UUID
	require.NoError(r.t, r.db.Get(&id, `SELECT status_id FROM tasks WHERE id=$1`, r.taskID))
	return id == r.doneID
}

func TestTransitionReceiptE2E_PolicyOffIsRefused(t *testing.T) {
	r := newReceiptE2E(t)
	path := "/tasks/" + r.taskID.String() + "/transitions/product-done"

	// Control: with no policy row the project is off and nothing changes.
	code, out := r.do(http.MethodPost, path, r.producer, r.body("off"))
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "transition_policy_disabled", out["code"])
	require.False(t, r.statusIsDone())

	code, out = r.do(http.MethodGet, "/projects/"+r.projID.String()+"/transition-receipt-policy", r.producer, nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["enabled"])
}

func TestTransitionReceiptE2E_CommitReplayAndRefusals(t *testing.T) {
	r := newReceiptE2E(t)
	r.enablePolicy()
	path := "/tasks/" + r.taskID.String() + "/transitions/product-done"

	// Refusals first, each leaves the task untouched.
	code, _ := r.do(http.MethodPost, path, uuid.Nil, r.body("anon"))
	require.Equal(t, http.StatusUnauthorized, code)

	b := r.body("no-version")
	delete(b, "expected_version")
	code, _ = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusBadRequest, code)

	code, _ = r.do(http.MethodPost, path, r.producer, `{"unknown_field":1}`)
	require.Equal(t, http.StatusBadRequest, code)

	b = r.body("stale")
	b["expected_version"] = r.version() - 1
	code, out := r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusConflict, code, "%v", out)

	b = r.body("moved-head")
	b["evidence"] = map[string]any{"vcs_link_id": r.linkID, "head_sha": "dddddddddddddddddddddddddddddddddddddddd"}
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "evidence_head_changed", out["code"])

	code, out = r.do(http.MethodPost, path, uuid.New(), r.body("other-actor"))
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "producer_not_authorized", out["code"])
	require.False(t, r.statusIsDone())

	b = r.body("bad-acceptor")
	b["accepting_actor_id"] = uuid.New()
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "acceptor_not_authorized", out["code"])

	b = r.body("not-done")
	b["to_status_id"] = r.todoID
	code, out = r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "target_not_done_status", out["code"])

	r.exec(`UPDATE tasks SET human_gate=true WHERE id=$1`, r.taskID)
	code, out = r.do(http.MethodPost, path, r.producer, r.body("gated"))
	require.Equal(t, http.StatusUnprocessableEntity, code)
	require.Equal(t, "human_gate_active", out["code"])
	r.exec(`UPDATE tasks SET human_gate=false WHERE id=$1`, r.taskID)
	require.False(t, r.statusIsDone())

	// The valid request commits and returns a receipt.
	ok := r.body("good")
	code, out = r.do(http.MethodPost, path, r.producer, ok)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, false, out["replayed"])
	receipt := out["receipt"].(map[string]any)
	require.True(t, r.statusIsDone())

	// Same request again, version has moved: same receipt, replayed.
	code, out = r.do(http.MethodPost, path, r.producer, ok)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["replayed"])
	require.Equal(t, receipt["id"], out["receipt"].(map[string]any)["id"])

	// Same key, other payload: conflict.
	changed := r.body("good")
	changed["expected_version"] = ok["expected_version"]
	changed["reason"] = "something else"
	code, out = r.do(http.MethodPost, path, r.producer, changed)
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "idempotency_key_payload_mismatch", out["code"])

	code, out = r.do(http.MethodGet, "/tasks/"+r.taskID.String()+"/transition-receipts", r.producer, nil)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, out["receipts"], 1)
}

func TestTransitionReceiptE2E_PolicyValidation(t *testing.T) {
	r := newReceiptE2E(t)
	p := "/projects/" + r.projID.String() + "/transition-receipt-policy"
	code, _ := r.do(http.MethodPut, p, r.producer, map[string]any{"enabled": true, "allowed_exemptions": []string{"code"}})
	require.Equal(t, http.StatusBadRequest, code, "only research/trivial may be exempted")
	code, _ = r.do(http.MethodPut, p, uuid.Nil, map[string]any{"enabled": true})
	require.Equal(t, http.StatusUnauthorized, code)
	code, _ = r.do(http.MethodGet, "/projects/not-a-uuid/transition-receipt-policy", r.producer, nil)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestTransitionReceiptE2E_ResearchExemption(t *testing.T) {
	r := newReceiptE2E(t)
	r.enablePolicy()
	path := "/tasks/" + r.taskID.String() + "/transitions/product-done"
	b := r.body("research")
	b["class"] = "research"
	b["evidence"] = map[string]any{"exemption_type": "research", "artifact_ref": "doc:abc@v1"}
	code, out := r.do(http.MethodPost, path, r.producer, b)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, "exemption", out["receipt"].(map[string]any)["evidence_kind"])
}
