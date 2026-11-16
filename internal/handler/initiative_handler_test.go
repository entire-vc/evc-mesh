package handler

import (
	"context"
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
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// recordingInitiatives records what the handler hands to the service; the
// service itself is covered against Postgres in internal/service.
type recordingInitiatives struct {
	service.InitiativeService
	update *service.UpdateInitiativeInput
	viewer *domain.ProjectViewer
	err    error
}

func (r *recordingInitiatives) Update(_ context.Context, id uuid.UUID, in service.UpdateInitiativeInput) (*domain.Initiative, error) {
	r.update = &in
	return &domain.Initiative{ID: id}, r.err
}

func (r *recordingInitiatives) GetByID(_ context.Context, id uuid.UUID, v domain.ProjectViewer) (*domain.Initiative, error) {
	r.viewer = &v
	if r.err != nil {
		return nil, r.err
	}
	return &domain.Initiative{ID: id}, nil
}

func (r *recordingInitiatives) List(_ context.Context, _ uuid.UUID, v domain.ProjectViewer) ([]domain.Initiative, error) {
	r.viewer = &v
	return nil, r.err
}

func initiativeCall(t *testing.T, h echo.HandlerFunc, param, value, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames(param)
	c.SetParamValues(value)
	c.Set(mw.ContextKeyUserID, uuid.New())
	c.Set(mw.ContextKeyWorkspaceRole, domain.RoleViewer)
	require.NoError(t, h(c))
	return rec
}

func TestInitiativeHandler_Update_TargetDate(t *testing.T) {
	id := uuid.NewString()

	t.Run("null clears the deadline", func(t *testing.T) {
		svc := &recordingInitiatives{}
		rec := initiativeCall(t, NewInitiativeHandler(svc).Update, "init_id", id, `{"target_date":null}`)
		assert.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, svc.update)
		assert.True(t, svc.update.ClearTargetDate)
		assert.Nil(t, svc.update.TargetDate)
	})

	t.Run("an absent date leaves it, a date sets it", func(t *testing.T) {
		svc := &recordingInitiatives{}
		initiativeCall(t, NewInitiativeHandler(svc).Update, "init_id", id, `{"name":"x"}`)
		assert.False(t, svc.update.ClearTargetDate)
		initiativeCall(t, NewInitiativeHandler(svc).Update, "init_id", id, `{"target_date":"2026-12-01"}`)
		assert.False(t, svc.update.ClearTargetDate)
		if assert.NotNil(t, svc.update.TargetDate) {
			assert.Equal(t, "2026-12-01", svc.update.TargetDate.Format("2006-01-02"))
		}
	})

	t.Run("a malformed date is a 400 and reaches no service", func(t *testing.T) {
		svc := &recordingInitiatives{}
		rec := initiativeCall(t, NewInitiativeHandler(svc).Update, "init_id", id, `{"target_date":"01.12.2026"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Nil(t, svc.update)
	})

	t.Run("a service error is passed through", func(t *testing.T) {
		svc := &recordingInitiatives{err: apierror.NotFound("Initiative")}
		rec := initiativeCall(t, NewInitiativeHandler(svc).Update, "init_id", id, `{"name":"x"}`)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

// A viewer's read goes to the service with the viewer's own project rights,
// and a refusal from the service reaches the client instead of an empty 200.
func TestInitiativeHandler_ReadsCarryTheViewer(t *testing.T) {
	for name, call := range map[string]func(h *InitiativeHandler, id string) *httptest.ResponseRecorder{
		"GetByID": func(h *InitiativeHandler, id string) *httptest.ResponseRecorder {
			return initiativeCall(t, h.GetByID, "init_id", id, "")
		},
		"List": func(h *InitiativeHandler, id string) *httptest.ResponseRecorder {
			return initiativeCall(t, h.List, "ws_id", id, "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &recordingInitiatives{}
			assert.Equal(t, http.StatusOK, call(NewInitiativeHandler(svc), uuid.NewString()).Code)
			if assert.NotNil(t, svc.viewer) {
				assert.False(t, svc.viewer.AllProjects)
			}

			svc = &recordingInitiatives{err: apierror.NotFound("Initiative")}
			assert.Equal(t, http.StatusNotFound, call(NewInitiativeHandler(svc), uuid.NewString()).Code)
		})
	}
}
