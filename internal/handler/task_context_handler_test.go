package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// Architecture guard for the defect class, not the single instance.
//
// The bug was not "a field was forgotten" — it was that a redaction guard was
// applied to two of three read paths, and nothing noticed the third. This test
// fails when a handler learns to read artifacts without also redacting them, so
// a fourth path cannot reopen the hole silently.
func TestArtifactReadPathsAllRedact(t *testing.T) {
	// Non-test sources only: this file mentions both strings itself, and matching
	// its own text would make the check pass for the wrong reason.
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	scanned, withReads := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		raw, readErr := os.ReadFile(name)
		require.NoError(t, readErr)
		src := stripGoComments(string(raw))

		// Every way a handler can obtain an artifact from the service layer.
		if !strings.Contains(src, "artifactService.ListByTask") &&
			!strings.Contains(src, "artifactService.GetByIDInWorkspace") &&
			!strings.Contains(src, "artifactService.GetByID(") {
			continue
		}
		withReads++

		// assert.True rather than assert.Contains: the latter dumps the whole
		// source file into the failure output and buries the message.
		assert.True(t, strings.Contains(src, "stripSensitiveMetadata"),
			"%s reads artifacts from the service but never calls stripSensitiveMetadata — "+
				"tr_agent_key and any future secret in artifact metadata would reach the "+
				"API response, which is exactly how GET /tasks/:id/context leaked it", name)
	}

	// Guards the guard: if the match strings ever stop matching (a rename, a
	// wrapper, a move to another package) this test would silently scan nothing
	// and pass. Both counts must be non-zero for a green result to mean anything.
	require.NotZero(t, scanned, "scanned no handler sources — the scan itself is broken")
	require.NotZero(t, withReads,
		"found no artifact read paths at all — the match strings no longer match "+
			"the code, so this test is passing vacuously rather than verifying anything")
}

// stripGoComments blanks out // and /* */ comments so the scan above judges code
// rather than prose. Without it a file that merely *mentions* the guard in a
// comment satisfies the check — which is not hypothetical: the comment written
// above the fix in task_context_handler.go names the function, and an early
// version of this test stayed green when the call itself was deleted.
//
// Deliberately not a Go parser: this only has to be conservative enough that a
// real call site is never blanked, and string literals containing "//" would at
// worst cost a false alarm, never a silent pass.
func stripGoComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))

	inBlock, inLine := false, false
	for i := 0; i < len(src); i++ {
		switch {
		case inLine:
			if src[i] == '\n' {
				inLine = false
				out.WriteByte('\n')
			}
		case inBlock:
			if src[i] == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			i++
		case src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		default:
			out.WriteByte(src[i])
		}
	}
	return out.String()
}
