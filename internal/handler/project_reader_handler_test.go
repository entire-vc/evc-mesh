package handler

import (
	"context"
	"encoding/json"
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
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
)

// Only the methods Issue calls are implemented; any other call panics on the
// nil embedded interface, which is what this test wants to notice.
type readerHandlerWorkspaces struct {
	repository.WorkspaceRepository
	ws *domain.Workspace
}

func (r readerHandlerWorkspaces) GetByID(_ context.Context, id uuid.UUID) (*domain.Workspace, error) {
	if r.ws != nil && r.ws.ID == id {
		return r.ws, nil
	}
	return nil, nil
}

type readerHandlerAgents struct{ repository.AgentRepository }

func (readerHandlerAgents) GetBySlug(context.Context, uuid.UUID, string) (*domain.Agent, error) {
	return nil, nil
}

type readerHandlerTx struct{ got []*domain.Agent }

func (r *readerHandlerTx) CreateProjectReader(_ context.Context, a *domain.Agent) error {
	r.got = append(r.got, a)
	return nil
}

func serveIssueReader(t *testing.T, wsParam, body string, userID *uuid.UUID) (*httptest.ResponseRecorder, *readerHandlerTx, *domain.Workspace) {
	t.Helper()
	ws := &domain.Workspace{ID: uuid.New(), Slug: "acme"}
	if wsParam == "" {
		wsParam = ws.ID.String()
	}
	tx := &readerHandlerTx{}
	h := NewProjectReaderHandler(service.NewProjectReaderService(
		readerHandlerWorkspaces{ws: ws}, nil, readerHandlerAgents{}, tx))

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+wsParam+"/project-readers", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("ws_id")
	c.SetParamValues(wsParam)
	if userID != nil {
		c.Set(mw.ContextKeyUserID, *userID)
	}
	if err := h.Issue(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec, tx, ws
}

func TestProjectReaderHandler_IssueAnswersTheKeyOnceNotCached(t *testing.T) {
	admin := uuid.New()
	project := uuid.New()
	rec, tx, _ := serveIssueReader(t, "", `{"name":"Consent Reader","project_id":"`+project.String()+`"}`, &admin)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Len(t, tx.got, 1)
	require.NotNil(t, tx.got[0].SupervisorUserID)
	assert.Equal(t, admin, *tx.got[0].SupervisorUserID, "the issuing admin is not recorded")

	var out struct {
		APIKey string `json:"api_key"`
		Agent  struct {
			ReaderProjectID string `json:"reader_project_id"`
		} `json:"agent"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.NotEmpty(t, out.APIKey)
	assert.Equal(t, project.String(), out.Agent.ReaderProjectID)
}

func TestProjectReaderHandler_IssueRefusals(t *testing.T) {
	project := uuid.New().String()
	cases := []struct {
		name, ws, body string
		status         int
	}{
		{"malformed workspace id", "not-a-uuid", `{"name":"r","project_id":"` + project + `"}`, http.StatusBadRequest},
		{"malformed body", "", `{"name":`, http.StatusBadRequest},
		{"malformed project id", "", `{"name":"r","project_id":"nope"}`, http.StatusBadRequest},
		{"missing project id", "", `{"name":"r"}`, http.StatusBadRequest},
		{"missing name", "", `{"project_id":"` + project + `"}`, http.StatusBadRequest},
		{"unknown workspace", uuid.New().String(), `{"name":"r","project_id":"` + project + `"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, tx, _ := serveIssueReader(t, tc.ws, tc.body, nil)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Empty(t, tx.got, "a refused request reached the issuing transaction")
			assert.NotContains(t, rec.Body.String(), "api_key")
		})
	}
}
