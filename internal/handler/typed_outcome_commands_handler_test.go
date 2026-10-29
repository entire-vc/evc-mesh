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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// The HTTP contract of the three commands: status codes, the 422 error_code
// of a typed refusal, and the body/path validation the handler owns. The
// service is replaced by a recorder; its own behaviour is tested in service.
type recordedCommands struct {
	err      error
	taskID   uuid.UUID
	request  uuid.UUID
	ask      service.AskAgentInput
	answer   service.AnswerDecisionInput
	finish   service.FinishTaskInput
	finished bool
}

func (r *recordedCommands) AskAgent(_ context.Context, taskID uuid.UUID, in service.AskAgentInput) (*domain.CommandResult, error) {
	r.taskID, r.ask = taskID, in
	if r.err != nil {
		return nil, r.err
	}
	return &domain.CommandResult{CommentID: uuid.New()}, nil
}

func (r *recordedCommands) AnswerDecision(_ context.Context, id uuid.UUID, in service.AnswerDecisionInput) (*domain.AnswerDecisionResult, error) {
	r.request, r.answer = id, in
	if r.err != nil {
		return nil, r.err
	}
	return &domain.AnswerDecisionResult{CommentID: uuid.New()}, nil
}

func (r *recordedCommands) FinishTask(_ context.Context, taskID uuid.UUID, in service.FinishTaskInput) (*domain.CommandResult, error) {
	r.taskID, r.finish, r.finished = taskID, in, true
	if r.err != nil {
		return nil, r.err
	}
	return &domain.CommandResult{CommentID: uuid.New()}, nil
}

func callCommand(t *testing.T, h echo.HandlerFunc, param, value, body string) (rec *httptest.ResponseRecorder, out map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames(param)
	c.SetParamValues(value)
	require.NoError(t, h(c))
	out = map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestTypedOutcomeCommandHandler_StatusCodes(t *testing.T) {
	cmds := &recordedCommands{}
	h := NewTypedOutcomeCommandHandler(cmds, nil)
	task, req := uuid.New(), uuid.New()

	rec, _ := callCommand(t, h.AskAgent, "task_id", task.String(), `{"to":"carol","question":"v1 or v2?","session_id":"s1"}`)
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, task, cmds.taskID)
	assert.Equal(t, "carol", cmds.ask.To)
	require.NotNil(t, cmds.ask.SessionID)
	assert.Equal(t, "s1", *cmds.ask.SessionID)

	rec, _ = callCommand(t, h.AnswerDecision, "decision_request_id", req.String(), `{"answer":"v2"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, req, cmds.request)
	assert.Equal(t, "v2", cmds.answer.Answer)

	rec, _ = callCommand(t, h.FinishTask, "task_id", task.String(),
		`{"result_kind":"change","evidence":"go test ok","evidence_links":["https://x.test/1"],"to":"review"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, service.FinishTaskInput{ResultKind: "change", Evidence: "go test ok",
		EvidenceLinks: []string{"https://x.test/1"}, To: "review"}, cmds.finish)
}

func TestTypedOutcomeCommandHandler_Refusals(t *testing.T) {
	task := uuid.New().String()
	cases := []struct {
		name   string
		err    error
		call   func(h *TypedOutcomeCommandHandler) echo.HandlerFunc
		param  string
		value  string
		body   string
		status int
		code   string
	}{
		{"finish without result_kind", domain.NewOutcomeError(domain.CodeMissingResultKind, "result_kind is required"),
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.FinishTask }, "task_id", task, `{}`, 422, domain.CodeMissingResultKind},
		{"ask a person", domain.NewOutcomeError(domain.CodeNotAnAgent, "no agent alex"),
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.AskAgent }, "task_id", task, `{"to":"alex"}`, 422, domain.CodeNotAnAgent},
		{"answer by a stranger", apierror.Forbidden("only the addressee"),
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.AnswerDecision }, "decision_request_id", task, `{"answer":"x"}`, 403, ""},
		{"ask on a bad task id", nil,
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.AskAgent }, "task_id", "not-a-task", `{}`, 400, ""},
		{"finish on a bad task id", nil,
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.FinishTask }, "task_id", "not-a-task", `{}`, 400, ""},
		{"answer on a bad request id", nil,
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.AnswerDecision }, "decision_request_id", "nope", `{}`, 400, ""},
		{"ask with a broken body", nil,
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.AskAgent }, "task_id", task, `{"to":`, 400, ""},
		{"answer with a broken body", nil,
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.AnswerDecision }, "decision_request_id", task, `[`, 400, ""},
		{"finish with a broken body", nil,
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.FinishTask }, "task_id", task, `{"to":1`, 400, ""},
		{"unexpected failure", errors.New("db down"),
			func(h *TypedOutcomeCommandHandler) echo.HandlerFunc { return h.FinishTask }, "task_id", task, `{}`, 500, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewTypedOutcomeCommandHandler(&recordedCommands{err: tc.err}, nil)
			rec, out := callCommand(t, tc.call(h), tc.param, tc.value, tc.body)
			assert.Equal(t, tc.status, rec.Code)
			if tc.code != "" {
				assert.Equal(t, tc.code, out["error_code"])
			}
		})
	}
}
