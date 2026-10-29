package handler

import (
	"context"
	"encoding/json"
	"errors"
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
)

type startAfterTaskService struct {
	*MockTaskService
	clear func(context.Context, uuid.UUID, domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error)
}

func (s *startAfterTaskService) CompareClearStartAfter(ctx context.Context, id uuid.UUID, in domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error) {
	return s.clear(ctx, id, in)
}

func invokeCompareClear(t *testing.T, id, body string, h *TaskHandler) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+id+"/start-after/compare-clear", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/start-after/compare-clear")
	c.SetParamNames("task_id")
	c.SetParamValues(id)
	require.NoError(t, h.CompareClearStartAfter(c))
	return rec
}

func TestCompareClearStartAfter_HTTP(t *testing.T) {
	taskID := uuid.New()
	body := `{"expected_start_after":"2026-11-01T06:00:00Z","expected_version":4,"expected_origin":"agent_evidence","idempotency_key":"k1"}`

	t.Run("passes the request and returns the receipt", func(t *testing.T) {
		receipt := uuid.New()
		svc := &startAfterTaskService{MockTaskService: &MockTaskService{}, clear: func(_ context.Context, id uuid.UUID, in domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error) {
			require.Equal(t, taskID, id)
			require.EqualValues(t, 4, *in.ExpectedVersion)
			require.Equal(t, "k1", in.IdempotencyKey)
			require.True(t, in.ExpectedStartAfter.Equal(time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)))
			return &domain.StartAfterClearResult{Receipt: domain.StartAfterReceipt{ID: receipt, Op: "clear"}, Version: 5}, nil
		}}
		rec := invokeCompareClear(t, taskID.String(), body, NewTaskHandler(svc))
		require.Equal(t, http.StatusOK, rec.Code)
		var got domain.StartAfterClearResult
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, receipt, got.Receipt.ID)
		assert.EqualValues(t, 5, got.Version)
	})

	t.Run("conflict carries the current values", func(t *testing.T) {
		origin := domain.StartAfterHumanExplicit
		at := time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC)
		svc := &startAfterTaskService{MockTaskService: &MockTaskService{}, clear: func(context.Context, uuid.UUID, domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error) {
			return nil, &domain.StartAfterConflict{CurrentVersion: 6, CurrentStartAfter: &at, CurrentOrigin: &origin}
		}}
		rec := invokeCompareClear(t, taskID.String(), body, NewTaskHandler(svc))
		require.Equal(t, http.StatusConflict, rec.Code)
		var got map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, "cas_conflict", got["error_code"])
		assert.EqualValues(t, 6, got["current_version"])
		assert.Equal(t, "human_explicit", got["current_origin"])
		assert.Equal(t, "2026-11-03T06:00:00Z", got["current_start_after"])
	})

	t.Run("a refusal is 422 with its machine code", func(t *testing.T) {
		svc := &startAfterTaskService{MockTaskService: &MockTaskService{}, clear: func(context.Context, uuid.UUID, domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error) {
			return nil, domain.NewOutcomeError(domain.CodeInvalidTarget, "compare-clear needs expected_start_after and expected_version")
		}}
		rec := invokeCompareClear(t, taskID.String(), `{"idempotency_key":"k1"}`, NewTaskHandler(svc))
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		var got map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		assert.Equal(t, domain.CodeInvalidTarget, got["error_code"])
	})

	t.Run("unknown field is refused before the service", func(t *testing.T) {
		svc := &startAfterTaskService{MockTaskService: &MockTaskService{}, clear: func(context.Context, uuid.UUID, domain.StartAfterCompareClear) (*domain.StartAfterClearResult, error) {
			t.Fatal("service called")
			return nil, errors.New("unreachable")
		}}
		rec := invokeCompareClear(t, taskID.String(), `{"start_after":null}`, NewTaskHandler(svc))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("a service without the capability is 503", func(t *testing.T) {
		rec := invokeCompareClear(t, taskID.String(), body, NewTaskHandler(&MockTaskService{}))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	t.Run("a bad task id never reaches the service", func(t *testing.T) {
		rec := invokeCompareClear(t, "not-a-task", body, NewTaskHandler(&MockTaskService{}))
		assert.GreaterOrEqual(t, rec.Code, 400)
	})
}
