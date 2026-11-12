package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/service"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

// turnStore is the transactional writer behind the turn commands: it records
// what was committed and answers the checkout read as told.
type turnStore struct {
	notHeld   bool
	committed []*domain.TurnCommand
}

func (s *turnStore) CommitTurn(_ context.Context, cmd *domain.TurnCommand) (*domain.TurnCommandResult, error) {
	s.committed = append(s.committed, cmd)
	res := &domain.TurnCommandResult{Wait: cmd.Wait, Released: true, Task: domain.TurnCommandTask{ID: cmd.TaskID}}
	if cmd.CommentID != nil {
		res.CommentID = *cmd.CommentID
	}
	return res, nil
}

func (s *turnStore) AddNote(context.Context, uuid.UUID, uuid.UUID, domain.ActorType, string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *turnStore) TaskScope(context.Context, uuid.UUID) (workspaceID, projectID uuid.UUID, err error) {
	return uuid.New(), uuid.New(), nil
}

func (s *turnStore) ResolveAgent(context.Context, uuid.UUID, string) (agentID uuid.UUID, name string, found bool, err error) {
	return uuid.Nil, "", false, nil
}

func (s *turnStore) HoldsCheckout(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return !s.notHeld, nil
}

// humanGate posts the question and arms the gate for its author, or refuses
// the arm check with refuse.
type humanGate struct {
	refuse error
	posted []*domain.Comment
	gated  map[uuid.UUID]uuid.UUID
}

func (g *humanGate) CheckAgentGateArm(context.Context, uuid.UUID, string, string, string) error {
	return g.refuse
}

func (g *humanGate) Create(_ context.Context, c *domain.Comment) error {
	g.posted = append(g.posted, c)
	g.gated[c.TaskID] = c.AuthorID
	return nil
}

func (g *humanGate) GetByID(_ context.Context, id uuid.UUID) (*domain.Task, error) {
	t := &domain.Task{ID: id}
	if author, ok := g.gated[id]; ok {
		t.HumanGate, t.GateAuthor = true, &author
	}
	return t, nil
}

type turnHandlerEnv struct {
	h     *TurnCommandHandler
	store *turnStore
	gate  *humanGate
	agent uuid.UUID
	task  uuid.UUID
}

func newTurnHandlerEnv() *turnHandlerEnv {
	e := &turnHandlerEnv{store: &turnStore{}, gate: &humanGate{gated: map[uuid.UUID]uuid.UUID{}}, agent: uuid.New(), task: uuid.New()}
	svc := service.NewTurnCommandService(e.store, nil, nil, nil).WithHumanGate(e.gate, e.gate)
	e.h = NewTurnCommandHandler(svc, nil)
	return e
}

// call runs one handler as actor (uuid.Nil: no authenticated agent) and
// returns the status and the decoded body.
func callTurnHandler(t *testing.T, h echo.HandlerFunc, actor uuid.UUID, taskID, body string) (status int, payload map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if actor != uuid.Nil {
		req = req.WithContext(actorctx.WithActor(req.Context(), actor, domain.ActorTypeAgent))
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames("task_id")
	c.SetParamValues(taskID)
	require.NoError(t, h(c))
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestTurnCommandHandler_AskHuman(t *testing.T) {
	deadline := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		name      string
		task      string
		anonymous bool
		body      string
		setup     func(e *turnHandlerEnv)
		status    int
		errorCode string
	}{
		{name: "malformed task id", task: "not-a-task", body: `{}`, status: http.StatusBadRequest},
		{name: "malformed body", body: `{"to":`, status: http.StatusBadRequest},
		{name: "deadline not a time", body: `{"to":"alex","question":"q?","deadline":"tomorrow"}`, status: http.StatusBadRequest},
		{name: "a person cannot end an agent's turn", anonymous: true, body: `{"to":"alex","question":"q?"}`, status: http.StatusForbidden},
		{name: "no question", body: `{"to":"alex"}`, status: http.StatusBadRequest},
		{name: "no addressee", body: `{"question":"q?"}`, status: http.StatusUnprocessableEntity, errorCode: service.GateRefusalAddresseeMissing},
		{name: "addressee not a username", body: `{"to":"Alex User","question":"q?"}`, status: http.StatusUnprocessableEntity, errorCode: domain.CodeInvalidTarget},
		{name: "not the checkout holder", body: `{"to":"alex","question":"q?"}`,
			setup: func(e *turnHandlerEnv) { e.store.notHeld = true }, status: http.StatusConflict},
		{name: "addressee is an agent", body: `{"to":"developer","question":"q?"}`, setup: func(e *turnHandlerEnv) {
			e.gate.refuse = &service.GateArmRefusedError{Code: service.GateRefusalAddresseeNotUser, Message: "@developer is not a person"}
		}, status: http.StatusUnprocessableEntity, errorCode: service.GateRefusalAddresseeNotUser},
		{name: "repeat of an answered ask", body: `{"to":"alex","question":"q?"}`, setup: func(e *turnHandlerEnv) {
			e.gate.refuse = &service.GateArmRefusedError{Code: service.GateRefusalRepeatAsk, Message: "already answered"}
		}, status: http.StatusUnprocessableEntity, errorCode: service.GateRefusalRepeatAsk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTurnHandlerEnv()
			if tc.setup != nil {
				tc.setup(e)
			}
			task, actor := e.task.String(), e.agent
			if tc.task != "" {
				task = tc.task
			}
			if tc.anonymous {
				actor = uuid.Nil
			}
			status, body := callTurnHandler(t, e.h.AskHuman, actor, task, tc.body)
			assert.Equal(t, tc.status, status, body)
			if tc.errorCode != "" {
				assert.Equal(t, tc.errorCode, body["error_code"])
			}
			assert.Empty(t, e.gate.posted, "a refused ask posted the question")
			assert.Empty(t, e.store.committed)
		})
	}

	t.Run("created", func(t *testing.T) {
		e := newTurnHandlerEnv()
		status, body := callTurnHandler(t, e.h.AskHuman, e.agent, e.task.String(),
			`{"to":"@alex","question":"Ship it?","recommended_default":"ship Monday","deadline":"`+deadline+`","soft":true,"canonical_key":"ship","supersedes":"the date","session_id":"s-1","turn_id":"t-1"}`)
		require.Equal(t, http.StatusCreated, status, body)
		require.Len(t, e.gate.posted, 1)
		assert.Equal(t, e.gate.posted[0].ID.String(), body["comment_id"])
		wait := body["wait"].(map[string]any)
		assert.Equal(t, "human", wait["kind"])
		assert.Equal(t, "alex", wait["addressee"])
		assert.Equal(t, "Ship it?", wait["question"])
		assert.Equal(t, "comment:"+e.gate.posted[0].ID.String(), wait["human_gate_ref"])
		cmd := e.store.committed[0]
		assert.Equal(t, "s-1", *cmd.SessionID)
		assert.Equal(t, "t-1", *cmd.TurnID)
		assert.Contains(t, e.gate.posted[0].Body, "[soft]")
	})
}

// The fields added with merge and registered reach the service from the body,
// and a stray one is a 422, not a silently dropped field.
func TestTurnCommandHandler_WaitForTypedFields(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	e := newTurnHandlerEnv()
	status, body := callTurnHandler(t, e.h.WaitFor, e.agent, e.task.String(),
		`{"on":"registered","registration":"receipt","note":"wait for the receipt"}`)
	require.Equal(t, http.StatusCreated, status, body)
	w := e.store.committed[0].Wait
	assert.Equal(t, domain.TaskWaitRegistered, w.Kind)
	assert.Equal(t, e.task.String()[:8], *w.RegistrationRef)

	status, body = callTurnHandler(t, e.h.WaitFor, e.agent, e.task.String(),
		`{"on":"pipeline","provider":"github","project_path":"org/repo","pipeline_id":42,"until":"changed","after_sha":"`+sha+`","after_pipeline_id":42,"after_status":"failed"}`)
	require.Equal(t, http.StatusCreated, status, body)
	w = e.store.committed[1].Wait
	assert.Equal(t, sha, *w.AfterSHA)
	assert.Equal(t, int64(42), *w.AfterPipelineID)
	assert.Equal(t, "failed", *w.AfterStatus)

	for name, b := range map[string]string{
		"stray after_sha":        `{"on":"registered","registration":"receipt","after_sha":"` + sha + `"}`,
		"stray registration_ref": `{"on":"time","registration_ref":"x"}`,
		"unknown registration":   `{"on":"registered","registration":"poll","registration_ref":"x"}`,
		"human through wait_for": `{"on":"human"}`,
	} {
		status, body = callTurnHandler(t, e.h.WaitFor, e.agent, e.task.String(), b)
		assert.Equal(t, http.StatusUnprocessableEntity, status, name)
		assert.Equal(t, domain.CodeInvalidTarget, body["error_code"], name)
	}
	assert.Len(t, e.store.committed, 2, "a refused wait reached the store")
}

// waitStore holds one wait for the resolve route.
type waitStore struct {
	w  *domain.TaskWait
	ws uuid.UUID
}

func (s *waitStore) Declare(context.Context, *domain.TaskWait) error { return nil }
func (s *waitStore) GetActive(context.Context, uuid.UUID, *uuid.UUID) (*domain.TaskWait, error) {
	return nil, nil
}
func (s *waitStore) Resolve(context.Context, uuid.UUID, domain.WaitResolution) (bool, error) {
	return false, nil
}
func (s *waitStore) GetForTask(_ context.Context, taskID, waitID uuid.UUID) (*domain.TaskWait, uuid.UUID, error) {
	if waitID != s.w.ID || taskID != s.w.TaskID {
		return nil, uuid.Nil, nil
	}
	cp := *s.w
	return &cp, s.ws, nil
}
func (s *waitStore) ResolveWithCause(_ context.Context, _, _ uuid.UUID, res domain.WaitResolution, cause string, by uuid.UUID) (*domain.TaskWait, bool, error) {
	if s.w.ResolvedAt != nil {
		cp := *s.w
		return &cp, false, nil
	}
	now := time.Now()
	s.w.ResolvedAt, s.w.Resolution, s.w.ResolveCause, s.w.ResolvedBy = &now, &res, &cause, &by
	cp := *s.w
	return &cp, true, nil
}

func TestTypedOutcomeHandler_ResolveWait(t *testing.T) {
	controller := uuid.New()
	setup := func() (*TypedOutcomeHandler, *waitStore) {
		store := &waitStore{ws: uuid.New(), w: &domain.TaskWait{ID: uuid.New(), TaskID: uuid.New(), AgentID: uuid.New(), Kind: domain.TaskWaitMR}}
		ctl := service.WithControllerCheck(func(_ context.Context, ws uuid.UUID, by service.WaitResolver) (bool, error) {
			return ws == store.ws && by.AgentID == controller && by.AuthWorkspaceID == ws && !by.Connector, nil
		})
		return NewTypedOutcomeHandler(service.NewTypedOutcomeService(store, nil, ctl), nil), store
	}
	type caller struct {
		agent, ws uuid.UUID
		connector bool
	}
	call := func(h *TypedOutcomeHandler, who caller, task, wait, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.SetParamNames("task_id", "wait_id")
		c.SetParamValues(task, wait)
		if who.agent != uuid.Nil {
			c.Set(mw.ContextKeyAgentID, who.agent)
			c.Set(mw.ContextKeyAgentAuthWorkspaceID, who.ws)
		}
		if who.connector {
			c.Set(mw.ContextKeyOAuthConnectorUserID, uuid.New())
		}
		require.NoError(t, h.ResolveWait(c))
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	refused := []struct {
		name   string
		who    func(s *waitStore) caller
		task   func(s *waitStore) string
		wait   func(s *waitStore) string
		body   string
		status int
	}{
		{name: "malformed task id", task: func(*waitStore) string { return "nope" }, status: http.StatusBadRequest},
		{name: "malformed wait id", wait: func(*waitStore) string { return "nope" }, status: http.StatusBadRequest},
		{name: "malformed body", body: `{"cause":`, status: http.StatusBadRequest},
		{name: "a person", who: func(*waitStore) caller { return caller{} }, status: http.StatusForbidden},
		{name: "another agent", who: func(s *waitStore) caller { return caller{agent: uuid.New(), ws: s.ws} }, status: http.StatusForbidden},
		{name: "controller key for another workspace", who: func(*waitStore) caller { return caller{agent: controller, ws: uuid.New()} }, status: http.StatusForbidden},
		{name: "controller through an OAuth connector", who: func(s *waitStore) caller { return caller{agent: controller, ws: s.ws, connector: true} }, status: http.StatusForbidden},
		{name: "superseded is not the resolver's", body: `{"resolution":"superseded","cause":"x"}`, status: http.StatusBadRequest},
		{name: "no cause", body: `{"resolution":"met"}`, status: http.StatusBadRequest},
		{name: "wait of another card", task: func(*waitStore) string { return uuid.NewString() }, status: http.StatusNotFound},
		{name: "unknown wait", wait: func(*waitStore) string { return uuid.NewString() }, status: http.StatusNotFound},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup()
			who := caller{agent: s.w.AgentID, ws: s.ws}
			task, wait, body := s.w.TaskID.String(), s.w.ID.String(), `{"cause":"MR merged"}`
			if tc.who != nil {
				who = tc.who(s)
			}
			if tc.task != nil {
				task = tc.task(s)
			}
			if tc.wait != nil {
				wait = tc.wait(s)
			}
			if tc.body != "" {
				body = tc.body
			}
			status, out := call(h, who, task, wait, body)
			assert.Equal(t, tc.status, status, out)
			assert.Nil(t, s.w.ResolvedAt, "a refused resolve ended the wait")
		})
	}

	t.Run("own agent, repeat, conflict", func(t *testing.T) {
		h, s := setup()
		own := caller{agent: s.w.AgentID, ws: s.ws}
		status, out := call(h, own, s.w.TaskID.String(), s.w.ID.String(), `{"cause":"MR g/p!12 merged"}`)
		require.Equal(t, http.StatusOK, status, out)
		assert.Equal(t, true, out["resolved"])
		wait := out["wait"].(map[string]any)
		assert.Equal(t, "met", wait["resolution"])
		assert.Equal(t, "MR g/p!12 merged", wait["resolve_cause"])
		assert.Equal(t, s.w.AgentID.String(), wait["resolved_by"])

		status, out = call(h, own, s.w.TaskID.String(), s.w.ID.String(), `{"resolution":"met","cause":"again"}`)
		require.Equal(t, http.StatusOK, status, out)
		assert.Equal(t, false, out["resolved"])
		assert.Equal(t, "MR g/p!12 merged", out["wait"].(map[string]any)["resolve_cause"])

		status, _ = call(h, own, s.w.TaskID.String(), s.w.ID.String(), `{"resolution":"invalid","cause":"x"}`)
		assert.Equal(t, http.StatusConflict, status)
	})

	t.Run("controller of the workspace", func(t *testing.T) {
		h, s := setup()
		status, out := call(h, caller{agent: controller, ws: s.ws}, s.w.TaskID.String(), s.w.ID.String(), `{"resolution":"expired","cause":"registration expired"}`)
		require.Equal(t, http.StatusOK, status, out)
		assert.Equal(t, "expired", out["wait"].(map[string]any)["resolution"])
		assert.Equal(t, controller.String(), out["wait"].(map[string]any)["resolved_by"])
	})
}

// A note longer than the wait row holds is no refusal: the comment keeps the
// whole text, the wait row its first 500 characters. (A card target needs the
// route's access check; the DB test covers on=card.)
func TestTurnCommandHandler_WaitForLongNote(t *testing.T) {
	for name, note := range map[string]string{
		"2000 ascii":   strings.Repeat("a", 1999) + "Z",
		"2000 unicode": strings.Repeat("я", 1999) + "ж",
	} {
		t.Run(name, func(t *testing.T) {
			e := newTurnHandlerEnv()
			raw, err := json.Marshal(map[string]any{"on": "registered", "registration": "receipt", "note": note})
			require.NoError(t, err)
			status, body := callTurnHandler(t, e.h.WaitFor, e.agent, e.task.String(), string(raw))
			require.Equal(t, http.StatusCreated, status, body)
			require.Len(t, e.store.committed, 1)
			cmd := e.store.committed[0]
			assert.True(t, strings.HasSuffix(cmd.CommentBody, "\n\n"+note), "the comment keeps the whole note")
			assert.Equal(t, domain.MaxWaitNoteLen, utf8.RuneCountInString(cmd.Wait.Note))
			assert.True(t, utf8.ValidString(cmd.Wait.Note))
			assert.True(t, strings.HasPrefix(note, cmd.Wait.Note))
			assert.LessOrEqual(t, utf8.RuneCountInString(cmd.Note), domain.MaxOutcomeNote)
		})
	}
}

// Every refusal of wait_for names the field and what is wrong with it, so the
// lane can fix the call instead of falling back to a marker line.
func TestTurnCommandHandler_WaitForRefusalsNameTheField(t *testing.T) {
	e := newTurnHandlerEnv()
	sha := "0123456789abcdef0123456789abcdef01234567"
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, tc := range []struct {
		name, body, code, field string
		status                  int
	}{
		{"card no target", `{"on":"card"}`, domain.CodeInvalidTarget, "target_task_id", 422},
		{"mr no iid", `{"on":"mr","provider":"gitlab","project_path":"g/p","until":"merged"}`, domain.CodeInvalidTarget, "mr_iid", 422},
		{"mr bad until", `{"on":"mr","provider":"gitlab","project_path":"g/p","mr_iid":1,"until":"finished"}`, domain.CodeInvalidTarget, "until", 422},
		{"mr bad baseline", `{"on":"mr","provider":"gitlab","project_path":"g/p","mr_iid":1,"until":"merged","after_sha":"abc"}`, domain.CodeInvalidTarget, "after_sha", 422},
		{"pipeline no id", `{"on":"pipeline","project_path":"g/p","until":"finished"}`, domain.CodeInvalidTarget, "pipeline_id", 422},
		{"pipeline bad provider", `{"on":"pipeline","provider":"bitbucket","project_path":"g/p","pipeline_id":1,"until":"finished"}`, domain.CodeInvalidTarget, "provider", 422},
		{"merge no sha", `{"on":"merge","project_path":"g/p","mr_iid":1}`, domain.CodeInvalidTarget, "after_sha", 422},
		{"merge with until", `{"on":"merge","project_path":"g/p","mr_iid":1,"after_sha":"` + sha + `","until":"merged"}`, domain.CodeInvalidTarget, "project_path, mr_iid and after_sha only", 422},
		{"registered unknown", `{"on":"registered","registration":"poll","registration_ref":"x"}`, domain.CodeInvalidTarget, "registration", 422},
		{"stray after_sha", `{"on":"registered","registration":"receipt","after_sha":"` + sha + `"}`, domain.CodeInvalidTarget, "after_sha", 422},
		{"manual no note", `{"on":"manual"}`, domain.CodeInvalidTarget, "note", 422},
		{"time no until_at", `{"on":"time"}`, domain.CodeInvalidTarget, "until_at", 422},
		{"time relative until", `{"on":"time","until":"6h"}`, domain.CodeNaiveTime, "until_at", 422},
		{"time no evidence", `{"on":"time","until_at":"` + future + `"}`, domain.CodeMissingEvidence, "event_ref, source and why", 422},
		{"time long why", `{"on":"time","until_at":"` + future + `","event_ref":"nightly-export","source":"https://status.example.com","why":"` + strings.Repeat("я", 501) + `"}`, domain.CodeMissingEvidence, "why", 422},
		{"time bad source", `{"on":"time","until_at":"` + future + `","event_ref":"nightly-export","source":"card:00000000","why":"the export publishes the file"}`, domain.CodeMissingEvidence, "source", 422},
		{"unknown kind", `{"on":"someday"}`, domain.CodeInvalidTarget, "on must be", 422},
		{"session_id too long", `{"on":"registered","registration":"receipt","session_id":"` + strings.Repeat("s", 201) + `"}`, "", "session_id must be at most 200 characters", 400},
		{"turn_id too long", `{"on":"registered","registration":"receipt","turn_id":"` + strings.Repeat("t", 201) + `"}`, "", "turn_id must be at most 200 characters", 400},
		{"body cut short", `{"on":"registered",`, "", "body is not valid JSON: it ends before", 400},
		{"body not JSON", `{"on" "registered"}`, "", "body is not valid JSON at byte", 400},
		{"body not an object", `["registered"]`, "", "body must be a JSON object, got array", 400},
		{"mr_iid of the wrong type", `{"on":"mr","provider":"gitlab","project_path":"g/p","mr_iid":"7","until":"merged"}`, "", "mr_iid", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := callTurnHandler(t, e.h.WaitFor, e.agent, e.task.String(), tc.body)
			require.Equal(t, tc.status, status, body)
			if tc.code != "" {
				assert.Equal(t, tc.code, body["error_code"])
			}
			msg, _ := body["message"].(string)
			assert.Contains(t, msg, tc.field, "the refusal names the field and the reason")
		})
	}
	assert.Empty(t, e.store.committed, "a refused wait reached the store")
}

// 251 Cyrillic characters fit the 500-character why of a time wait; the
// bound counts characters, not bytes.
func TestTurnCommandHandler_WaitForUnicodeEvidenceCountsCharacters(t *testing.T) {
	e := newTurnHandlerEnv()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	status, body := callTurnHandler(t, e.h.WaitFor, e.agent, e.task.String(),
		`{"on":"time","until_at":"`+future+`","event_ref":"nightly-export","source":"https://status.example.com","why":"`+strings.Repeat("я", 400)+`"}`)
	require.Equal(t, http.StatusCreated, status, body)
}
