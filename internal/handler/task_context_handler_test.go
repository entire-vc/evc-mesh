package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/pagination"
)

// GET /tasks/:task_id/context is the endpoint behind the get_task_context tool.
// It was the one artifact read path that never called stripSensitiveMetadata, so
// it served the long-lived TeamRelay share key in the clear to any caller with
// workspace access — and cached it in Redis for 60s on top.
func TestTaskContextHandler_StripsTrAgentKeyFromArtifacts(t *testing.T) {
	taskID := uuid.New()

	h, e := setupTaskContextTest(taskID, json.RawMessage(
		`{"tr_public_url":"https://relay.example.com/f","tr_agent_key":"tr_agent_secret"}`,
	))

	rec := serveTaskContext(t, h, e, taskID)
	body := rec.Body.String()

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, body, "tr_agent_key",
		"tr_agent_key must not appear anywhere in the task-context response")
	assert.NotContains(t, body, "tr_agent_secret",
		"the credential value must not appear anywhere in the task-context response")

	// Negative control: without this the assertion above would also pass on a
	// response that carried no artifact metadata at all, proving nothing.
	assert.Contains(t, body, "tr_public_url",
		"artifact metadata must still be served — otherwise the assertion above is vacuous")
	assert.Contains(t, body, "https://relay.example.com/f")
}

// The redaction must not eat unrelated metadata.
func TestTaskContextHandler_PreservesOtherArtifactMetadata(t *testing.T) {
	taskID := uuid.New()

	h, e := setupTaskContextTest(taskID, json.RawMessage(
		`{"custom":"value","tr_agent_key":"tr_agent_secret"}`,
	))

	rec := serveTaskContext(t, h, e, taskID)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	artifacts, ok := resp["artifacts"].([]any)
	require.True(t, ok, "artifacts should be a list")
	require.Len(t, artifacts, 1)

	meta, ok := artifacts[0].(map[string]any)["metadata"].(map[string]any)
	require.True(t, ok, "metadata should be a JSON object")
	assert.Equal(t, "value", meta["custom"])
	assert.NotContains(t, meta, "tr_agent_key")
}

func setupTaskContextTest(taskID uuid.UUID, meta json.RawMessage) (*TaskContextHandler, *echo.Echo) {
	taskSvc := &MockTaskService{
		GetByIDFunc: func(_ context.Context, id uuid.UUID) (*domain.Task, error) {
			return &domain.Task{ID: id, Title: "t", ProjectID: uuid.New()}, nil
		},
	}
	artifactSvc := &MockArtifactService{
		ListByTaskFunc: func(_ context.Context, _ uuid.UUID, _ pagination.Params) (*pagination.Page[domain.Artifact], error) {
			return &pagination.Page[domain.Artifact]{
				Items: []domain.Artifact{{ID: uuid.New(), Name: "report.md", Metadata: meta}},
			}, nil
		},
	}

	// Must return a non-nil page: the handler treats a nil error as success and
	// dereferences the page directly.
	commentSvc := &MockCommentService{
		ListByTaskFunc: func(_ context.Context, _ uuid.UUID, _ repository.CommentFilter, _ pagination.Params) (*pagination.Page[domain.Comment], error) {
			return &pagination.Page[domain.Comment]{Items: []domain.Comment{}}, nil
		},
	}

	h := NewTaskContextHandler(taskSvc, commentSvc, artifactSvc,
		&MockTaskDependencyService{}, &stubEventBusService{}, nil)
	return h, echo.New()
}

// The shared MockEventBusService does not implement the full interface; this
// test only needs the aggregate call to be non-fatal, and the handler treats
// events as best-effort.
type stubEventBusService struct{ service.EventBusService }

func (s *stubEventBusService) GetContext(_ context.Context, _ uuid.UUID, _ service.GetContextOptions) ([]domain.EventBusMessage, error) {
	return nil, nil
}

func serveTaskContext(t *testing.T, h *TaskContextHandler, e *echo.Echo, taskID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/tasks/:task_id/context")
	c.SetParamNames("task_id")
	c.SetParamValues(taskID.String())
	require.NoError(t, h.GetTaskContext(c))
	return rec
}

// TestArtifactReadPathsAllRedact used to live here as a source-text scanner:
// it grepped this package's non-test .go files for known artifactService read
// calls and required each file to also mention stripSensitiveMetadata. That
// caught the original defect class (#58a0e7aa — this exact handler had the
// read but not the guard) but only within this one package, only at file
// granularity, and only for the read methods it knew the names of (#4b33a6fd).
//
// Redaction is no longer a call each handler must remember — it is on
// domain.Artifact.MarshalJSON, so it runs for every caller in every package
// automatically. See internal/domain/artifact_test.go for the guarantee test,
// including a negative control that constructs an Artifact in a throwaway
// handler this package has never heard of and confirms it still redacts —
// which is the structural equivalent of "a fourth read path can't reopen the
// hole silently", without needing to enumerate read-method names to scan for.
