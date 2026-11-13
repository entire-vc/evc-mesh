package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// Contract of the result-write precondition (
// docs/extensions/agents-me-contract.md "Fencing a stale worker"): the four
// result writes read the same headers, and a refusal is 409 with a stable
// error_code. A failure here is a breaking change for external workers.

// fencedRoute is one result write with a service double that records the
// fence it received and answers with err.
type fencedRoute struct {
	name    string
	body    string
	ctype   string
	handler func(err error, got **domain.ResultFence) echo.HandlerFunc
}

func fencedRoutes() []fencedRoute {
	return []fencedRoute{
		{"POST /tasks/:id/comments", `{"body":"result"}`, echo.MIMEApplicationJSON,
			func(err error, got **domain.ResultFence) echo.HandlerFunc {
				return NewCommentHandler(&MockCommentService{CreateFunc: func(_ context.Context, c *domain.Comment) error {
					*got = c.Fence
					return err
				}}, nil).Create
			}},
		{"POST /tasks/:id/artifacts", `{"name":"r.txt","content":"aGk=","encoding":"base64"}`, echo.MIMEApplicationJSON,
			func(err error, got **domain.ResultFence) echo.HandlerFunc {
				return NewArtifactHandler(&MockArtifactService{UploadFunc: func(_ context.Context, in service.UploadArtifactInput) (*domain.Artifact, error) {
					*got = in.Fence
					if err != nil {
						return nil, err
					}
					return &domain.Artifact{ID: uuid.New()}, nil
				}}, nil).Upload
			}},
		{"POST /tasks/:id/move", `{"position":3}`, echo.MIMEApplicationJSON,
			func(err error, got **domain.ResultFence) echo.HandlerFunc {
				return NewTaskHandler(&MockTaskService{MoveTaskFunc: func(_ context.Context, _ uuid.UUID, in service.MoveTaskInput) error {
					*got = in.Fence
					return err
				}}).MoveTask
			}},
		{"POST /tasks/:id/finish", `{"result_kind":"change","evidence":"ok","to":"done"}`, echo.MIMEApplicationJSON,
			func(err error, got **domain.ResultFence) echo.HandlerFunc {
				rc := &recordedCommands{err: err}
				return func(c echo.Context) error {
					herr := NewTypedOutcomeCommandHandler(rc, nil).FinishTask(c)
					*got = rc.finish.Fence
					return herr
				}
			}},
	}
}

func callFenced(t *testing.T, r fencedRoute, h echo.HandlerFunc, task, actor uuid.UUID, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(r.body))
	req.Header.Set(echo.HeaderContentType, r.ctype)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = req.WithContext(actorctx.WithActor(req.Context(), actor, domain.ActorTypeAgent))
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("agent_id", actor)
	c.SetParamNames("task_id")
	c.SetParamValues(task.String())
	require.NoError(t, h(c))
	return rec
}

func TestContract_ResultFence_HeadersReachEveryResultWrite(t *testing.T) {
	task, actor, token, session := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, r := range fencedRoutes() {
		t.Run(r.name, func(t *testing.T) {
			var got *domain.ResultFence
			rec := callFenced(t, r, r.handler(nil, &got), task, actor, nil)
			require.Less(t, rec.Code, 300, rec.Body.String())
			assert.Nil(t, got, "no header: no precondition, the route behaves as before")

			got = nil
			rec = callFenced(t, r, r.handler(nil, &got), task, actor, map[string]string{
				"X-Mesh-Checkout-Token": token.String(), "X-Mesh-Expected-Input-Revision": "4"})
			require.Less(t, rec.Code, 300, rec.Body.String())
			require.NotNil(t, got)
			assert.Equal(t, task, got.TaskID)
			require.NotNil(t, got.Token)
			assert.Equal(t, token, *got.Token)
			require.NotNil(t, got.ExpectedInputRevision)
			assert.EqualValues(t, 4, *got.ExpectedInputRevision)

			got = nil
			rec = callFenced(t, r, r.handler(nil, &got), task, actor, map[string]string{
				"X-Mesh-Checkout-Session-Id": session.String(), "X-Mesh-Checkout-Generation": "7"})
			require.Less(t, rec.Code, 300, rec.Body.String())
			require.NotNil(t, got)
			assert.Equal(t, actor, got.Holder, "the session form binds to the caller")
			require.NotNil(t, got.SessionID)
			assert.Equal(t, session, *got.SessionID)
			assert.EqualValues(t, 7, got.Generation)
		})
	}
}

func TestContract_ResultFence_MalformedHeaderIs400(t *testing.T) {
	for _, r := range fencedRoutes() {
		for name, h := range map[string]map[string]string{
			"bad token":             {"X-Mesh-Checkout-Token": "nope"},
			"session without gen":   {"X-Mesh-Checkout-Session-Id": uuid.NewString()},
			"token and session":     {"X-Mesh-Checkout-Token": uuid.NewString(), "X-Mesh-Checkout-Session-Id": uuid.NewString(), "X-Mesh-Checkout-Generation": "1"},
			"zero generation":       {"X-Mesh-Checkout-Session-Id": uuid.NewString(), "X-Mesh-Checkout-Generation": "0"},
			"bad expected revision": {"X-Mesh-Expected-Input-Revision": "x"},
			"bad session id":        {"X-Mesh-Checkout-Session-Id": "nope", "X-Mesh-Checkout-Generation": "1"},
			"session, no actor":     {"X-Mesh-Checkout-Session-Id": uuid.NewString(), "X-Mesh-Checkout-Generation": "1", "-anonymous": ""},
		} {
			t.Run(r.name+"/"+name, func(t *testing.T) {
				actor := uuid.New()
				if _, anonymous := h["-anonymous"]; anonymous {
					actor = uuid.Nil
				}
				var got *domain.ResultFence
				rec := callFenced(t, r, r.handler(nil, &got), uuid.New(), actor, h)
				assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), `"error_code":"`+apierror.CodeBadRequest+`"`, "the 400 keeps the API error shape")
				assert.Nil(t, got, "a malformed precondition never reaches the write")
			})
		}
	}
}

// A JSON result write whose body does not parse is a 400 and never reaches
// the write, fenced or not.
func TestContract_ResultFence_MalformedBodyIs400(t *testing.T) {
	for _, r := range fencedRoutes() {
		t.Run(r.name, func(t *testing.T) {
			r.body = `{"body":`
			var got *domain.ResultFence
			rec := callFenced(t, r, r.handler(nil, &got), uuid.New(), uuid.New(), map[string]string{"X-Mesh-Checkout-Token": uuid.NewString()})
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Nil(t, got)
		})
	}
}

// The refusal body: 409, error_code checkout_precondition_failed or
// input_revision_changed, never the current token.
func TestContract_ResultFence_RefusalIs409WithStableCode(t *testing.T) {
	task, actor, token, current := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	later := now.Add(time.Hour)
	stale := &domain.ResultFence{TaskID: task, Holder: actor, Token: &token}
	lockErr := stale.Check(domain.ResultFenceState{Holder: &actor, Token: &current, Expires: &later, Generation: 8, InputRevision: 3, Now: now})
	rev := int64(2)
	revErr := (&domain.ResultFence{TaskID: task, ExpectedInputRevision: &rev}).Check(domain.ResultFenceState{InputRevision: 3, Now: now})
	require.Error(t, lockErr)
	require.Error(t, revErr)

	for _, r := range fencedRoutes() {
		for code, err := range map[string]error{"checkout_precondition_failed": lockErr, "input_revision_changed": revErr} {
			t.Run(r.name+"/"+code, func(t *testing.T) {
				var got *domain.ResultFence
				rec := callFenced(t, r, r.handler(err, &got), task, actor, map[string]string{"X-Mesh-Checkout-Token": token.String()})
				require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
				body := contractKeys(t, rec.Body.Bytes())
				requireContractKeys(t, body, "code", "error_code", "message")
				assert.JSONEq(t, strconv.Quote(code), string(body["error_code"]))
				assert.NotContains(t, rec.Body.String(), current.String(), "a refusal never reveals the live token")
			})
		}
	}
}
