package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

func postMoveBatch(t *testing.T, svc *MockTaskService, body string, withWorkspace uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	h, e := setupTaskTest(svc)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if withWorkspace != uuid.Nil {
		c.Set(mw.ContextKeyWorkspaceID, withWorkspace)
	}
	require.NoError(t, h.MoveToProjectBatch(c))
	return rec
}

func TestTaskHandler_MoveToProjectBatch_PassesParsedInputAndWorkspace(t *testing.T) {
	ws, proj, task, src, dst := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var got domain.MoveBatchInput
	svc := &MockTaskService{MoveToProjectBatchFunc: func(_ context.Context, in domain.MoveBatchInput) (*domain.MoveBatchResult, error) {
		got = in
		return &domain.MoveBatchResult{DryRun: in.DryRun}, nil
	}}
	body := fmt.Sprintf(`{"project_id":%q,"task_ids":[%q],"status_mapping":{%q:%q},"leaf_mode":"detach_parent_keep_blocks","dry_run":true}`,
		proj, task, src, dst)
	rec := postMoveBatch(t, svc, body, ws)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, ws, got.WorkspaceID, "workspace comes from the authenticated context, not the body")
	assert.Equal(t, proj, got.ProjectID)
	assert.Equal(t, []uuid.UUID{task}, got.TaskIDs)
	assert.Equal(t, dst, got.StatusMapping[src])
	assert.Equal(t, domain.MoveLeafDetachKeepBlocks, got.LeafMode)
	assert.True(t, got.DryRun)
	assert.Contains(t, rec.Body.String(), `"dry_run":true`)
}

func TestTaskHandler_MoveToProjectBatch_RejectsBadInputBeforeService(t *testing.T) {
	ws, id := uuid.New(), uuid.New().String()
	called := false
	svc := &MockTaskService{MoveToProjectBatchFunc: func(context.Context, domain.MoveBatchInput) (*domain.MoveBatchResult, error) {
		called = true
		return nil, nil
	}}
	cases := map[string]struct {
		body string
		ws   uuid.UUID
		code int
	}{
		"malformed json":    {`{`, ws, http.StatusBadRequest},
		"no workspace":      {fmt.Sprintf(`{"project_id":%q}`, id), uuid.Nil, http.StatusForbidden},
		"bad project id":    {`{"project_id":"nope"}`, ws, http.StatusBadRequest},
		"short task id":     {fmt.Sprintf(`{"project_id":%q,"task_ids":["95051680"]}`, id), ws, http.StatusBadRequest},
		"bad mapping key":   {fmt.Sprintf(`{"project_id":%q,"task_ids":[%q],"status_mapping":{"x":%q}}`, id, id, id), ws, http.StatusBadRequest},
		"bad mapping value": {fmt.Sprintf(`{"project_id":%q,"task_ids":[%q],"status_mapping":{%q:"y"}}`, id, id, id), ws, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postMoveBatch(t, svc, tc.body, tc.ws)
			assert.Equal(t, tc.code, rec.Code)
		})
	}
	assert.False(t, called, "the service must not run on invalid input")
}

func TestTaskHandler_MoveToProjectBatch_ServiceErrorIsMapped(t *testing.T) {
	ws, id := uuid.New(), uuid.New().String()
	svc := &MockTaskService{MoveToProjectBatchFunc: func(context.Context, domain.MoveBatchInput) (*domain.MoveBatchResult, error) {
		return nil, apierror.Unprocessable("no status mapping", "")
	}}
	rec := postMoveBatch(t, svc, fmt.Sprintf(`{"project_id":%q,"task_ids":[%q]}`, id, id), ws)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}
