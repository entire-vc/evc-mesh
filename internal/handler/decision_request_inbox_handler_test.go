package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The HTTP contract of GET /agents/me/decision-requests. The store is a
// recorder; the SQL (scope, order, filters) is tested in the postgres package.
type recordedInbox struct {
	items     []domain.InboxDecisionRequest
	err       error
	called    bool
	agent, ws uuid.UUID
	limit     int
}

func (r *recordedInbox) ListOpenForAgent(_ context.Context, agentID, workspaceID uuid.UUID, limit int) ([]domain.InboxDecisionRequest, error) {
	r.called, r.agent, r.ws, r.limit = true, agentID, workspaceID, limit
	return r.items, r.err
}

// callInbox runs the handler with the context the agent-key middleware sets;
// a nil agent or workspace leaves that key unset.
func callInbox(t *testing.T, store *recordedInbox, query string, agent, ws any) (rec *httptest.ResponseRecorder, body []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/agents/me/decision-requests"+query, http.NoBody)
	rec = httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	if agent != nil {
		c.Set(mw.ContextKeyAgentID, agent)
	}
	if ws != nil {
		c.Set(mw.ContextKeyAgentAuthWorkspaceID, ws)
	}
	require.NoError(t, NewDecisionRequestInboxHandler(store).ListMine(c))
	return rec, rec.Body.Bytes()
}

func TestDecisionRequestInbox_ListsTheCallersOpenRequests(t *testing.T) {
	agent, ws := uuid.New(), uuid.New()
	created := time.Date(2026, 11, 11, 9, 30, 0, 0, time.UTC)
	item := domain.InboxDecisionRequest{
		ID: uuid.New(), TaskID: uuid.New(), Question: "v1 or v2?", State: domain.DecisionOpen, CreatedAt: created,
		FromAgent: domain.AgentRef{ID: uuid.New(), Name: "Garfield", Slug: "garfield"},
	}
	item.Task = domain.DecisionRequestTask{ID: item.TaskID, Title: "Schema", ProjectID: uuid.New(),
		WorkspaceID: ws, StatusCategory: domain.StatusCategoryInProgress}
	store := &recordedInbox{items: []domain.InboxDecisionRequest{item}}

	rec, body := callInbox(t, store, "", agent, ws)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, agent, store.agent)
	assert.Equal(t, ws, store.ws, "the workspace is the one the key authenticated into")
	assert.Equal(t, DefaultDecisionInboxLimit, store.limit)
	assert.JSONEq(t, `{"items":[{
		"id":"`+item.ID.String()+`","task_id":"`+item.TaskID.String()+`","question":"v1 or v2?",
		"state":"open","created_at":"2026-11-11T09:30:00Z",
		"from_agent":{"id":"`+item.FromAgent.ID.String()+`","name":"Garfield","slug":"garfield"},
		"task":{"id":"`+item.TaskID.String()+`","title":"Schema","project_id":"`+item.Task.ProjectID.String()+`",
		        "workspace_id":"`+ws.String()+`","status_category":"in_progress"}}]}`, string(body))

	rec, _ = callInbox(t, store, "?state=open&limit=5", agent, ws)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 5, store.limit)
	rec, _ = callInbox(t, store, "?limit=1000", agent, ws)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, MaxDecisionInboxLimit, store.limit, "a limit above the maximum is capped")

	empty := &recordedInbox{}
	rec, body = callInbox(t, empty, "", agent, ws)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"items":[]}`, string(body))
}

func TestDecisionRequestInbox_Refusals(t *testing.T) {
	agent, ws := uuid.New(), uuid.New()
	cases := []struct {
		name   string
		query  string
		agent  any
		ws     any
		err    error
		status int
		code   string
	}{
		{"a closed state", "?state=answered", agent, ws, nil, http.StatusBadRequest, apierror.CodeValidationFailed},
		{"an unknown state", "?state=all", agent, ws, nil, http.StatusBadRequest, apierror.CodeValidationFailed},
		{"a zero limit", "?limit=0", agent, ws, nil, http.StatusBadRequest, apierror.CodeValidationFailed},
		{"a non-numeric limit", "?limit=ten", agent, ws, nil, http.StatusBadRequest, apierror.CodeValidationFailed},
		{"no agent key", "", nil, ws, nil, http.StatusUnauthorized, apierror.CodeUnauthorized},
		{"a malformed agent id", "", "not-a-uuid", ws, nil, http.StatusBadRequest, ""},
		{"no authenticated workspace", "", agent, nil, nil, http.StatusForbidden, ""},
		{"a nil authenticated workspace", "", agent, uuid.Nil, nil, http.StatusForbidden, ""},
		{"the store fails", "", agent, ws, errors.New("db down"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordedInbox{err: tc.err}
			rec, body := callInbox(t, store, tc.query, tc.agent, tc.ws)
			assert.Equal(t, tc.status, rec.Code)
			if tc.code != "" {
				var out map[string]any
				require.NoError(t, json.Unmarshal(body, &out))
				assert.Equal(t, tc.code, out["error_code"])
			}
			assert.Equal(t, tc.err != nil, store.called, "a refused request reads nothing")
		})
	}
}
