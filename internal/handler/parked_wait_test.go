package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

type parkedWaitTaskService struct {
	*MockTaskService
	register func(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error)
	release  func(context.Context, uuid.UUID, domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error)
}

func (s *parkedWaitTaskService) RegisterParkedWait(ctx context.Context, id uuid.UUID, plan domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
	return s.register(ctx, id, plan)
}

func (s *parkedWaitTaskService) ReleaseParkedWait(ctx context.Context, id uuid.UUID, input domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error) {
	return s.release(ctx, id, input)
}

func invokeParkedWait(t *testing.T, path, body string, handler func(echo.Context) error) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/parked-waits")
	if strings.Contains(path, "/release") {
		c.SetPath("/tasks/:task_id/parked-waits/release")
	}
	c.SetParamNames("task_id")
	c.SetParamValues(strings.Split(strings.TrimPrefix(path, "/tasks/"), "/")[0])
	err := handler(c)
	require.NoError(t, err)
	return rec
}

func TestRegisterParkedWait_HTTPBehavior(t *testing.T) {
	taskID, registrationID := uuid.New(), uuid.New()
	plan := domain.ParkedWaitPlan{ID: registrationID, ExpectedVersion: 7, Reason: "pipeline", FeedSource: "gitlab"}
	body, err := json.Marshal(plan)
	require.NoError(t, err)

	t.Run("delegates parsed request and returns result", func(t *testing.T) {
		want := &domain.ParkedWaitResult{TaskID: taskID, RegistrationID: registrationID, Version: 8}
		called := false
		svc := &parkedWaitTaskService{MockTaskService: &MockTaskService{}, register: func(ctx context.Context, gotID uuid.UUID, got domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
			called = true
			require.Equal(t, taskID, gotID)
			require.Equal(t, plan, got)
			require.NotNil(t, ctx)
			return want, nil
		}}
		rec := invokeParkedWait(t, "/tasks/"+taskID.String()+"/parked-waits", string(body), NewTaskHandler(svc).RegisterParkedWait)
		require.True(t, called)
		require.Equal(t, http.StatusOK, rec.Code)
		var got domain.ParkedWaitResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, *want, got)
	})

	for _, tc := range []struct {
		name, path, body string
		service          *parkedWaitTaskService
		wantStatus       int
		wantMessage      string
	}{
		{name: "malformed task id", path: "/tasks/nope/parked-waits", body: string(body), service: &parkedWaitTaskService{MockTaskService: &MockTaskService{}, register: func(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
			t.Fatal("must not delegate")
			return nil, nil
		}}, wantStatus: http.StatusBadRequest},
		{name: "malformed JSON", path: "/tasks/" + taskID.String() + "/parked-waits", body: "{", service: &parkedWaitTaskService{MockTaskService: &MockTaskService{}, register: func(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
			t.Fatal("must not delegate")
			return nil, nil
		}}, wantStatus: http.StatusBadRequest, wantMessage: "invalid registration body"},
		{name: "optional service unavailable", path: "/tasks/" + taskID.String() + "/parked-waits", body: string(body), wantStatus: http.StatusServiceUnavailable, wantMessage: "parked wait registration unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			taskSvc := interfaceTaskServiceForParkedWait(tc.service)
			rec := invokeParkedWait(t, tc.path, tc.body, NewTaskHandler(taskSvc).RegisterParkedWait)
			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantMessage != "" {
				require.Contains(t, rec.Body.String(), tc.wantMessage)
			}
		})
	}

	t.Run("maps service conflict to HTTP 409", func(t *testing.T) {
		svc := &parkedWaitTaskService{MockTaskService: &MockTaskService{}, register: func(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
			return nil, apierror.Conflict("snapshot changed")
		}}
		rec := invokeParkedWait(t, "/tasks/"+taskID.String()+"/parked-waits", string(body), NewTaskHandler(svc).RegisterParkedWait)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), "snapshot changed")
	})

	t.Run("maps unexpected service failure to 500", func(t *testing.T) {
		svc := &parkedWaitTaskService{MockTaskService: &MockTaskService{}, register: func(context.Context, uuid.UUID, domain.ParkedWaitPlan) (*domain.ParkedWaitResult, error) {
			return nil, errors.New("storage unavailable")
		}}
		rec := invokeParkedWait(t, "/tasks/"+taskID.String()+"/parked-waits", string(body), NewTaskHandler(svc).RegisterParkedWait)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}

func interfaceTaskServiceForParkedWait(svc *parkedWaitTaskService) service.TaskService {
	if svc != nil {
		return svc
	}
	return &MockTaskService{}
}

func TestReleaseParkedWait_HTTPBehavior(t *testing.T) {
	taskID, registrationID, releaseID := uuid.New(), uuid.New(), uuid.New()
	input := domain.ReleaseParkedWait{RegistrationID: registrationID, ExpectedVersion: 12, ReleaseID: releaseID, Trigger: domain.ParkedWaitTrigger{EventID: "pipeline-42", Kind: "pipeline"}}
	body, err := json.Marshal(input)
	require.NoError(t, err)

	t.Run("delegates parsed request and returns result", func(t *testing.T) {
		want := &domain.ParkedWaitResult{TaskID: taskID, RegistrationID: registrationID, Version: 13, Released: true}
		called := false
		svc := &parkedWaitTaskService{MockTaskService: &MockTaskService{}, release: func(ctx context.Context, gotID uuid.UUID, got domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error) {
			called = true
			require.Equal(t, taskID, gotID)
			require.Equal(t, input, got)
			require.NotNil(t, ctx)
			return want, nil
		}}
		rec := invokeParkedWait(t, "/tasks/"+taskID.String()+"/parked-waits/release", string(body), NewTaskHandler(svc).ReleaseParkedWait)
		require.True(t, called)
		require.Equal(t, http.StatusOK, rec.Code)
		var got domain.ParkedWaitResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, *want, got)
	})

	t.Run("rejects malformed task id before delegation", func(t *testing.T) {
		svc := &parkedWaitTaskService{MockTaskService: &MockTaskService{}, release: func(context.Context, uuid.UUID, domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error) {
			t.Fatal("must not delegate")
			return nil, nil
		}}
		rec := invokeParkedWait(t, "/tasks/bad/parked-waits/release", string(body), NewTaskHandler(svc).ReleaseParkedWait)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
	t.Run("rejects malformed JSON before delegation", func(t *testing.T) {
		svc := &parkedWaitTaskService{MockTaskService: &MockTaskService{}, release: func(context.Context, uuid.UUID, domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error) {
			t.Fatal("must not delegate")
			return nil, nil
		}}
		rec := invokeParkedWait(t, "/tasks/"+taskID.String()+"/parked-waits/release", "{", NewTaskHandler(svc).ReleaseParkedWait)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "invalid release body")
	})
	t.Run("reports missing optional capability", func(t *testing.T) {
		rec := invokeParkedWait(t, "/tasks/"+taskID.String()+"/parked-waits/release", string(body), NewTaskHandler(&MockTaskService{}).ReleaseParkedWait)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Contains(t, rec.Body.String(), "parked wait release unavailable")
	})
	t.Run("maps service conflict to HTTP 409", func(t *testing.T) {
		svc := &parkedWaitTaskService{MockTaskService: &MockTaskService{}, release: func(context.Context, uuid.UUID, domain.ReleaseParkedWait) (*domain.ParkedWaitResult, error) {
			return nil, apierror.Conflict("release version changed")
		}}
		rec := invokeParkedWait(t, "/tasks/"+taskID.String()+"/parked-waits/release", string(body), NewTaskHandler(svc).ReleaseParkedWait)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), "release version changed")
	})
}
